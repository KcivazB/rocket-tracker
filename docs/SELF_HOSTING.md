# Self-hosting Rocket Tracker

```
 Gaming PC (Windows)                         Homelab
┌──────────────────────────┐   HTTPS    ┌──────────────────────────────┐     ┌───────────┐
│ Rocket League ─ Stats API│            │ reverse proxy (TLS)          │     │ Authentik │
│        │ localhost:49123 │            │        │                     │     │  (OIDC)   │
│ rltracker agent ─────────┼──────────► │ rltracker-server :8080 ◄─────┼────►│           │
│  outbox\ (offline queue) │  device    │  SQLite /data                │sign-│           │
└──────────────────────────┘  token     └──────────────────────────────┘ in  └───────────┘
                                                 ▲ browsers (session cookie)
```

- **Agents** authenticate with a per-device token (`rtk_…`), created by each player in the dashboard
  (**Devices**). Only a SHA-256 hash of it is stored; revoking it takes effect immediately.
- **Browsers** sign in with OpenID Connect (authorization code + PKCE). The session is an HttpOnly cookie
  (30 days, sliding). State-changing requests must come from the dashboard's own origin.
- All signed-in players can see each other's statistics; only their owner can edit a match, tag, manual entry
  or setting.

## 1. Authentik

In the Authentik admin interface:

1. **Applications → Providers → Create → OAuth2/OpenID Provider**
   - Name: `Rocket Tracker`
   - Authorization flow: your usual one (e.g. `default-provider-authorization-implicit-consent`)
   - Client type: **Confidential**
   - Redirect URIs: `https://rl.example.com/auth/callback` (strict)
   - Signing key: pick a certificate (e.g. `authentik Self-signed Certificate`) — **required**: without one,
     Authentik signs ID tokens with HS256, which the server rejects.
   - Scopes: keep `openid`, `email`, `profile` (the default mappings also send `groups`)
   - Note the **Client ID** and **Client Secret**.
2. **Applications → Applications → Create**
   - Name: `Rocket Tracker`, slug: `rocket-tracker`, provider: the one above,
     launch URL: `https://rl.example.com`.
   - To restrict access, bind a group/policy to the application, or use `RT_OIDC_ALLOWED_GROUPS` below.
3. The **issuer** is shown in the provider's overview ("OpenID Configuration Issuer"), typically
   `https://auth.example.com/application/o/rocket-tracker/` — copy it exactly, trailing slash included.

Any other OpenID Connect provider (Keycloak, Authelia, Pocket ID, Zitadel…) works the same way: a confidential
client, redirect URI `<public URL>/auth/callback`, scopes `openid profile email`.

## 2. Docker Compose

```bash
mkdir rocket-tracker && cd rocket-tracker
git clone <this repo> src
cp src/docker-compose.example.yml docker-compose.yml   # then set build: ./src
printf '%s' 'the-client-secret' > oidc_client_secret.txt && chmod 600 oidc_client_secret.txt
mkdir data && sudo chown 65532:65532 data              # the image runs as the distroless "nonroot" user
docker compose up -d --build
docker compose logs -f
```

### Configuration

