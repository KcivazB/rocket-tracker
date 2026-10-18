# L1/L2 — Ingestion validation

Status: implemented locally; not merged or released. Base: `6f531fd43be3bcd6f3a746e3b352103ef2a06ef3`. Epic E1; US-02 and US-09. [Plan and documentation contracts](https://github.com/KcivazB/rocket-tracker/issues/2#issuecomment-6250332932).

The shared match ingestion path rejects impossible quantities before persistence for device uploads, queued JSON and database imports. Signed coordinates and goal difference, absent historical telemetry and explicit zero remain valid. Invalid imported records are skipped individually; the import is not an all-or-nothing transaction.

Device matches and trainings require exactly one JSON value and complete-body validation within 4 MiB / 512 KiB before writes. Unknown fields remain tolerated. Format errors return 400 and body-limit errors 413.

Before implementation, focused regression tests failed for negative quantities and trailing/oversized JSON on the original code. The implementation preserves the existing authorization and store convergence. Current contracts are documented in [SPEC](../../SPEC.md#agent-ingestion-and-offline-imports) and [self-hosting](../../SELF_HOSTING.md#ingestion-errors).

Acceptance checks cover live and queue/database imports, invalid input with no persisted row, replay of a mixed valid/invalid database import, partial/signed telemetry, extra values/suffixes, exact limits and overflows with known and unknown lengths. Tests use synthetic temporary SQLite data; the new handler tests do not open a listening socket. Existing package integration tests use their existing local test servers. Windows, real game, real IdP, proxy and load qualification are separate.

Principal Engineer: one shared validation path, existing scope and convergence retained. Product Engineer: valid old/partial data and signed positions remain usable. SRE: failures occur before persistence; clear 400/413; no network resilience claim. Review and exact test results are recorded in the PR.

## Executed evidence and retirement

`go test ./internal/server ./internal/store ./internal/agent -count=1`, targeted `go vet`, a native-host server build and a Windows/amd64 cross-build passed. Independent source review raised a 400/413 precedence defect; the decoder now finishes its bounded read before JSON classification, and regression cases cover early malformed JSON, a second value and suffixes above the limit. The reviewer rechecked the fix without rerunning the suite. The complete-body buffer and decoded objects add O(limit) memory per request; no memory/load measurement was performed.

The application guides above own the current contract. Before removing this temporary file, preserve decisions in the ADRs (the proposed measurement ADR is in documentation PR #3) and transfer exact evidence, limitations and open tasks to the PR/issue. This record does not close the full audit.
