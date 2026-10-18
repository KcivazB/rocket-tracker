# L9 — Build, test and publish the same artifacts

## Execution contract

Input: integrated checkout `17c9fe71df7ac9a1d8d7ffda4d205e02c263bf87`, plus
coordinator-owned local integration changes. During this lane the coordinator
moved Windows composition/tests to `internal/desktopapp/*_windows*.go` and
added `web/tests/profiles.test.js`; those source changes are consumed, not
authored here. Linux package patterns exclude the Windows-only composition;
Windows `./...` includes it. The initial three Node scripts were explicit CI
commands; the follow-up below also adds the coordinator-requested polling script.
One writer owns `.github/workflows/*`, `Dockerfile`, `docs/RELEASING.md`
and this record. Runtime, `build.ps1`, `AGENTS.md` and `DEVELOPMENT.md`
are outside this lane. No Git mutations, remote CI operations or publication
are authorized during implementation.

- PRs start Build and package Tests independently. Linux server and Windows
  desktop candidates are transferred as immutable workflow artifacts and their
  version output is checked after download. Go vet/tests and all three Node 22
  scripts remain automatic.
- GitHub Release is a separate tag workflow. Its only writer job requires all
  build/test jobs and downloads their candidates without compiling again.
- Docker exports one Linux/amd64 Docker archive, transfers it to an offline
  version/health test, then loads/tags/pushes that same image without rebuilding.
  Archive SHA-256, image config digest and registry manifest digest are distinct
  identifiers. Registry manifest verification must bind back to the tested
  config digest.
- Only `KcivazB/rocket-tracker` may publish. GitHub releases require a numeric
  `vMAJOR.MINOR.PATCH` tag; image publication retains only upstream `main`,
  `self-hosted` and numeric version tags. Forks and work branches do not publish.
- One version variable remains `rocket-tracker/internal/buildinfo.Version`,
  default `dev`; tags inject their numeric version. No source bump.
- Deploy is N/A: this repository has no deployment workflow. Slow qualification
  (real game, browser, OIDC provider, upgrade/recovery) is outside PR gates.

## Handoff

The owned files are quiescent and ready for the coordinator's independent
review. This lane was authored and locally checked in one Codex session; the
three analytical lenses below are author review, not independent approval.
No Git mutation, remote CI operation, registry push, GitHub release or deploy
was performed.

| Lane | Owner / surface | Evidence / next event | State |
| --- | --- | --- | --- |
| Pipeline implementation | Codex, seven files listed below | Local patch frozen for review | handoff |
| Local verification | Codex, temporary fixtures and isolated Docker builder | Checks below executed; builder and smoke container removed | done |
| Independent review | Coordinator assigns a read-only reviewer | Review the same frozen files before integration | ready |

## Executed evidence

- `actionlint` **v1.7.12** passed on all four workflow files. ShellCheck and
  Pyflakes integrations were disabled because those tools are unavailable;
  every embedded Bash script separately passed `bash -n`, and embedded Python
  passed Python compilation. Dockerfile's RUN command passed `sh -n`.
- A temporary harness executed **81** structure/syntax/script checks, including
  the actual publisher guards against fork, work branch, PR, manual event,
  incorrect case and invalid version refs; all passed. Positive upstream tag
  and allowed image branches were admitted. It also checked permissions,
  dependency edges, no compiler in publishers, Node 22 and all three scripts.
- The actual image test shell ran against a simulated Docker CLI for altered
  archive, incorrect archive SHA, loaded config/version mismatch, unhealthy,
  early exit, event-stream EOF and final-healthcheck failure. Each was rejected;
  cleanup was exercised. Publisher digest comparison was tested against a fake
  CLI only, including a wrong registry config; no registry operation occurred.
- The actual reusable Build and downloaded-binary verification scripts passed
  for server `dev` and `1.2.3` on **Darwin/arm64, Go 1.27.2**. Plain/injected/
  legacy smokes printed `dev`/`1.2.3`/`dev`. Simulated artifact transfer removed
  executable bits before verification. An altered downloaded candidate failed
  its checksum before execution. This is host execution, not Linux execution.
