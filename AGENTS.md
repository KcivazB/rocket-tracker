# Agent guide

This is the shared repository guide for coding agents. `CLAUDE.md` imports this
file; keep project instructions here rather than duplicating them per tool.

## Start here

1. Read [README.md](README.md) for the supported user workflows.
2. Read [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for runtime ownership and
   domain vocabulary, then [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) for commands
   and platform constraints.
3. For the affected surface, consult [docs/SPEC.md](docs/SPEC.md) for payloads
   and tracking rules, or [docs/SELF_HOSTING.md](docs/SELF_HOSTING.md) for server
   configuration and operations. Verify examples against the owning source.
4. Inspect the working tree, callers and relevant tests before editing. Preserve
   unrelated changes. State consequential assumptions and compatibility effects.

## Find the owning code

| Change | Start with |
|---|---|
| Desktop startup, lifecycle, autostart | `internal/desktopapp/`, `main.go`, `internal/winutil/`, `internal/tray/` |
| Installation wizard and server connection | `internal/desktopapp/wizard_windows.go`, `internal/server/setup.go`, `web/setup.js` |
| Game transport and tolerant decoding | `internal/statsapi/` |
| Shared match types, normalization and profile identity | `internal/domain/`; compatibility aliases in `internal/store/model.go` |
| Match lifecycle, player detection, movement, training detection | `internal/tracker/` |
| Break and tilt rules using match history | `internal/tilt/` |
| SQLite schema, ingestion, profiles, imports and aggregates | `internal/store/` |
| Upload queue, heartbeat, device configuration | `internal/agent/`, `internal/desktopapp/agent_cmd_windows.go` |
| HTTP, user scope, OIDC, devices, Discord, file imports | `internal/server/`, `cmd/rltracker-server/` |
| Analytics, browser state and rendering | `web/app.js`, `web/index.html`, `web/style.css` |
| Translations | `web/i18n.js`, `internal/i18n/`, `web/tests/i18n.test.js` and `web/tests/analytics.test.js` |
| Artifact version, updater and publication | `internal/buildinfo/`, both `main` packages, `internal/update/`, `build.ps1`, `Dockerfile`, `.github/workflows/` |

## Change rules

- Keep the existing Go module, SQLite store and embedded web assets. Introduce
  a dependency or architectural layer only to solve a demonstrated need.
- Keep pure match/training values, observations and availability rules in
  `internal/domain`; the tracker consumes observations and injected saves.
  Stats API decoding belongs to its adapter. Application recording and ingestion
  use consumer-owned ports; composition belongs to `internal/desktopapp` and the
  server command. Preserve store aliases for compatibility. Domain and tracker
  import tests enforce their infrastructure boundaries.
- A server user, a Rocket League profile and a device are different identities.
  Review the relevant user/profile scope on every read, write and deduplication
  change. Do not reduce a match perspective to its GUID without considering
  its player, offline identity and reconnection behavior.
- Preserve tolerant Stats API decoding, partial-match handling and missing
  historical fields. Missing telemetry is not a measured zero.
- Treat replay, import order, concurrent ingestion and `abandoned` to final-result
  replacement as explicit test scenarios when changing persistence. Keep
  user-edited tags and training focus in mind. Do not assume existing tests
  establish all of these guarantees.
- A transport key is not a match identity. Local result/training and delivery
  intent commit together in SQLite. Destination includes normalized server URL,
  installation ID and authenticated account ID. A token is not account identity.
  Acknowledge only the loaded revision; preserve held/rejected/legacy records.
  Never attach unknown legacy ownership from today's credentials. See ADR-0002
  and the operations runbook for recovery and downgrade constraints.
- Add schema changes as new ordered migrations in `internal/store/store.go`;
  do not rewrite migrations already applied to users' databases. Test upgrades
  from the relevant older schema using synthetic fixtures.
- Reuse the `module.exports` analytics interface in `web/app.js` for pure Node
  checks. Keep DOM and network effects out of new analytical functions. Preserve
  existing generation checks when changing the server player being viewed;
  test routes and in-game profile selection separately for stale responses.
- Add French and English text together in the existing translation modules.
- Keep the single version variable in `internal/buildinfo`: its default is
  `dev`. Inject numbered versions with
  `-ldflags '-X rocket-tracker/internal/buildinfo.Version=1.2.3'`.
  Plain builds, `build.ps1` without `-Version` and Docker builds without a
  `VERSION` argument remain `dev`; release and tagged Docker builds inject the
  tag without its `v`. Do not bump source literals or require version suffixes
  in commit titles. See [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) for the flag
  migration and development-build updater behavior.

## Verification and delivery

- Follow [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md). Run the smallest relevant
  checks during iteration, then the applicable platform checks before delivery.
  Add focused regression coverage for behavior changes; documentation-only
  changes need checked links, commands and a clean diff, not invented code tests.
- The desktop root package requires Windows. A cross-build is not execution of
  Windows tests. The Go wrapper fails if Node is missing: run
  `node web/tests/i18n.test.js`, `node web/tests/analytics.test.js`,
  `node web/tests/profiles.test.js` and `node web/tests/polling.test.js`
  explicitly when changing browser text or analytics.
  The analytics script owns analytical assertions; add focused coverage for changed
  analytical rules rather than relying only on translation checks.
- Report exact commands, results and untested environments. Do not describe a
  green package test as proof of a real game, IdP, Docker or deployment scenario.
- Update the affected current documentation with behavior changes. Keep
  investigation logs and proposed redesigns out of these instruction files.
- Do not commit real databases, logs, `config.json`, `agent.json`, outboxes,
  tokens or OIDC/Discord secrets. Use temporary data directories and synthetic
  players. Never start a development server against a player's existing data.
- Bind fake-login development servers to loopback; never expose
  `--insecure-dev-login` on a shared or public interface.
- Preserve user scoping, origin checks, token hashing, OIDC validation, import
  bounds and CSV escaping. Do not treat a loopback bind as CSRF protection.
- Do not merge, release, deploy or publish artifacts without explicit user
  authorization. Building and testing do not authorize those actions.
