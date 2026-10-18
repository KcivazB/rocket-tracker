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
  Spectating: when most live frames (≥ 10) carry the spectator fields for both teams' cars (a player only
  gets them for their own team), step (3) is skipped: the camera target is the watched player, not me.
- Clock: regulation length inferred = max TimeSeconds seen before overtime (default 300). Elapsed game seconds
  = regulation − TimeSeconds (regulation) or regulation + TimeSeconds (overtime).
- Partial: tracking started mid-match when the start was not seen (MatchCreated/Initialized, or a countdown or
  kickoff before any clock), or the first clock was overtime or under 300 s. Exception, v0.22: a first clock of
  exactly 180 s at 0–0 after a seen start is an Honor Duel (3:00), a full match; a 5-minute match can only show
  3:00 at a kickoff after a goal. A partial match keeps a 300 s regulation. `regulation_s` (match and `live`) is
  the regulation length of a match seen from its start (300, or 180 for an Honor Duel), absent when partial.
  Duels saved before v0.22 as partial 5-minute matches (times 120 s late) are repaired once at startup and when
  an older agent sends one: partial, complete goal list, first touch between 120 and 130 s, no goal, touch,
  demolition or event before 120 s. Without touch positions (before v0.16) they cannot be recognised.
- Movement stats for me (only when SPECTATOR fields present, clock running = not replay, not paused, after
  countdown/RoundStarted, TimeSeconds changing): accumulate with dt = real time between samples (cap dt 0.5 s):
  avg speed, % supersonic, % on wall (bOnWall) / on ground (bOnGround, also true on walls, so the wall test
  comes first) / airborne (not ground, not wall, has car, not demolished),
  avg boost, % at 0 boost, % at 100 boost, % boosting, % powersliding, seconds demolished.
  Complete-car metrics require all HasCar, Demolished, Speed, Boost, OnGround,
  OnWall, Boosting, Supersonic and Powersliding observations (HasCar true,
  Demolished false). Missing fields do not add zeros or dilute the denominator.
  An observed demolition alone retains sample_s/demolished_s, sets car_sample_s
  to zero and serializes car aggregates as null. Legacy car_sample_s is absent.
  Speed stays raw and carries speed_unit; new unqualified observations are
  `unknown`. Explicit decoder options may declare `uu/s` or `km/h`; magnitude
  never selects the unit. Historical omitted units retain legacy interpretation.
  Unknown units display as raw values, without a claimed km/h conversion.
