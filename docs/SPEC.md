# Rocket Tracker — Spec

Single Windows executable `rltracker.exe` (Go 1.26, no cgo) that:
1. Runs in background (auto-start at Windows login), waits for Rocket League, connects to the
   **official Rocket League Stats API** (local socket, default `localhost:49123`).
2. Records every match the user plays into SQLite.
3. Serves an embedded dashboard at `http://localhost:8765`.

## 1. Rocket League Stats API (official, Psyonix)

- Enabled via `<Install Dir>\TAGame\Config\TAStatsAPI.ini` (or `DefaultStatsAPI.ini` if the former doesn't
  exist — handle both: read both, write `DefaultStatsAPI.ini` AND `TAStatsAPI.ini` if it exists),
  section `[TAGame.MatchStatsExporter_TA]`:
  `PacketSendRate` (float, default 0 = disabled, must be > 0, max 120), `Port` (default 49123, raw TCP likely)
  and `WebPort` (default 49124, likely the WebSocket variant). Client connects to `Port` with auto-detection;
  if it fails repeatedly but `WebPort` accepts, use WebPort (WebSocket).
  Changes need a game restart. Install dir on this machine: `C:\Program Files\Epic Games\rocketleague`
  (also probe Steam paths and `D:\...` variants; allow override in config). File is in Program Files → writing
  it needs elevation. The file may not exist yet: create it.
  If the section name is uncertain, write it as:
  ```ini
  [TAGame.MatchStatsExporter_TA]
  PacketSendRate=30
  Port=49123
  WebPort=49124
  ```
  Preserve other existing keys/sections when editing.
  When *reading*, accept `PacketSendRate=` under any section.
