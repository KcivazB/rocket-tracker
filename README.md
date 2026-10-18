# Rocket Tracker

Automatically tracks every Rocket League match you play through the game's **official Stats API**
and shows your progress in a dashboard.

It runs in one of two ways:

| | **Local** | **Self-hosted** |
|---|---|---|
| On the gaming PC | `rltracker` (tracker + dashboard) | `rltracker` (local copy + server uploads) |
| Dashboard | http://localhost:8765, on that PC | your server, e.g. `https://rl.example.com` |
| Accounts | none, one player | sign-in with OpenID Connect (Authentik, Keycloak…), several players |
| Extras | — | player search, other players' stats, leaderboard, several PCs per player |

## Installation

Download `rltracker.exe` from the [releases](https://github.com/KcivazB/rocket-tracker/releases) and double-click it.
The first time, the **installation wizard** opens in the browser:

1. **Stats API**: finds Rocket League and enables its Stats API in `<RL>\TAGame\Config\DefaultStatsAPI.ini`
   (`PacketSendRate=30`; Windows may ask for admin rights). Restart the game if it is running.
2. **Where your matches go**: this PC only, or your Rocket Tracker server (address + device token created in
   **Devices** on the server).
3. **Startup**: start Rocket Tracker with Windows; then the dashboard opens.

After that, launching `rltracker.exe` opens the dashboard: **the server's when it answers, else the local one**.
Every match is saved on the PC too, so while the server is down the local dashboard keeps working, and the
matches are sent as soon as the server is back. The wizard stays at `http://localhost:8765/setup.html`.

> A game update can reset the ini file: the dashboard then shows a warning with a link to the wizard.

## Local mode (one PC)

Choose **This PC only** in the wizard (or run `rltracker setup`), start Rocket League and play: every match is
recorded automatically.

Data: `%APPDATA%\RocketTracker\` (`rltracker.db` SQLite, `config.json`, `rltracker.log`).

The tracker (and the agent) shows an icon in the notification area, next to the clock: open the dashboard, quit,
and **Install the vX update** when a newer release is published on GitHub (checked at start, then every 12 hours;
the local dashboard shows it too, with an **Install and restart** button). The update downloads the new
`rltracker.exe`, checks it, replaces the running one and restarts; if it fails, the release page opens instead.

**Several Rocket League accounts.** Each account you play on is a profile (found from the matches, by
platform id). With two or more, a selector at the top of the dashboard picks the account shown, or **All my
accounts**: every stat, the history, the ranks and the sessions follow it. The default is the account of
**Settings → Platform id(s)**; games entered by hand count with it. The tilt alert and the Discord posts use
the history of the account just played, and the server leaderboards only count the accounts listed in your
settings (all of them when none is).

**Rank & MMR.** The Stats API gives neither, so you note them: **Note my rank** on the dashboard (rank with
tier and division, MMR, or both, per playlist). The dashboard shows your current rank per playlist with its
badge, the MMR change, and the MMR curve. When a session ends (30 minutes after the last match), the tray icon
reminds you and the dashboard offers to note the playlists you just played, prefilled with your last entry.
The badges are drawn by Rocket Tracker, not Psyonix's emblems.

**Met before.** During a match, the live banner lists the players you already faced or played with, with
your record against / alongside them (online matches, matched by platform id; bots are ignored).

**Tilt alert.** After a match, a Windows notification suggests a break when you lose several matches in a row
in a session (3 by default, set in **Settings**, or off), quoting how you usually do after such a streak. It also
fires once per session when you reach the number of matches past which your win rate usually drops. In
self-hosted mode the server computes it from your history and the agent shows it.

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
   This enables the Stats API, starts the tracker with Windows and connects it to the server (the installation
   wizard does the same: choose **My Rocket Tracker server** and paste the token).
3. Already used the local mode? Send your old matches and hand-entered games to the server:
   ```powershell
   rltracker agent import
   ```
   Running it again does not create duplicates.

Finished matches and trainings are saved locally. With a configured server, the
same SQLite transaction records a delivery intention; the agent retries when the
verified destination is reachable. Legacy `outbox\` files are retained separately.
For disconnected transfer, import `%APPDATA%\RocketTracker\rltracker.db` or
legacy queue JSON through the dashboard or `rltracker-server import`. Import
copies supported records; it does not restore a complete installation. Follow
[the backup and recovery guide](docs/OPERATIONS.md) to preserve the whole stopped
data directory, configuration, credentials and delivery state. Revoking a device
cuts its access immediately; matches already sent are kept.

## The dashboard

- **Dashboard**: win rate, progression, mental (tilt), mechanics, arenas, teammates…
- **What wins your matches**: the stats that clearly separate your wins from your losses (time at 0 boost,
  touches in the attacking third, hit power…), with your win rate on each side of your usual value, and the
  mechanics to train next. Each mode is compared with itself, and stats that only follow the score (saves,
  demolitions taken) are left out.
- **On the field** (matches recorded from v0.16): where you touch the ball, where goals go in (scored and
  conceded), demolitions made and taken; plus the score at half-time and your stats through a session.
- **Season goal**: calendar of games per day (adjustable goal, 10 games of 1v1 by default), streaks, projection.
  Click a day to add games played without the tracker.
- **Training**: a warm-up plan built from your stats (car control, then the mechanics to target, each with a
  drill) and a timer that chimes at each block; breaks every N matches of a session (a Windows notification,
  Pomodoro-like); whether warming up helps (your first matches of a session with and without a warm-up); and the
  trainings log. Freeplay, training packs and workshop maps (alone, offline, 1 min or more) are recorded
  automatically, and so is the timer's warm-up.
- **In game**: while you play, a page with the match only (opens by itself when a match starts, full screen
  with F): big score and clock, scoreboard with every player's stats, goal timeline, your stats so far with their
  pace over a full match against your average, how your matches of the mode went from the same score at the same
  time, the momentum (each team's shots, goals per shot and saves over the last 90 s against the rest of the
  match, with fading or pushing flagged), the session going on (record, streak, tilt and fatigue warnings, noted rank, daily goal) and the players
  already met. Between two matches it shows the last one.
- **Days & hours**: your best and worst weekday and time slot (by win rate), with a day × hour heat map.
- **History**: every match day by day, with filters and search by player or arena.
- **Honor Duels** (3-minute matches) carry a Duel badge and have their own filter: left out of the statistics by
  default, shown alone, or included. They are compared only with other duels.
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
French and the English version. `go test ./...` checks that none is missing and requires Node.js for the web checks.
Run `node web/tests/i18n.test.js` and `node web/tests/analytics.test.js` directly for focused checks; CI uses Node 22.

## Commands

| Command | What it does |
|---|---|
| `rltracker` | starts the tracker, then opens the installation wizard (first launch) or the dashboard |
| `rltracker run` | tracker + local dashboard, also sending the matches to the server when one is set (what Windows starts at login) |
| `rltracker setup` | enables the Stats API + autostart of the local tracker |
| `rltracker agent` | same as `rltracker run` (kept for older autostart entries) |
| `rltracker agent setup --server URL --token TOKEN` | connects this PC to a server, enables the Stats API, autostarts the agent |
| `rltracker agent import [--db PATH]` | uploads the local database to the server |
| `rltracker open` | opens the dashboard (the server when it answers, else the local one) |
| `rltracker uninstall` | removes the autostart (data is kept) |
| `rltracker simulate` | fake game server to test without Rocket League |
| `rltracker-server import --user HANDLE PATH...` | imports files copied from a gaming PC (`rltracker.db`, the `outbox` folder or its `.json` files) |
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
go run ./cmd/rltracker-server --listen 127.0.0.1:8080 --public-url http://127.0.0.1:8080 --insecure-dev-login --demo --data-dir .\debug\server
```

Contributor and agent documentation: [architecture and domain map](docs/ARCHITECTURE.md),
[development and verification](docs/DEVELOPMENT.md), [AGENTS.md](AGENTS.md).
Operations and release: [backup/recovery](docs/OPERATIONS.md), [artifact publication](docs/RELEASING.md).
Detailed references: [specification](docs/SPEC.md) and [self-hosting](docs/SELF_HOSTING.md).

## Implementation plan

See the [audit implementation plan](docs/implementation/README.md) for proposed personas, epics, user stories, architecture migration and the documentation and acceptance contract for each step. The implementation folder is temporary; durable choices and their status live in [architecture decision records](docs/decisions/README.md). Current runtime behavior is described in [the architecture guide](docs/ARCHITECTURE.md).