- Ball hits by me (from BallHit): count, avg & max raw PostHitSpeed (using the record's explicit speed_unit), and each touch
  (`hits.points`): game time, ball Location and Pre/PostHitSpeed.
- Positions (touches, goal `impact` = GoalScored ImpactLocation) are stored **from my team's side**: the blue
  team (0) defends negative Y, so an orange player's positions are turned around (X and Y negated). My goal is
  always at Y = −5120.
- Demolitions involving me (Statfeed `Demolish`, MainTarget = attacker, SecondaryTarget = victim):
  `demolitions` = `[{t, player, mine}]` (`mine`: I demolished `player`).
  Its availability is independent of touch positions: an explicit empty list is
  measured zero; an absent/null list is unknown and excluded from demolition
  averages. Goal impacts and touch positions also use their own availability.
- Shots and saves of both teams (Statfeed `Shot`, `Save`, `EpicSave`; team of the MainTarget player, else its
  TeamNum): `events` = `[{t, kind: shot|save, team: us|them, me}]` in game time. A goal also comes as a `Shot`.
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
- **Trainings**: an offline "match" with a single player (freeplay, training packs, workshop maps) lasting at
  least 1 minute of real time is saved as a training instead (`trainings` table): start, last update, arena,
  touches and their ball speeds, account. A match starting right after it is a new match (it does not adopt
  the training's start). The live status reports `training: true` meanwhile.
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
   "match_count":42,"training_count":3,"shared_rev":17}
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
   "hits":{"count":28,"avg_speed":2100.0,"max_speed":4300.0,
           "points":[{"t":12.5,"x":-1200,"y":-3400,"z":93,"pre":800,"post":2400}]},
   "goals":[{"t":45.2,"team":"us","scorer":"X","assister":"","speed":3120.4,"me_scored":true,"me_assist":false,
             "overtime":false,"impact":{"x":310,"y":5120,"z":180}}],
   "statfeed":{"EpicSave":1,"Demolish":1,"MVP":1},
   "demolitions":[{"t":80.1,"player":"Opp","mine":true}],
   "events":[{"t":44.8,"kind":"shot","team":"us","me":true},{"t":61.0,"kind":"save","team":"them"}]}
  ```
  `hits.points`, `goals[].impact` and `demolitions` are missing on matches recorded before v0.16;
  `events` is missing before v0.21 (missing does not mean no shots).
  `movement` and `hits` are null when no data. Rates are percentages 0–100.
  Speeds retain their declared `speed_unit`; legacy values without a unit and new `unknown` values have no established physical unit.
- `PATCH /api/matches/{id}` body `{"tag":"casual"}` → updated match.
- `GET /api/trainings` → `[{"id":1,"started_at":"…","ended_at":"…","source":"game|timer","focus":"shooting",
  "active_intervals":[{"started_at":"…","ended_at":"…"}],
  "profile":"epic|abc|0","arena":"Park_P","touches":120,"avg_hit_speed":2000,"max_hit_speed":4100}]` (oldest
  first). `POST /api/trainings` (the warm-up timer: `{"started_at","ended_at","active_intervals","focus"}`, source forced to
  `timer`), `PATCH /api/trainings/{id}` `{"focus":"kickoff"}`, `DELETE /api/trainings/{id}`. A training within
  2 minutes of a stored one is merged into it (the timer's and the game's record of one warm-up). Focuses:
  freeplay, kickoff, shooting, saves, aerials, power, boost, dribble, recovery, rotation. Agents send theirs to
  `POST /api/agent/trainings` `{"key","training"}` (queued in the outbox like matches).
  Calendar bounds retain actual start/end chronology at whole UTC second precision:
  every start/end is truncated to the second before union or duration decisions,
  consistently in the browser and Go persistence. The calendar span is at most
  12 hours and a submitted list has at most 4096 active intervals.
  Optional `active_intervals` are bounded `{started_at, ended_at}` timestamps, sorted and unioned to avoid
  double counting overlaps. Absent/null means historical whole-span activity;
  an explicit `[]` means zero activity. Nearby merged records union their active
  intervals, including the full spans of legacy observations, rather than
  filling unobserved gaps. Explicitly different profiles stay separate; merges
  that would exceed the 12-hour calendar bound stay as separate valid records.
- `DELETE /api/matches/{id}` → 204.
- `GET /api/export.csv` → one row per match (flat columns) for Excel/Grafana use.
- `GET /api/config` / `PUT /api/config` → `{"player_names":[],"player_ids":[],"default_tag":"ranked",
  "rl_install_dir":"","dashboard_port":8765,"rl_port":49123,"break_every":0,"break_minutes":5,
  "warmup":{"minutes":15,"blocks":[{"focus":"freeplay","minutes":3}]}}` (excerpt). `break_every` (0–20, 0 = off):
  after every N-th decided match of a session, the tray suggests a `break_minutes` break (alert kind `pause`;
  a tilt alert comes first). `warmup.blocks` empty = the dashboard builds the plan from the stats.

### Agent ingestion and offline imports

`POST /api/agent/matches` requires an authenticated device and exactly one JSON
object, including any trailing whitespace, within 4 MiB.
`POST /api/agent/trainings` applies the same envelope rule within 512 KiB.
Malformed JSON or a second value returns HTTP 400; exceeding the body limit
returns HTTP 413. No match or training is written for a rejected envelope.
Unknown fields remain tolerated for forward compatibility.

Live matches and both database/queue imports use the same quantity validation:
durations, team size, scores, player counters, movement quantities, hit counts
and speeds, goal speeds, event times, regulation length and statfeed counts must be finite
and nonnegative.
Missing historical fields and optional telemetry remain accepted; zero is not
rejected. Coordinates and derived signed goal difference remain signed.
Result and timestamp validation and per-user ingestion/deduplication still apply.
An invalid imported match is reported as skipped; other valid records in the
import can still be stored. This does not attest that an accepted match is real.

## 5. Dashboard (embedded web/, French UI)

Dark, polished, Rocket-League-ish but tasteful; responsive; Chart.js vendored locally (`web/vendor/`),
no runtime CDN dependency. Filters: mode (Tous/1v1/2v2/3v3/autres), tag, période (7j/30j/90j/tout),
online only (default on), exclude abandoned toggle, Honor Duel (exclude by default / include / only; the history
includes them by default). Honor Duels are compared only with duels (match and live averages, live odds) and
carry a Duel badge.
Sections: live match banner (polls /api/status every 3 s) + setup warnings (ini disabled, not connected);
KPIs (matchs, % victoire, V-D, diff de buts moyenne, buts/passes/arrêts/tirs par match, % de tirs cadrés→
conversion goals/shots, taux MVP, série en cours + meilleure série, score moyen);
progression charts (winrate glissant 20 matchs, diff de buts glissante, score moyen glissant, stats/match
glissantes); activité (matchs/jour + winrate/jour); distribution des écarts de buts; analyse « mental »
(winrate selon n° du match dans la session — session = gap > 30 min —, winrate après une défaite vs après une
victoire, winrate par heure de la journée / jour de semaine); situations (premier but marqué vs encaissé,
matchs serrés ≤1 but, prolongations, forfaits, kickoffs — but dans les 3 s suivant l'engagement =
kickoff perdu par l'équipe qui l'encaisse); buts par minute de jeu (marqués vs encaissés); mécanique/
mouvement (vitesse moy, % supersonique, % air, boost moyen, % à 0 boost — tendance); statfeed favoris
(arrêts décisifs, démolitions, buts aériens…); stats par arène; coéquipiers fréquents (winrate avec eux);
tableau des derniers matchs (expandable details, tag editable, delete).

Shared match badges, duos and cached shared match copies use the independent
`status.shared_rev` invalidation marker: another player's contribution can change
them without changing the viewed player's `data_rev`. Failed shared-list reads
retain the last successful display and retry independently at the next successful
status poll. A failed match-detail copy offers an explicit retry action, which
works even when `shared_rev` stays unchanged. Double clicks cannot overlap detail
requests, and late responses cannot repopulate an invalidated cache. A request acknowledges only the revision
seen when it began, and old player generations cannot mutate the new view.
Older servers omitting `shared_rev` revalidate sharing at every successful status
poll. Local dashboards make no shared-data requests.

Progression aids (v0.17): « Ce qui fait gagner vos matchs » — for each per-match stat (boost, speed, air, wall,
powerslide, hit power, touches/shots/demos per 5 min, share of touches in the attacking third, aerial touches),
win rate above vs at-or-below the player's median (≥ 10 matches each side), with the training focus it maps to
and the suggested mechanics (clear factors where most of the last 20 matches are on the losing side, lost
kickoffs, low conversion, conceded goals going in high). v0.23: each mode (Honor Duels apart, a mode needs 6+
values) is split at its own median and the gap is tested within modes (Cochran-Mantel-Haenszel z); a gap is
clear when it passes Benjamini-Hochberg at a 10 % false discovery rate over the factors tested. With several
modes there is no single threshold (« above your median for the mode »). Stats that follow the score rather
than drive it are not tested: saves, defensive touches, demolitions taken, time demolished.
Comparisons across modes use deviations from each mode's mean: the mechanics trend (last 20 vs previous 20) and
the gaps of the stats through a session (the shown averages stay raw).
The comparison cohort is always `(variant, exact mode, duel flag)`, with an omitted
variant interpreted as Soccar. The display filter `other` may show several
variants but never combines their medians or deviation baselines. Constant
within-variant values cannot become a clear factor just because one variant wins
more often than another.
Every temporal inference (first goal, kickoffs, score at half-time, leads lost,
comebacks, late goals, goals per minute and historical live odds) requires a
non-partial match with positive known duration, nonnegative integer final scores,
exactly that many goals, known teams, finite times within the match, and per-team
goal counts equal to the final score. The first goal is derived from those
qualified events, not a cached attribution. Invalid timelines retain their final
result KPIs; history and match details show the recorded events with a warning
and hide the timeline graphic and inferred running score. Goals-per-minute bins
are real 60-second intervals for both 180-second duels and standard matches;
the terminal regulation goal stays in its last minute, with overtime separate.
Ground / wall split: before v0.20.1 the wall time was counted as ground time (`wall_pct` = 0). A movement with
`wall_pct` = 0 over 60 s or more of samples has an unknown split: left out of the split averages (dashboard,
match and live averages) and of the wall factor, and the match page says so; its other movement stats count.
« Sur le terrain » (touch heat map from my side, thirds, aerial and powerful touches, speed added per touch;
3 × 2 zones of the goal mouth for goals scored and conceded; demolitions made / taken, nemesis, win rate when
demolished often); Situations: score at half-time, leads lost, comebacks, last-minute goals; Mental: stats by
match # in the session (1–2, 3–4, 5–6, 7+).
Training page (`#/training`, v0.18): warm-up plan (saved blocks, or built from the suggested mechanics:
freeplay ≈ 20 %, first suggestion 60 % of the rest, second one the remainder) with a timer (per-block countdown,
chime + browser notification at each block, state kept in localStorage, a topbar pill elsewhere; the warm-up is
POSTed as a `timer` training at the end, ≥ 1 active min), the mechanics to target with a drill each, break settings,
the warm-up effect (sessions whose first match starts within 1 h after a training of ≥ 5 active min, vs the others:
win rate of the first 3 matches, whole session, touches / 5 min; compared from 5 sessions each side), and the
trainings log (7 / 30 days, share of sessions warmed up, training share of the time played, per focus; focus
editable, delete). The live banner shows a training in progress.
All training durations, summaries, ratios and warm-up eligibility use the union
of active intervals. Pausing closes an interval; resuming opens one. Block changes
and reload preserve those intervals and any still-open interval. Calendar start
is never shifted forward to disguise pauses. Before a POST or the 60/300-second
thresholds, the browser truncates each outer bound and active-interval endpoint
to the whole UTC second, removes zero-length intervals and unions touching or
overlapping intervals. Adjacent blocks therefore need not remain separate list
entries. The help explains this precision. For example, active intervals
`00:00:00.100–00:01:00.900` and `00:01:01.100–00:05:00.300` contain 300 precise
seconds but become `00:00:00–00:01:00` and `00:01:01–00:05:00`: 299 stored active
seconds, ineligible for the five-minute warm-up threshold both before and after
save. The analogous 60 precise seconds can become 59 and cannot be saved.

