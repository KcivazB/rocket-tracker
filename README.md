# Rocket Tracker

Automatically tracks every Rocket League match you play through the game's **official Stats API**
and shows your progress in a dashboard.

It runs in one of two ways:

| | **Local** | **Self-hosted** |
|---|---|---|
| On the gaming PC | `rltracker` (tracker + dashboard) | `rltracker agent` (tracker only) |
| Dashboard | http://localhost:8765, on that PC | your server, e.g. `https://rl.example.com` |
| Accounts | none, one player | sign-in with OpenID Connect (Authentik, Keycloak…), several players |
| Extras | — | player search, other players' stats, leaderboard, several PCs per player |

## Local mode (one PC)

1. Close Rocket League.
2. Run `dist\rltracker.exe setup`:
   - enables the Stats API in `<RL>\TAGame\Config\DefaultStatsAPI.ini` (`PacketSendRate=30`, asks for admin rights);
   - starts the tracker with Windows;
   - starts the tracker and opens the dashboard.
3. Start Rocket League and play: every match is recorded automatically.

> A game update can reset the ini file: the dashboard then shows a warning; just run `rltracker setup` again.

Data: `%APPDATA%\RocketTracker\` (`rltracker.db` SQLite, `config.json`, `rltracker.log`).

## Self-hosted mode (server + agents)

The server (`rltracker-server`, a Docker image) hosts the dashboard and the accounts. Each player runs the
agent on their gaming PC; it sends the finished matches and the live state to the server.

1. **Server**: deploy it on your homelab and connect it to your identity provider —
   see [docs/SELF_HOSTING.md](docs/SELF_HOSTING.md) (Docker Compose + Authentik step by step).
2. **Each player** signs in to the dashboard, opens **Devices**, creates a token and runs the command it shows
   on their gaming PC:
   ```powershell
   rltracker agent setup --server https://rl.example.com --token rtk_…
   ```
   This enables the Stats API, starts the agent with Windows (replacing the local tracker) and starts it now.
3. Already used the local mode? Send your old matches and hand-entered games to the server:
   ```powershell
   rltracker agent import
   ```
   Running it again does not create duplicates.

When the server is unreachable, the agent keeps finished matches in `%APPDATA%\RocketTracker\outbox\` and
uploads them as soon as it is back. Revoking a device in the dashboard cuts its access immediately; matches already
sent are kept.

## The dashboard

- **Dashboard**: win rate, progression, mental (tilt), mechanics, arenas, teammates…
- **Season goal**: calendar of games per day (adjustable goal, 10 games of 1v1 by default), streaks, projection.
  Click a day to add games played without the tracker.
- **Days & hours**: your best and worst weekday and time slot (by win rate), with a day × hour heat map.
- **History**: every match day by day, with filters and search by player or arena.
- **Match page**: score, scoreboard, goal timeline, comparison with your average, movement and hits,
  tracker.gg links to the players' profiles.
- **Players** (self-hosted): search players by name or in-game name, open anyone's dashboard and history
  (read-only), see who is playing right now, and a **leaderboard** by mode, period and match type, sortable by
  win rate, goal difference, goals, saves, MVP rate…

## Languages

The dashboard is available in **French and English**: it follows the browser language, and can be changed in
**Settings → Language** (remembered by the browser). The sign-in pages of the server follow the browser language;
the messages of `rltracker.exe` (setup windows, import) follow the Windows display language, or `RT_LANG=fr|en`.

Texts live in `web/i18n.js` (dashboard) and `internal/i18n` (command line, sign-in pages): each entry holds the
French and the English version; `go test ./...` checks that none is missing.

## Commands

| Command | What it does |
|---|---|
| `rltracker` / `rltracker run` | tracker + local dashboard (what Windows starts at login) |
| `rltracker setup` | enables the Stats API + autostart of the local tracker |
| `rltracker agent` | tracker sending matches to a server (what Windows starts at login in self-hosted mode) |
| `rltracker agent setup --server URL --token TOKEN` | connects this PC to a server, enables the Stats API, autostarts the agent |
| `rltracker agent import [--db PATH]` | uploads the local database to the server |
| `rltracker open` | opens the dashboard (local, or the server in agent mode) |
| `rltracker uninstall` | removes the autostart (data is kept) |
| `rltracker simulate` | fake game server to test without Rocket League |
| `rltracker-server` | the self-hosted server (configured with `RT_*` environment variables, see the guide) |

CSV export (Excel-friendly `;` separator and decimal commas, or `?sep=,`): **Export CSV** button, or `/api/export.csv`.

## Limits

- The API provides neither the **MMR** nor the **playlist** (ranked/casual): the mode (1v1/2v2/3v3) is inferred
  from the number of players, and the match type is an editable tag (default configurable).
- You are identified automatically (player camera); if needed, set your in-game name in **Settings**.

## Development

```powershell
.\build.ps1 -Test            # vet + tests + build dist\rltracker.exe
.\build.ps1 -Dev             # console build (visible logs)
.\build.ps1 -Server          # also builds dist\rltracker-server (linux/amd64)
docker build -t rocket-tracker-server .
```

Try the server locally without an identity provider (fake sign-in, demo players):

```powershell
go run ./cmd/rltracker-server --public-url http://localhost:8080 --insecure-dev-login --demo --data-dir .\debug\server
```

Technical details: [docs/SPEC.md](docs/SPEC.md), [docs/BACKEND_NOTES.md](docs/BACKEND_NOTES.md),
[docs/SELF_HOSTING.md](docs/SELF_HOSTING.md).