| Variable | Default | |
|---|---|---|
| `RT_PUBLIC_URL` | — (required) | External URL, e.g. `https://rl.example.com`. Used for the redirect URI, cookies and CSRF checks. |
| `RT_OIDC_ISSUER` | — | Issuer URL of the provider. |
| `RT_OIDC_CLIENT_ID` | — | |
| `RT_OIDC_CLIENT_SECRET` / `RT_OIDC_CLIENT_SECRET_FILE` | — | The secret, or a file containing it (Docker secret). |
| `RT_OIDC_ALLOWED_GROUPS` | (all) | Comma-separated groups allowed to sign in (from the `groups` claim). |
| `RT_SESSION_DAYS` | `30` | Session lifetime. |
| `RT_LISTEN` | `:8080` | Listen address. |
| `RT_DATA_DIR` | `/data` (image) | Where `rltracker-server.db` lives. |
| `RT_LOG_LEVEL` | `info` | `debug`, `info`, `warn`. |
| `RT_INSECURE_DEV_LOGIN` | — | `1` lets anyone sign in as anyone. **Local tests only.** |
| `RT_DISCORD_WEBHOOK` / `RT_DISCORD_WEBHOOK_FILE` | — | Discord webhook URL (or a file containing it): see [Discord](#discord). |
| `RT_LANG` | `en` | `fr` or `en`: language of the Discord posts. |

## 3. Reverse proxy

The server speaks plain HTTP; put it behind your TLS proxy and serve it on its own (sub)domain at the root path.
Forward the `Host` header as usual. Examples:

**Caddy**
```
rl.example.com {
    reverse_proxy rocket-tracker:8080
}
```

**Traefik** (labels on the service)
```yaml
labels:
  - traefik.enable=true
  - traefik.http.routers.rl.rule=Host(`rl.example.com`)
  - traefik.http.routers.rl.entrypoints=websecure
  - traefik.http.routers.rl.tls.certresolver=letsencrypt
  - traefik.http.services.rl.loadbalancer.server.port=8080
```

Do **not** put Authentik's forward-auth/outpost in front of `/api/agent/`: the agents authenticate with their own
tokens and cannot follow a browser sign-in. (The dashboard itself already requires the OIDC sign-in, so no
forward-auth is needed at all.)

The agents must reach the server: from the gaming PCs, `https://rl.example.com` must resolve and have a certificate
Windows trusts (a public one, or your local CA installed on the PC).

## 4. Players

Each player:

1. signs in at `https://rl.example.com` (their account is created on first sign-in, with a handle taken from
   their Authentik username);
2. opens **Devices**, names the PC, clicks **Create token** and runs the displayed command on that PC:
   `rltracker agent setup --server https://rl.example.com --token rtk_…`;
3. optionally uploads their old local matches: `rltracker agent import`.

**Offline import.** When a gaming PC and the server never run at the same time (same machine, dual boot…), the
agent's matches stay queued on the PC. Copy `%APPDATA%\RocketTracker\rltracker.db` (local mode) and/or the `.json`
files of `%APPDATA%\RocketTracker\outbox\`, then either click **Import** in the dashboard and pick them, or put
them in `data/import/` and run:

```bash
docker compose run --rm rocket-tracker import --user <handle> /data/import/rltracker.db /data/import/outbox
```

Importing the same files again updates the matches instead of duplicating them. Once imported, the `.json` files
can be deleted from the PC's `outbox\` (the agent would otherwise send them again, harmlessly, at its next connection).

A player can register several PCs. The status pill shows whether an agent is online and connected to the game;
the Players page shows who is in a match right now.

### Matches played together

When several players of the server are in the same online match, each agent records it: the matches are linked
by the game's match id. The history shows who else was there, the match page puts everyone's own stats side by
side (movement and boost come from each player's own record), and the dashboard's **Server duos** table gives
the record with each player (win rate together, average score of each) and against them.

### Discord

With a webhook (Discord: channel settings → Integrations → Webhooks → New webhook → Copy URL) in
`RT_DISCORD_WEBHOOK`, the server posts to that channel:

- the highlights of a won match: overtime, hat trick, comeback from 2 goals down or more, every 5 wins in a row;
- a recap when a player's session ends (no match for 30 minutes, 3 matches at least);
- every Monday at 9:00 (server time, set `TZ` in the container), last week's leaderboard (5 decided matches at
  least).

Only online matches sent live by an agent count (not imports, nor a queue sent hours later). A player can opt
out in **Settings**. The last weekly post is remembered in `data/discord.json`.

## Backups and upgrades

- Everything is in `data/rltracker-server.db` (SQLite, WAL). Back up the `data` folder, or online with
  `sqlite3 data/rltracker-server.db ".backup backup.db"`.
- Upgrade: `git pull && docker compose up -d --build`. The schema migrates automatically on start.

## Troubleshooting

| Symptom | Cause |
|---|---|
| "Identity provider unreachable" | The container cannot reach `RT_OIDC_ISSUER` (DNS, TLS with a private CA: mount the CA into the container's `/etc/ssl/certs`). |
| "The ID token is invalid" | Issuer mismatch (copy it exactly, trailing slash included) or no signing key on the Authentik provider (HS256). |
| Redirect URI error on Authentik | `RT_PUBLIC_URL` differs from the URL in the browser, or the redirect URI is not registered. |
| `cross-origin request refused` when saving | `RT_PUBLIC_URL` does not match the URL used in the browser (scheme, host, port). |
| Agent: "device token refused" | The device was revoked: create a new token and run `rltracker agent setup` again. |
| Agent: matches not arriving | `%APPDATA%\RocketTracker\agent.log` on the PC; queued matches wait in `outbox\` (and `outbox\rejected\` if the server refused them). |