Failed timer saves, including HTTP 400, retain a frozen payload in this browser.
Explicit retries reuse exactly its bounds and intervals and cannot run
concurrently. The recovery card offers JSON export of that payload, preserving
the original in browser storage, and a confirmed discard to free the timer for
a new session. Cancelling discard preserves it. Saving successfully also clears
it. Starting another timer cannot overwrite a pending session. Even a session
shorter than one active minute stays available for export or confirmed discard.

Every timer action and tick checks the 12-hour calendar bound (including pauses).
At the limit the session freezes, and a resume/block transition cannot open an
interval beyond the 4096-interval bound. Exactly 12 hours and exactly 4096
intervals remain saveable if activity is at least one minute. A delayed tab or
an older stored session may already exceed a bound: its actual measurements and
calendar envelope are preserved, and no invalid POST is issued. Export and
confirmed discard remain available, including after reload; the browser does
not shorten the envelope, fill pauses, clip intervals or silently drop overrun
time to make the request acceptable. The stopped-state explanation and recovery
actions exist in French and English.
Compatibility assumption: an older browser timer has no trustworthy locations
for previous pauses. On first loading that state, all activity before the reload
is conservatively excluded, including earlier blocks; measurement begins at
reload only if running, or on the next resume if paused. The UI explains this
exclusion. This migration does not reinterpret already stored legacy trainings,
whose absent/null intervals retain whole-span semantics. A new browser submitting
to an older server that ignores `active_intervals` cannot guarantee active-time
semantics; use the matching server version. Warmup person-vs-profile attribution
remains an open policy question; this correction does not choose a new policy.
In-game page (`#/live`, v0.19): the match being played, from `/api/status` `live` (`elapsed_s`, `partial`, every
player's stats in `players[]` and `me`, `me.movement`, `goals[]` with raw team number, scorer, assister, speed,
`events[]` = timed shots and saves `{t, kind, team (raw number), me}`, absent from agents before v0.21).
Opens by itself when a match starts (option kept in localStorage, own pages only), full screen with the button or
F. Shows the score and clock, the scoreboard, the goals, my stats with their pace over regulation duration
(180 s for Honor Duels, 300 s standard, from the first minute) against my last 50
decided matches of the same mode/variant/duel cohort. Both the live counts and
each historical count are normalized to that regulation duration using observed
match duration; the column names the actual minutes. In overtime both columns
use raw counts and say so. Movement uses its own unscaled averages. Also shown: the win rate of my matches of the mode
with the same goal difference (capped at ±3) after the same game time (complete goal lists only; a match
forfeited earlier counts with its frozen score; overtimes: the overtime win rate; shown from 15 matches with its
90 % Wilson interval), the momentum (shots, shots per minute, goals, goals per shot, saves and shots neither
scored nor saved of each team in the last 90 s against the earlier part, shots of the last minute; from 3:00 of
play, a team is flagged as fading when its shots in the window are at most half of those its earlier pace
predicts (2 or more), pushing at twice that and 3 or more, blunted with as many shots but no goal after scoring
before, sharper with 2+ goals at a better conversion; hidden without `events`), and the session going on (record, streak, tilt warning at
`tilt_streak` (0 disables the warning, absent defaults to 3; the factual streak
remains visible), fatigue when my win rate from this match # on is 5+ pts below my usual one, noted rank, daily goal).
Between matches: the last match of the session and the session.

## Measurement, attribution and delivery contracts

- `Match`, `Training` and live observations carry optional `speed_unit` (`unknown`,
  `uu/s`, `km/h`; omitted on legacy data). New raw telemetry is unknown by
  default. An explicitly configured source unit is fixed for the observation
  stream; the implementation does not attest the real game's unit.
- `movement.car_sample_s` is accepted complete-car time. A zero denominator means
  unavailable car metrics, serialized as null; actual measured zero stays zero.
  Other clients must check availability before computing aggregates.
- Negative/nonfinite car sample time and unrecognized unit declarations are
  rejected at ingestion. Existing historical records without these fields remain
  compatible. Goal/hit coordinates and signed deltas retain their signs.
- Percentages shown as match means are not called a time-weighted distribution.
  Air time comparisons are descriptive associations. Historical live odds use
  only eligible scores reached at or before the observed time; final KPIs retain
  their separate population. End-of-regulation goals do not invent a new kickoff.
- Rank summaries and curves group profile and playlist; unspecified legacy
  accounts remain explicit. A rank dialog resolves its destination before both
  prefill and submit, and explicitly labels that fixed account. Changing the
  selected in-game profile keeps the open form's original destination and
  values; closing and reopening selects the new destination. Changing the
  viewed server player/generation or entering a read-only route invalidates
  the old form and prevents its late response from changing the new view.
  Session completion considers all games of the profile before selecting ranked
  playlists. Both minimum-session and minimum-match sample thresholds are shown.
- A device authenticates the sender's server account. The in-game profile remains
  declared/detected, and accepted telemetry is not an attested official match.
  Existing authenticated social visibility is unchanged; platform linking,
  consent changes and verification badges are not implemented implicitly.
- Local result/training plus delivery intent are atomic (schema v8, persistent record origins in v9). Server
  ingestion is scoped and never queues a reply. See ARCHITECTURE.md and
  ADR-0002 for verified destinations, revision acknowledgements, migration and
  explicit recovery; dashboard edits do not promise bidirectional replication.

Training POST bodies (dashboard and device) are bounded at 512 KiB, including
trailing whitespace, and require exactly one JSON value before any write. This
accommodates the 4096 active-interval domain ceiling without filling pauses or
dropping intervals. CSV syntax quoting and formula neutralization are separate:
every textual cell, including imported mode/variant/first-goal values, escapes
a leading `=`, `+`, `-`, `@`, tab, CR or LF; signed numeric cells remain numeric.