- Transport: docs say "web socket", other sources say plain TCP. **Client must auto-detect**: open TCP, send a
  WebSocket upgrade request; if the reply starts with `HTTP/1.1 101` → WebSocket mode (server frames unmasked,
  client frames masked, handle text/continuation/ping→pong/close); if the first non-space byte received is `{`
  (or the server closes / answers non-HTTP) → raw TCP mode where the stream is concatenated JSON objects
  (decode with `json.Decoder`, keep the already-read bytes). Retry connect every 3 s forever (game not running
  = connection refused, that's normal, log quietly).
- Envelope: `{"Event": "<Name>", "Data": {...}}`. **Data may be an object OR a JSON-encoded string** — handle both.
- Events: UpdateState, BallHit, ClockUpdatedSeconds, CountdownBegin, CrossbarHit, GoalReplayEnd,
  GoalReplayStart, GoalReplayWillEnd, GoalScored, MatchCreated, MatchInitialized, MatchDestroyed, MatchEnded,
  MatchPaused, MatchUnpaused, PodiumStart, ReplayCreated, RoundStarted, StatfeedEvent.
- UpdateState Data:
  ```
  MatchGuid string
  Players[]: Name, PrimaryId ("Platform|Uid|Splitscreen"), Shortcut int, TeamNum int (0 blue, 1 orange),
             Score, Goals, Shots, Assists, Saves, Touches, CarTouches, Demos (int)
             -- only for own team / spectator (may be ABSENT): bHasCar, Speed (uu/s), Boost (0-100),
                bBoosting, bOnGround, bOnWall, bPowersliding, bDemolished, bSupersonic
             -- conditional: Attacker {Name, Shortcut, TeamNum}
  Game: Teams[] {Name, TeamNum, Score, ColorPrimary, ColorSecondary}, TimeSeconds int, bOvertime bool,
        Frame int?, Elapsed float?, Ball {Speed, TeamNum}, bReplay, bHasWinner, Winner string, Arena string,
        bHasTarget bool, Target {Name, Shortcut, TeamNum}?
  ```
- MatchCreated / MatchInitialized / MatchDestroyed: `{MatchGuid}` (MatchGuid empty for offline matches).
- ClockUpdatedSeconds: `{TimeSeconds, bOvertime, MatchGuid}`
- MatchEnded: `{WinnerTeamNum, MatchGuid}`
- GoalScored: `{GoalSpeed, GoalTime, ImpactLocation{X,Y,Z}, Scorer{Name,Shortcut,TeamNum}, Assister?{...},
  BallLastTouch{Player{...}, Speed}, MatchGuid}`
- StatfeedEvent: `{EventName, Type, MainTarget{Name,Shortcut,TeamNum}, SecondaryTarget?{...}, MatchGuid}`
  (EventName examples: Goal, Assist, Save, EpicSave, Shot, Demolish, AerialGoal, BackwardsGoal, BicycleGoal,
  LongGoal, TurtleGoal, PoolShot, HatTrick, Playmaker, Savior, OvertimeGoal, MVP, Win, ...). Store raw names.
- BallHit (best effort, fields may differ): `{MatchGuid, Players[{Name,Shortcut,TeamNum}], Ball{PreHitSpeed,
  PostHitSpeed, Location{X,Y,Z}}}`.
- Every field must be decoded leniently (missing fields → zero values / nil pointers, unknown fields ignored,
  numbers may be float or int). Never crash on malformed input; log and continue.

## 2. Match tracking rules

- Match starts on MatchCreated/MatchInitialized/first UpdateState with a new MatchGuid (offline: empty guid →
  generate `local-<timestamp>`; mark `online=false`).
- Keep latest snapshot of players/teams/clock from UpdateState.
- **Who am I**: precedence (1) config `player_ids` (PrimaryId match), (2) config `player_names`
  (case-insensitive), (3) auto: count `Game.Target` (when bHasTarget && !bReplay) resolved to a player via
  Name+Shortcut+TeamNum; most frequent target = me. When auto-detected on an online match and config has no
  identity, persist that PrimaryId + name to config.
- Clock: regulation length inferred = max TimeSeconds seen before overtime (default 300). Elapsed game seconds
  = regulation − TimeSeconds (regulation) or regulation + TimeSeconds (overtime).
- Movement stats for me (only when SPECTATOR fields present, clock running = not replay, not paused, after
  countdown/RoundStarted, TimeSeconds changing): accumulate with dt = real time between samples (cap dt 0.5 s):
  avg speed, % supersonic, % on ground / on wall / airborne (not ground, not wall, has car, not demolished),
  avg boost, % at 0 boost, % at 100 boost, % boosting, % powersliding, seconds demolished.
  Speed is in uu/s; convert to km/h for display in the frontend (×0.036).
- Ball hits by me (from BallHit): count, avg & max PostHitSpeed (km/h conversion in frontend).
- Goals: store each GoalScored with game time (elapsed seconds), team us/them, scorer, assister, speed,
  is_me_scorer/is_me_assist, overtime flag.
- Statfeed: count EventName occurrences where MainTarget is me → map. MVP = statfeed "MVP" for me, else
  computed at end: my score is max of winning team and my team won.
- End:
  - MatchEnded → result win/loss from WinnerTeamNum vs my team. `forfeit=true` if regulation time left > 0 and
    not overtime.
  - MatchDestroyed (or new match / disconnect) without MatchEnded → result `abandoned` (still saved if >= 30 s of
    game time was played, else dropped).
  - Final stats = last snapshot before end (score/goals/... from Players).
- Skip saving: no players, me not found (spectating), or offline match with only 1 player (freeplay).
- Mode: team_size = max players per team → "1v1".."4v4"; arena name containing "Hoops" → "Hoops",
  "ShatterShot" → "Dropshot", else "Soccar". `mode` string e.g. "2v2" and `variant` "Soccar".
- `tag` (ranked/casual/tournament/private/other): not provided by the API → config `default_tag`
  (default "ranked") applied at save, editable from dashboard.
- Data dir: `%APPDATA%\RocketTracker\` → `rltracker.db`, `config.json`, `rltracker.log` (rotate at 5 MB).

## 3. Commands (`rltracker.exe <cmd>`)

- *(no args)* / `run`: tracker + dashboard server. If built with `-H windowsgui` there's no console; log to file.
  Single-instance guard (named mutex or lock on port 8765). Opens nothing by default.
- `setup`: find RL install, enable Stats API in ini (PacketSendRate=30, Port=49123), self-elevate via
  ShellExecute "runas" if access denied; register autostart (HKCU\...\Run "RocketTracker" = "<exe> run");
  print/open dashboard URL.
- `uninstall`: remove autostart key.
- `open`: open dashboard in default browser.
- `simulate [--port 49123] [--mode ws|tcp] [--matches N] [--speed X]`: fake Stats API server emitting realistic
  matches (random modes 1v1/2v2/3v3, goals, statfeed, overtime sometimes, a forfeit sometimes, an abandoned one
  sometimes) — used for testing end to end without the game.
- `seed --n 300`: insert N realistic fake matches spread over the last 90 days into a DB given by `--db path`
  (for dashboard development; never into the real DB unless explicitly targeted).
- Global flags: `--data-dir`, `--port 8765`, `--rl-port 49123`.

## 4. HTTP API (contract between backend and frontend)

Server on `127.0.0.1:8765`. Static files embedded from `web/` (package `web`, `//go:embed`).
All analytics are computed **client-side** from the raw match list.

- `GET /api/status` →
  ```json
  {"version":"0.1.0","connected":true,"transport":"ws|tcp|","in_match":true,
   "live":{"mode":"2v2","arena":"Stadium_P","time_seconds":123,"overtime":false,"team_score":1,"opp_score":0,
           "my_team":0,"me":{"name":"X","score":120,"goals":1,"shots":2,"assists":0,"saves":1}} ,
   "ini":{"path":"C:\\...\\DefaultStatsAPI.ini","found":true,"packet_send_rate":30,"ok":true},
   "identity":{"names":["X"],"ids":["Epic|abc|0"]},
   "match_count":42}
  ```
  `live` is null when not in a match.
- `GET /api/matches` → array (oldest → newest) of:
  ```json
  {"id":1,"guid":"...","online":true,"started_at":"2026-10-08T20:01:02Z","ended_at":"...",
   "duration_s":312,"overtime":true,"overtime_s":12,"arena":"Stadium_P","mode":"2v2","team_size":2,
   "variant":"Soccar","tag":"ranked","result":"win","forfeit":false,
   "my_team":0,"team_score":3,"opp_score":2,"goal_diff":1,"first_goal":"us|them|none","mvp":true,
   "me":{"name":"X","primary_id":"Epic|abc|0","score":512,"goals":2,"shots":4,"assists":1,"saves":2,
         "touches":31,"demos":1},
   "players":[{"name":"X","primary_id":"...","team":0,"score":512,"goals":2,"shots":4,"assists":1,"saves":2,
               "touches":31,"demos":1,"is_me":true}],
   "movement":{"sample_s":290.5,"avg_speed":1234.5,"supersonic_pct":12.3,"ground_pct":60.1,"wall_pct":8.2,
               "air_pct":31.7,"avg_boost":41.2,"zero_boost_pct":9.5,"full_boost_pct":7.0,"boosting_pct":18.0,
               "powerslide_pct":3.1,"demolished_s":6.0},
   "hits":{"count":28,"avg_speed":2100.0,"max_speed":4300.0},
   "goals":[{"t":45.2,"team":"us","scorer":"X","assister":"","speed":3120.4,"me_scored":true,"me_assist":false,
             "overtime":false}],
   "statfeed":{"EpicSave":1,"Demolish":1,"MVP":1}}
  ```
  `movement` and `hits` are null when no data. Rates are percentages 0–100. Speeds in uu/s.
- `PATCH /api/matches/{id}` body `{"tag":"casual"}` → updated match.
- `DELETE /api/matches/{id}` → 204.
- `GET /api/export.csv` → one row per match (flat columns) for Excel/Grafana use.
- `GET /api/config` / `PUT /api/config` → `{"player_names":[],"player_ids":[],"default_tag":"ranked",
  "rl_install_dir":"","dashboard_port":8765,"rl_port":49123}`.

## 5. Dashboard (embedded web/, French UI)

Dark, polished, Rocket-League-ish but tasteful; responsive; Chart.js vendored locally (`web/vendor/`),
no runtime CDN dependency. Filters: mode (Tous/1v1/2v2/3v3/autres), tag, période (7j/30j/90j/tout),
online only (default on), exclude abandoned toggle.
Sections: live match banner (polls /api/status every 3 s) + setup warnings (ini disabled, not connected);
KPIs (matchs, % victoire, V-D, diff de buts moyenne, buts/passes/arrêts/tirs par match, % de tirs cadrés→
conversion goals/shots, taux MVP, série en cours + meilleure série, score moyen);
progression charts (winrate glissant 20 matchs, diff de buts glissante, score moyen glissant, stats/match
glissantes); activité (matchs/jour + winrate/jour); distribution des écarts de buts; analyse « mental »
(winrate selon n° du match dans la session — session = gap > 30 min —, winrate après une défaite vs après une
victoire, winrate par heure de la journée / jour de semaine); situations (premier but marqué vs encaissé,
matchs serrés ≤1 but, prolongations, forfaits); buts par minute de jeu (marqués vs encaissés); mécanique/
mouvement (vitesse moy, % supersonique, % air, boost moyen, % à 0 boost — tendance); statfeed favoris
(arrêts décisifs, démolitions, buts aériens…); stats par arène; coéquipiers fréquents (winrate avec eux);
tableau des derniers matchs (expandable details, tag editable, delete).
