# Development and verification

Use the Go version declared in `go.mod` (currently Go 1.26.9 or newer).
Desktop builds target Windows/amd64 with `CGO_ENABLED=0`; the server also builds
on other Go-supported platforms. Browser assets are embedded directly, without
a JavaScript build step. Node.js runs the translation and pure-analytics checks; CI uses Node 22.

## Windows desktop

Run from the repository root in PowerShell:

```powershell
.\build.ps1 -Test            # go vet ./..., go test ./..., then GUI build
.\build.ps1 -Dev             # console build for inspecting logs
.\build.ps1 -Test -Server    # tests, Windows GUI, Linux/amd64 server
.\build.ps1 -Version 1.2.3   # explicitly override the embedded version
node web/tests/i18n.test.js
node web/tests/analytics.test.js
node web/tests/profiles.test.js
node web/tests/polling.test.js
```

Builds go to `dist/`. The script restores the session's directory and
`GOOS`/`GOARCH`/`CGO_ENABLED` when it ends. `-Dev` changes the Windows subsystem, not the data
directory: use `--data-dir .\debug\desktop` when running a development binary.
The root entry, `internal/desktopapp` and `internal/winutil` target Windows APIs, so a native
macOS/Linux `go test ./...` cannot test the complete desktop application.

## Server on a development host

This uses disposable data, demo users and fake authentication. The explicit
loopback listener is necessary because the server otherwise listens on `:8080`.

```sh
go run ./cmd/rltracker-server --listen 127.0.0.1:8080 --public-url http://127.0.0.1:8080 --insecure-dev-login --demo --data-dir ./debug/server
```

For actual hosting and OIDC, follow [SELF_HOSTING.md](SELF_HOSTING.md).

```sh
go build -o ./dist/rltracker-server ./cmd/rltracker-server
go build -ldflags '-X rocket-tracker/internal/buildinfo.Version=1.2.3' -o ./dist/rltracker-server ./cmd/rltracker-server
./dist/rltracker-server --version
```

Both executables use the single `internal/buildinfo.Version` variable, whose
default is `dev`. No numbered source version or version suffix in commit titles
is required. Plain `go build`, `build.ps1` without `-Version` and Docker builds
without a `VERSION` build argument report `dev`; there is no automatic Git
version discovery. `build.ps1 -Version 1.2.3` injects that value into both
binaries. Release tags and tagged Docker builds inject the tag minus its `v`;
`docker build --build-arg VERSION=1.2.3 -t rocket-tracker-server .` injects an
explicit Docker version.

**Custom build-script migration:** replace the old linker target `main.Version`
with `rocket-tracker/internal/buildinfo.Version` everywhere, for desktop and
server builds. The old symbol no longer exists; a stale `-X` target can leave
the binary reporting `dev` without making the build fail. Check the output of
`rltracker version` or `rltracker-server --version` after changing a build script.

The existing desktop updater skips its release polling entirely when the
current version is not a numeric `x.y.z`, including `dev`. A plain development
build therefore neither checks for nor offers an automatic update. To test
numbered-version update behavior, inject an explicit version; doing so enables
the normal release check. This does not change server deployment behavior.
The CI version checks build both plain and injected binaries and assert their
reported versions (server on Linux, desktop on Windows).

## Focused checks

Choose packages affected by the change. These example commands run on a
non-Windows host as well:

```sh
go test ./internal/domain ./internal/tilt
go test ./internal/tracker ./internal/statsapi
go test ./internal/application ./internal/store ./internal/agent ./internal/server
go test ./internal/update ./internal/config ./web
go vet ./internal/domain ./internal/tilt ./internal/store ./internal/agent ./internal/server ./cmd/rltracker-server
node web/tests/i18n.test.js
node web/tests/analytics.test.js
node web/tests/profiles.test.js
node web/tests/polling.test.js
node -e 'const a = require("./web/app.js"); console.log(Object.keys(a).length > 0)'
```

The last command is an analytics import smoke check, not an assertion of
analytical correctness. Pure functions exported by `web/app.js` can be exercised
with Node's built-in assertion/test tools and synthetic match fixtures.

