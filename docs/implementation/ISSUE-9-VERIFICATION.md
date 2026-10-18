# Issue #9 — remediation evidence

Starting revision: `1369874f2db0a7cf93ad793f1b57cf627e526f59` (PR #8).
Scope: all 22 findings in [issue #9](https://github.com/KcivazB/rocket-tracker/issues/9).
Implementation and regression evidence below belong to the updated PR #8;
maintainer review, merge, release, deployment and external qualification are
separate states. The [plan](ISSUE-9-PLAN.md) records personas, stories, epics,
dependencies and documentation per stage. Earlier L0–L10 verification is retained
as historical evidence; this record supersedes readiness claims for the old head.

## Finding-to-contract matrix

| Finding | Implemented result | Maintained regression / evidence | Current documentation |
| --- | --- | --- | --- |
| ARCH-01 | Explicit local-only, unknown and bound origins never merge across destinations, including after acknowledgement/reopen | `store/persistence_regression_test.go`: origins all directions, historical distinction, atomic rollback | ARCHITECTURE, ADR-0002/0005 |
| ARCH-02 | Immutable snapshots survive callback errors/panics and retry; flush returns unresolved errors | `tracker/persistence_test.go`: result/training retry, mutation isolation, panic; invalid retained training cannot block a valid later match | ARCHITECTURE, OPERATIONS; precommit memory limit explicit |
| ARCH-03 | Training union never exceeds 12 h; split rows retain all sender keys and edited focus across replay | `store/persistence_regression_test.go`: split/replay/focus, merged keys, v9→v10; interval-cap split replays converge | ARCHITECTURE, ADR-0002/0006 |
| ARCH-04 | Offline transport-key branch respects explicit profile | `TestTransportKeyCannotOverrideExplicitMatchProfile` | ARCHITECTURE, ADR-0005 |
| ARCH-05 | Persisted setup selection and recording commit share interprocess lock; stale uploader cannot select destination | `desktopapp/recording*_test.go`: separate-process OS-lock probe, commit/rollback, missing/invalid setup; Windows composition test | ARCHITECTURE, OPERATIONS, ADR-0005 |
| ARCH-06 | Owned server workers cancel/join; cancelled weekly recap keeps unsent recipients pending | `server/discord_lifecycle_test.go`: highlight cancellation/join, weekly restart without resending processed destinations | OPERATIONS, ARCHITECTURE |
| PROD-01 | Variant/mode/duel cohorts remain separate | `web/tests/analytics.test.js`: Hoops/Dropshot confounding and positive within-cohort control | SPEC, ADR-0004 |
| PROD-02 | Actual activity intervals exclude pauses across blocks/reload; failed save retains fixed payload | Domain interval tests, store round-trip, actual timer VM; HTTP regression accepts 4096 intervals on both routes | SPEC, ARCHITECTURE, ADR-0006 |
| PROD-03 | Demolition availability is independent from touch telemetry | Analytics tests and actual FR/EN rendered demo-only card | SPEC, ADR-0004 |
| PROD-04 | All historical temporal inferences use qualified chronology | Analytics malformed teams/times/scores/counts and rendered history/detail tests | SPEC, ADR-0004 |
| PROD-05 | Independent shared revision triggers reload on another user's contribution/deletion; failures retry | Store/server A/B scope tests; polling controlled revision/failure/staleness/fallback cases | ARCHITECTURE, SPEC |
| PROD-06 | Real 60-second buckets; live and baseline pace use equal regulation duration | Analytics 180/300/OT fixtures, rendered duel labels, i18n tests | SPEC, ADR-0004 |
| PROD-07 | Zero disables tilt warning while retaining factual streak | FR/EN rendered 0/3/absent/4 regressions | SPEC, FR/EN help |
| PROD-08 | Rank dialog retains and explicitly labels its original destination | Existing A→B target test retained and label assertion | SPEC, ADR-0005 |
| SEC1 | Every match-derived textual CSV cell neutralizes formula prefixes, including mode/variant/first_goal | All text-column matrix and HTTP import→own/social export, both separators; signed numeric control | SPEC |
| SEC2 | Redirect refused before any second payload/credential request; journal remains pending | Agent redirect matrix, injected hook, real journal negative acknowledgement | SELF_HOSTING, ARCHITECTURE |
| SEC3 | UserInfo subject must equal verified nonempty ID-token subject before profile/groups merge | Synthetic signed IdP and subject/group/crypto/PKCE negative tests | SELF_HOSTING, ARCHITECTURE |
| SEC4 | Untrusted SQLite uses separate read-only contextual streamed reader, no migrations/repairs | Hostile view refusal, capacity recovery, legacy source immutability and goal conversion/replay without speed heuristics, actual running SQL cancellation | OPERATIONS, ADR-0007 |
| SEC5 | HTTPS host-prefixed cookies and opaque one-use server-side login transaction | TLS login/session/renewal/logout, namespace, replay/concurrent consume, expiry/capacity, 2048-byte owned return path, crypto fixture | SELF_HOSTING, ADR-0008 |
| BUILD-01 | Minimum and Docker compiler patched to Go 1.26.9; CI scans actual candidates | Source and Linux/Windows binary `govulncheck@v1.9.0`; CI records compiler metadata | DEVELOPMENT, RELEASING |
| DEP-01 | go-jose upgraded to 4.1.4 | Readonly module build, scans, negative compact-JWE authentication test | DEVELOPMENT, RELEASING |
| DOC-01 | README delivery and DB path corrected; coherent stopped backup replaces SQLite-only claim | Current README/SELF_HOSTING agree with OPERATIONS and actual journal/config layout | README, SELF_HOSTING, OPERATIONS |

Paths in the regression column are relative to `internal/` unless prefixed with
`web/`. The tests use synthetic fixtures; no real player data or credentials.

## Validation and review record

Local integrated validation and independent review results are recorded in the
PR and the issue against the pushed head. Required checks include the portable
Go suite, vet, focused race suite, all four Node scripts, Windows compilation,
workflow syntax and source/candidate scans with the pinned compiler. Hosted
Windows execution and Docker candidate checks are separately observable CI gates.

## Explicit limits and decisions outside these findings

- Failed **precommit** snapshots remain in memory. Forced exit/power loss can lose
  them; successful result+intent commits are transactional in SQLite.
- The importer has byte/time/admission controls and cancellation, not an OS
  sandbox or strict process-memory/disk quota. Partial commits are not rolled back.
- Discord remains best effort. Crash between remote post and checkpoint can
  duplicate delivery; non-cancellation HTTP failures consume the existing attempt.
- Browser cookie-prefix enforcement, real proxy/IdP, interactive Windows game,
  actual telemetry units and hardware capacity/RPO/RTO require separate qualification.
- Warmup person/profile attribution and deletion replication remain explicit
  product decisions, not silently implemented policy. ADRs remain **Proposed**.
- Independent readers use inherited model settings: no claim of cross-provider
  review diversity. Their findings and resolutions are retained in issue #9.

Only PR #8 remains in the merge sequence. Prior foundation changes are already
integrated; do not merge the superseded #6/#7 proposals separately. Merge #8 only
after its current head passes required checks and receives maintainer approval.

## Findings caught during independent review

The intermediate candidate `ecc32cb` was challenged by readers distinct from each
surface's author. Counterexamples found after the original 22 findings are part
of the same correction scope, not deferred to an unrelated backlog:

- Interval-cap split replay inserted another row; candidate scanning now reaches
  an existing mergeable row. Red on `ecc32cb`, green after correction.
- An invalid retained training blocked later valid matches; each retained record
  now attempts independently, preserving unresolved errors and immutable content.
- OAuth count-only capacity retained large return paths/query backing; paths are
  bounded at 2048 bytes and cloned after validation. Real handler regressions.
- The read-only importer omitted historical duplicate-goal conversion; the pure
  conversion is restored in memory, without old speed-magnitude heuristics.
- Docker publish accidentally repeated the scan with undeclared ambient Go; the
  required scan stays in Verify, and Publish checks the same sealed archive.
- Frontend detail retry now has an explicit action at a stable revision. Failed
  timers retain/export the frozen payload and require confirmed discard to exit;
  actions guard the domain bounds. UTC whole-second canonicalization precedes
  duration and threshold decisions. HTTP round-trip and real timer-handler tests
  cover 299-second and 59-second boundary examples. Re-review is recorded in #9.

The final issue update records independent Principal Engineer, Product Engineer
and SRE lenses, corrections made, exact head, check URLs and external limits.
