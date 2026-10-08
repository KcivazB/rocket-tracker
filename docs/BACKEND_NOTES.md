# Rocket Tracker — backend notes (draft README section)

## Build

```powershell
.\build.ps1            # dist\rltracker.exe   (-H windowsgui -s -w, no console window)
.\build.ps1 -Dev       # dist\rltracker-dev.exe (console build, handy for debugging)
.\build.ps1 -Test      # go vet + go test, then build
```

Pure Go (no cgo): SQLite via `modernc.org/sqlite`, Win32 via `golang.org/x/sys/windows`.
The dashboard (`web/`) is embedded with `//go:embed *` (`web/embed.go`).

## Commands

| Command | What it does |
|---|---|
| `rltracker` / `rltracker run` | Tracker + dashboard on `http://localhost:8765`. Single instance per data dir (named mutex). Logs to file. |
| `rltracker setup` | Finds Rocket League, enables the Stats API in the ini (`PacketSendRate=30`, `Port=49123`, `WebPort=49124`), asks for UAC elevation if Program Files is not writable, registers autostart (`HKCU\Software\Microsoft\Windows\CurrentVersion\Run\RocketTracker = "<exe>" run`), starts the tracker and opens the dashboard. Flags: `--rl-dir`, `--rate`, `--no-autostart`, `--no-open`. **Restart the game afterwards.** |
| `rltracker uninstall` | Removes the autostart value (data is kept). |
| `rltracker open` | Opens the dashboard in the default browser. |
| `rltracker simulate [--port 49123] [--mode ws\|tcp] [--matches N] [--speed X] [--kinds normal,forfeit,abandoned,offline] [--seed S]` | Fake Stats API server for end-to-end testing without the game. |
| `rltracker seed --db <path> [--n 300] [--days 90]` | Inserts N realistic fake matches into the given DB (refuses to run without `--db`). |
| `rltracker version` | Prints the version. |

Global flags: `--data-dir DIR`, `--port 8765` (dashboard), `--rl-port 49123`, `--debug`.

With the `windowsgui` build, commands launched from a terminal attach to the parent console
(`AttachConsole(ATTACH_PARENT_PROCESS)`) so `setup`/`simulate`/`seed` output is visible; redirections
(`> out.txt`) also work. When started without a console (double-click), `setup`/`uninstall` show a MessageBox
summary and errors are shown in a MessageBox.

## Data directory

`%APPDATA%\RocketTracker\` (override with `--data-dir`):

- `rltracker.db` — SQLite (WAL). One row per match: indexed columns + the full match JSON (`data`). Schema
  versioned with `PRAGMA user_version`.
- `config.json` — `player_names`, `player_ids`, `default_tag`, `rl_install_dir`, `dashboard_port`, `rl_port`.
- `rltracker.log` — rotated at 5 MB (`.1`, `.2` kept).

## How detection works

1. **Install / ini**: probes `rl_install_dir`, the Epic launcher manifests
   (`%ProgramData%\Epic\EpicGamesLauncher\Data\Manifests\*.item`), then the usual Epic/Steam paths on drives C–G.
   Reads `TAGame\Config\TAStatsAPI.ini` (effective if present) and `DefaultStatsAPI.ini`; `PacketSendRate` is
   accepted under any section. `setup` writes `DefaultStatsAPI.ini` (created if missing) and patches
   `TAStatsAPI.ini` only if it exists, preserving all other lines, comments, BOM and line endings.
   `/api/status.ini` reports `found` / `packet_send_rate` / `ok`.
2. **Connection**: every 3 s, connect to `Port` (then `WebPort` if `Port` is refused). On connect, send a
   WebSocket upgrade request: `HTTP/1.1 101` → WebSocket mode (masked client frames, ping→pong, fragmentation);
   first byte `{`, non-HTTP data, or silence for 2 s → raw TCP mode (stream of concatenated JSON objects).
   If the server hangs up on the upgrade request, the next attempt connects in raw mode without a handshake.
   "Connection refused" (game not running) is logged once, then only at debug level.
3. **Decoding**: a brace-aware object splitter extracts each JSON object from the stream (robust to
   missing separators, garbage, and partial reads); `Data` may be an object or a JSON-encoded string. All
   numeric/bool fields are lenient (int, float, numeric string, null).
4. **Match tracking** (`internal/tracker`, pure logic with injected clock):
   - start on `MatchCreated` / `MatchInitialized` / first `UpdateState` with a new guid (empty guid → offline,
     `local-<ms>`; a guid appearing later is adopted);
   - **me** = config `player_ids` → config `player_names` (case-insensitive) → most frequent `Game.Target`
     (non-replay frames). An auto-detected identity on an online match is saved to `config.json` when the
     config has none;
   - clock: regulation = max `TimeSeconds` before overtime (default 300); elapsed = reg − t (or reg + t in OT);
   - movement stats are integrated over real time (dt capped at 0.5 s) only while the clock runs (not replay,
     not paused, between `RoundStarted` and the next goal/countdown, `TimeSeconds` changed within 1.5 s) and
     only for players whose spectator fields are present;
   - end: `MatchEnded` → win/loss from `WinnerTeamNum`, `forfeit` if regulation time remained;
     `MatchDestroyed`, a new match, or a disconnect (game closed) → `abandoned` (saved only if ≥ 30 s of game time);
   - final scores = max(last `UpdateState`, count of `GoalScored`) so an overtime winner immediately followed by
     `MatchEnded` is not lost;
   - skipped: no players, me not found (spectating), offline with one player (freeplay/training), or a match made
     of >90 % replay frames (watching a replay file).

## Known limits

- The Stats API exposes **no MMR / rank and no playlist** → `tag` (ranked/casual/tournament/private/other) is
  `default_tag` at save time and editable from the dashboard. Rumble / Heatseeker / Snow Day are reported as
  `Soccar` (only Hoops and Dropshot are inferred from the arena name).
- Movement stats require the spectator fields, which the game only sends for the local player's team; they
  depend on `PacketSendRate` (higher = more precise).
- Exact field names of `BallHit` and some events are best effort; unknown/missing fields degrade gracefully
  (no hits stats rather than a crash).
- Joining a match already in progress (tracker started mid-match) can under-estimate the regulation length.
- Identity auto-detection needs at least one online match with the camera on yourself; set `player_names` /
  `player_ids` in `config.json` (or via `PUT /api/config`) to be explicit (needed for split-screen).