For shared match-model changes, run domain, Tilt, store and tracker tests
together. Domain tests cover JSON shape, missing telemetry, normalization,
profile identity and the production import boundary across platform files.
`internal/tilt/domain_contract_test.go` also checks store aliases and the JSON
round trip between store and domain. Inspect the production dependency graph:

```sh
go test ./internal/domain ./internal/tilt ./internal/store ./internal/tracker ./internal/statsapi
go list -f '{{.ImportPath}}: {{join .Imports ", "}}' ./internal/domain ./internal/tilt
go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./internal/domain ./internal/tilt
```

Domain production code uses only standard-library dependencies; Tilt imports domain,
`sort` and `time`. Tests may import store to exercise source compatibility; this
does not add store or SQLite to Tilt's production graph.

The broader portable-package suite is:

```sh
go test ./internal/... ./web ./cmd/rltracker-server
```

`TestEnableStatsAPIAtomicOnFailure` in `internal/setup` expects a read-only INI
file to reject an edit, which only happens on Windows; it skips elsewhere.

Where the host supports the Go race detector and its native toolchain:

```sh
go test -race ./internal/domain ./internal/agent ./internal/config ./internal/server ./internal/statsapi ./internal/store ./internal/tilt ./internal/tracker ./internal/update ./web ./cmd/rltracker-server
```

The race detector finds Go memory races, not every SQL read/modify/write race.
Persistence changes also need concurrent and replay assertions on stored data.

## Cross-compilation and simulation

From a Unix shell:

```sh
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o ./dist/rltracker.exe .
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o ./dist/rltracker-server-linux-amd64 ./cmd/rltracker-server
```

These verify compilation only. Run the desktop tests on Windows to check
registry, command-line and other platform behavior.

The Windows CLI's `simulate` command emits synthetic Stats API traffic; its
flags are in `internal/desktopapp/app_windows.go` (`cmdSimulate`) and `internal/sim/`. Use a separate test
port and data directory when connecting a tracker to it. Simulation does not
establish compatibility with a running Rocket League installation.

## Current publication workflow

See [RELEASING.md](RELEASING.md) for the current Build/Tests graph,
immutable candidate checks and separately guarded binary/image publication.
PRs and work branches do not receive publisher credentials. Windows native,
Docker and real external qualification have separate evidence.

Keep runtime data out of patches. Before committing, inspect `git diff --check`
and the complete diff, run the applicable checks above, and report any platform
or external-service coverage that is missing.

The frontend checks run from `web/tests/` and require Node.js. All CI and release workflows configure Node 22 explicitly. Only named runtime assets are embedded by `web/embed.go`; test scripts, fixtures and Go source are excluded.

## Windows composition and delivery changes

`main.go` delegates to `internal/desktopapp.Main`; the package's `_windows.go`
files preserve CLI, data paths, autostart and shared version injection. A local
cross-check compiles tests without running them:

```sh
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/desktopapp.test.exe ./internal/desktopapp
```

Run `go test ./...` on Windows for native package tests. Tray, elevation and
restart with a real game remain interactive qualification. Before migration,
follow [OPERATIONS.md](OPERATIONS.md) for a stopped coherent backup. v8 stores
local delivery intents in SQLite; a downgrade cannot safely ignore this table.
See [ADR-0002](decisions/0002-livraison-durable.md).

Build/Tests/Release artifact handoff is described in [RELEASING.md](RELEASING.md).
The immutable candidate is checked before publication, and publishers consume
its downloaded bytes. Running tests does not authorize a release or deployment.

## Security regression checks

The maintained Go toolchain minimum is pinned in `go.mod`; Docker pins the same
patch. CI scans maintained source and the exact downloaded binary candidates
with `govulncheck@v1.9.0`. To reproduce locally, use the candidate's compiler:

```sh
GOTOOLCHAIN=go1.26.9 go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./internal/... ./web ./cmd/rltracker-server
GOTOOLCHAIN=go1.26.9 go build -o /tmp/rltracker-server ./cmd/rltracker-server
go version -m /tmp/rltracker-server
GOTOOLCHAIN=go1.26.9 go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -mode=binary /tmp/rltracker-server
```

Windows-only packages require Windows execution or cross-compilation as described
above. Fake transports and cookies cover refusal boundaries without real IdP or
webhook calls; they do not qualify a production proxy or identity provider.