- The actual Build script cross-compiled the Windows/amd64 GUI candidate and
  version smoke binaries. This does **not** execute Windows desktop tests or
  GUI version output; those remain hosted Windows CI checks.
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go vet ./internal/... ./cmd/... ./web`
  passed against the current integrated source. An earlier attempt to run that
  entire pattern on Darwin with CGO disabled failed in the pre-existing Darwin
  systray implementation; it was not treated as a Linux CI failure.
- `CGO_ENABLED=0 go test ./internal/buildinfo ./internal/update ./cmd/... ./web`
  passed on the host (`buildinfo` and cmd have no test files). No full Windows
  or complete Linux package suite execution is claimed by this lane.
- All three scripts passed under **Node 22.23.3** installed in a temporary npm
  cache: `node web/tests/i18n.test.js`, `node web/tests/analytics.test.js`,
  `node web/tests/profiles.test.js`. The host's default Node is 26.11.0.
- **Real Docker build and offline smoke passed.** Docker server **29.8.2** runs
  Linux/aarch64 in OrbStack on Darwin/arm64. A dedicated `docker-container`
  builder compiled the actual Dockerfile for **Linux/amd64 under emulation**,
  with `VERSION=1.2.3`, and exported a Docker archive. The unchanged workflow
  sealing and image test scripts then loaded it, checked config/version,
  observed a healthy event, executed healthcheck and rechecked hashes, with no
  network or published ports in the smoke container. Builder and smoke
  container were removed; the local image/archive remain evidence.
  The initial default Docker driver rejected archive export before compilation;
  the dedicated builder matches `setup-buildx-action`'s driver model.

Real Docker image config digest:
`sha256:c6699a5bbe4ebc9ee3f1a0cf7c463f7e581eeae0aa26d9ea44b25796e7cb8b46`.
Archive SHA-256:
`8a5acb44e4df66343e5e0600537354fead187863d2fbb302aaa803087f53c929`.
These are local artifact identifiers, not a published registry manifest digest.

Temporary evidence/reproduction helpers (outside the repository):
`/tmp/rocket-l9-checks.py`, `/tmp/rocket-l9-binaries.py`, and
`/tmp/rocket-l9-docker.py`. The binary helper completed its Build, transfer,
version and cross-build checks before its Darwin-wide vet failed as described
above; subsequent targeted commands passed separately. Helpers are evidence
from this run, not maintained repository test tooling. Docker fixture and
archive live at
`/var/folders/6z/f46tllln6396f15rnh2qmk1h0000gn/T/rocket-l9-docker-_kq5l451/`.

## Three analytical lenses

| Lens | Author assessment | Strongest objection / disposition | Evidence and remaining gate |
| --- | --- | --- | --- |
| Principal Engineer | PASS for local implementation; independent review required | Rebuilding in a publisher could ship another candidate. Removed: publishers only download/check/copy or load/tag/push. Archive checksum, config identity and manifest digest are distinct. | Workflow graph, real archive transfer/load and host binary transfer checks. Hosted artifact-service handoff and native Windows candidate output remain CI gates. |
| Product Engineer | PASS for local implementation | Users need predictable asset names and version behavior. Existing executable names remain; checksums are additive, version remains dev or injected with the shared path. | Real server/legacy smokes pass. All four current Node scripts pass in the follow-up below. Coordinator has realigned DEVELOPMENT's publication section and desktop composition pointer. |
| SRE | Local offline smoke PASS; publication readiness BLOCKED on external evidence | A tested file alone does not establish registry identity or least-privilege deployment. Publisher verifies config through immutable registry manifest; guards deny forks/work refs. No deploy stage exists. | Real emulated-amd64 image health/version test and negative fixtures pass. Hosted Windows/Actions gates, owner-managed protections/credentials and authorized publication remain unexecuted. |

The initial author review missed the mutable-tag freshness race; its accepted
P2 objection and correction are recorded below. This is a handoff, not
independent approval or publication readiness. Slow real-game,
browser, OIDC and updater installation qualification remains outside PR gates;
Chromium availability does not establish that qualification. Deploy is N/A:
the exhaustive workflow inventory is CI, reusable Build/Tests, Release and
Docker image, with no command that deploys to a running environment.

## Coordinator follow-up and limits

- Independent cross-review uses this frozen scope. Actual Actions upload/
  download, native Windows execution and registry manifest verification must
  be observed in their proper CI/publication environments; local mocks cannot
  supply that evidence. No test tag or image was published to obtain it.
- The coordinator replaced DEVELOPMENT's old publication section with a link
  to RELEASING and corrected the desktop composition pointer. Both changes
  were verified by reading the current source; this lane did not edit that file.
- The coordinator updated the Go web runner to include `profiles.test.js` while
  this lane was active; the explicit workflow commands already include it.
- Repository branch protection/check names and Harbor access policies were not
  inspected or modified. Configure the new Build/Tests jobs as intended;
  ownership/ref allowlists intentionally prevent this work fork publishing.
- Binary downstream-only retries fail closed across attempts: rerun all jobs,
  as described in RELEASING. Publication is not transactional across services
  or image tags; partial external writes require inspection before retry.
- Build and package Tests are parallel. There are six binary matrix jobs and
  three image validation jobs on a PR; Linux package checks overlap between CI
  and Docker. Seven-day artifact retention bounds storage; four binary files
  per target preserve explicit compatibility smokes. No caching redesign or
  slow qualification was added to the required path.

## Mutable-tag freshness follow-up

Target: shared checkout HEAD `33674b3154e73454d5b6c080deba4693e8549cf4`,
with other lanes working on disjoint files. Authorized writes for this follow-up
are `docker.yml`, RELEASING and this record, extended by the coordinator to
`binaries.yml` for the fourth Node script. No runtime, store/import, Git or
remote state was changed. Other agents' current source remains their work.

Accepted P2: artifact identity does not prevent an older, slower run from
overwriting a newer `main`/`latest` image. The publisher now holds a job-level
concurrency lock throughout its work. `self-hosted` is isolated; main and
version tags share a lock because both publish `latest`. Build/Tests retain
their independent parallelism. Active publication is never automatically
cancelled by another run in that group.

Under that lock, a generated Python checker queries the authoritative GitHub
Git References API before registry login and before each tag push. Its token
is read-only. The exact branch/tag must resolve to the tested `github.sha`;
annotated tags are peeled with a bounded, cycle-checked traversal. Changed or
deleted refs, wrong refs/types/SHAs, malformed responses and API failures abort
publication. No Git checkout, cached ref or fallback is used.

Executed follow-up checks:

- `/tmp/rocket-l9-tools/actionlint -shellcheck= -pyflakes= .github/workflows/ci.yml .github/workflows/binaries.yml .github/workflows/release.yml .github/workflows/docker.yml`
  passed with v1.7.12. External ShellCheck/Pyflakes remain unavailable.
- `python3 /tmp/rocket-l9-freshness-checks.py` passed **158 assertions**:
  workflow graph, read-only permissions, exact publication boundaries,
  four explicit Node scripts, Bash syntax and embedded Python compilation;
  existing archive/health negative probes; actual new checker and publisher
  scripts executed with synthetic `gh`/Docker CLIs only. Cases cover current
  main/self-hosted, lightweight/nested annotated tags, stale/deleted/moved
  refs, malformed/null/array API data, invalid SHA/type, tag cycle/depth,
  authentication/API failure, movement before login/before first push/between
  pushes, wrong registry config and empty tags. A newer commit publishes in
  the fixture; a later queued old run makes zero pushes. Removing the gate in
  a mutation probe allows both stale pushes, demonstrating oracle sensitivity.
  The lock/group behavior is checked structurally; GitHub scheduling itself
  is not executed by this harness. CLI timeouts are inspected, not timed out
  against a live API.
- Node **22.23.3** executed `node web/tests/i18n.test.js`,
  `node web/tests/analytics.test.js`, `node web/tests/profiles.test.js` and
  `node web/tests/polling.test.js`; all passed. Polling reports eight scenarios.
  Both binary and image package jobs now invoke all four; Carver owns the Go
  wrapper and polling implementation. No full Go suite was rerun for this
  workflow-only correction.

Fixtures/results are outside the checkout at
`/var/folders/6z/f46tllln6396f15rnh2qmk1h0000gn/T/rocket-l9-freshness-4e0bgkry/`.
The helper is local evidence, not maintained repository test tooling.

| Lens | Author verdict on this delta | Strongest objection / evidence / limit |
| --- | --- | --- |
| Principal Engineer | PASS locally; independent cross-review required | A per-ref lock alone would leave main and version tags racing on `latest`. Their shared lock plus per-push remote equality rejects stale same-ref runs; group routing and reverse completion are probed. No claim of live GitHub scheduler execution. |
| Product Engineer | PASS locally | A rejected old run may leave users waiting for a current image. RELEASING gives a current-ref rebuild/retest recovery path and explains pending-run replacement. Existing latest policy and asset names remain; all four Node scripts pass. |
| SRE | Local checks PASS; live publication readiness remains BLOCKED | GitHub ref reads and registry pushes are not atomic. Movement during a request or API failure between pushes can leave partial tags; probes assert one completed push then rejection. External writers do not share the lock. Hosted serialization/ref reads and authorized registry publication are still external gates. |

`latest` keeps its prior last-publication policy across distinct main/version
refs; this change does not introduce SemVer precedence. Queue ordering is not
trusted. Default pending-job replacement can displace the current run; recovery
requires a complete rerun for the current ref, not forcing an old run through.
No image rebuild, daemon operation, registry push or Git mutation was needed
for this follow-up. The coordinator owns the separate SQLite correction and
the upstream PR transfer. This writer is quiescent after the updated handoff.

## Frozen file inventory

Only these files were written by this lane. Other local integration changes
were authored by their coordinating lanes and were preserved.

| File | SHA-256 |
| --- | --- |
| `.github/workflows/ci.yml` | `ca219a64dffd277f5eaf2dcbdc8ba281999dd8b7ddd5767f9bd239c411e4859e` |
| `.github/workflows/binaries.yml` | `86cfd45c7cece46258943105c7db1b306a9d644fc9ceffee83f8b59a6f9440e0` |
| `.github/workflows/release.yml` | `84339fccffc6f6e1749e750b803903aba842f52fd496c5181ed1f13c32d9e9ad` |
| `.github/workflows/docker.yml` | `ad0975bd4095167926f59ddc62fe112bcbaab4ec1448371cac106eb6afc6c4f4` |
| `Dockerfile` | `3c551f13472a9c54069493c6bbb50a3b107740820424ec2a2e86e4ea597913c3` |
| `docs/RELEASING.md` | `4aaa3ede974971c97df9e0dd8bab5f008ed86baf1f2b72fda6c3c16d67192763` |

The seventh file is this evidence record; its hash is supplied in the handoff
snapshot, outside itself. No source version bump or runtime Go change was made
by this lane.
