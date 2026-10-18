# Operations: imports, stopped backups and isolated recovery

This guide covers the local L10 implementation and its synthetic exercises. It
does not establish a production capacity, recovery-time objective (RTO),
recovery-point objective (RPO), Windows qualification or deployment result.
Keep real databases, exports, queue entries, manifests and credentials private.

## HTTP import budgets

`POST /api/import` retains the authenticated user's scope (the local user in
desktop mode). Files can mix a SQLite database and outbox JSON documents.
Database imports retain matches, local manual days and local trainings; they
do not restore users, credentials, settings, ranks or delivery intentions.
**Import is not backup restoration.**

The handler first stages the complete multipart envelope in a private temporary
directory. Only after a complete envelope fits the following budgets does it
start importing records. Set these process environment variables and restart
the service/desktop process. They are read once for each admitted request;
there are no corresponding CLI flags or settings-screen fields.

| Setting | Default | Effective boundary |
| --- | --- | --- |
| `RT_IMPORT_MAX_BYTES` | `1073741824` (1 GiB) | Whole HTTP body, including multipart framing, fields and epilogue; also enforced for unknown/chunked lengths. |
| `RT_IMPORT_MAX_FILES` | `128` | File parts per request. |
| `RT_IMPORT_MAX_PARTS` | `256` | All multipart parts, including ignored form fields. |
| `RT_IMPORT_MAX_TEMP_BYTES` | `1073741824` | Sum of file payload bytes staged for one request. The probe for an excess byte is never written to disk. |
| `RT_IMPORT_MAX_CONCURRENT` | `2` | Active HTTP imports per process, across users and Server instances, through cleanup; excess requests are rejected without waiting. |
| `RT_IMPORT_TIMEOUT` | `5m` | Time from admission through reception and import; HTTP read deadline plus cancellation of contextualized database work. |

These are configurable protective choices, **not measured player needs or a
capacity promise**. Byte counts must be positive decimal integers below
9223372036854775807; counts must be positive integers supported by the host;
timeout is a positive Go duration such as `30s` or `5m`. Empty values use the
defaults. Invalid settings fail the import closed with HTTP 500 and identify
the setting, rather than silently disabling a limit.

The default reception budget is at most 2 GiB of staged payload across two
admitted imports. The source SQLite file is opened read-only with trusted schema
disabled. Import never migrates or repairs that source. Views, triggers, virtual
or shadow tables, generated columns and unsupported schema versions are rejected;
ordinary historical match tables remain supported. When the historical goal
repair marker is absent, the known full/empty duplicate-goal pattern is converted
in memory, idempotently, before ingestion. No source SQL repair runs and no
unknown speed is converted from its numerical magnitude. Rows are streamed with the
request context rather than materializing the full history. Cancelled SQL is
interrupted and capacity is released after cleanup.

These controls are not a filesystem, process-memory or hard wall-clock quota.
There is no independent row-count limit; large individual values, destination
writes and filesystem operations still consume resources. Reserve space for the
destination database and use a private capacity-limited temporary filesystem
when a hard disk quota is needed. `Server.TempDir` selects staging for embedded
callers; otherwise `os.MkdirTemp` uses the system temporary directory. A proxy
body/time limit may reject an upload sooner with its own response format.

Cancellation prevents subsequent import-loop writes once observed; earlier
commits remain applied. No import writer continues after the handler returns.
A client disconnect can prevent delivery of the response and leave an uncertain
final count. Keep the source and reconcile partial progress before replay.

Temporary staging is removed on ordinary success and failure. Process crashes
or filesystem cleanup failures can leave `rltracker-import-*` directories. With
all import processes stopped, inspect only this dedicated temp location and
remove abandoned import directories after confirming they are not recovery
sources. Never delete the application data directory or its WAL as cleanup.

### Responses and partial progress

Errors keep the existing `error` string and add `field` and `imported`, e.g.:

```json
{"error":"too many files; split the upload (RT_IMPORT_MAX_FILES)","field":"files","imported":{"matches":0,"manual":0,"trainings":0}}
```

| HTTP | `field` | Operator/player action |
| --- | --- | --- |
| 413 | `body_bytes`, `temporary_bytes`, `files`, `parts` | Split the batch; operator may adjust the named limit after checking disk/memory capacity. |
| 429 | `concurrency` | Retry later; `Retry-After: 1` is advisory, not a promised free slot. |
| 408 | `duration` | Inspect partial counts; retry a smaller batch or investigate slow storage. |
| 400 | `multipart`, `files` | Resend a complete multipart upload or select at least one file. |
| 400 | `file` | Inspect the named file/content failure and the already-imported counts. |
| 500 | setting name or `temporary_bytes` | Correct configuration, permissions or available space before retrying. |

Invalid/truncated multipart envelopes and staging/admission budget rejections
write **no destination matches, manual days or trainings**, even if an earlier
part was valid. Staging itself writes temporary files; this is not a claim of
zero filesystem writes. Envelope checks do not validate every record in advance.

After staging, import remains **non-transactional across records and files**.
Malformed queue JSON, invalid match/training quantities and non-SQLite files
larger than the existing 4 MiB JSON ceiling are reported in `skipped`; valid
records in the same request can succeed. A corrupt SQLite source or a database
error can stop the request after earlier records committed. An error includes
the counts accumulated before failure; these count successful operations,
including updates, not necessarily newly inserted rows. A timeout may likewise
leave earlier operations applied. Keep the source and response; repeating an
import retains the existing deduplication and training-merge rules. Replay is
not an undo operation. The offline `rltracker-server import`/agent CLI paths do
not acquire these HTTP budgets.

## BACKUP: one coherent bundle with every writer stopped

1. Identify the server and each desktop data directory, external configuration,
   secrets and the exact executable/image version. On the desktop the default is
   `%APPDATA%\RocketTracker\`; the server database is
   `<RT_DATA_DIR>/rltracker-server.db`. Record identities and destinations in a
   private inventory; do not paste them into an issue or public manifest.
2. Stop the owning service/container, desktop tracker, agent and CLI import
   jobs. Disable automatic restart for this maintenance window. Confirm that
   all their processes have exited before copying anything. This runbook does
   not authorize stopping somebody else's running service.
3. Copy the **whole stopped data directory**, preserving relative paths and all
   existing `.db`, `-wal` and `-shm` files together. A clean `Store.Close`
   checkpoints; an unclean stop can leave committed data in WAL. Never delete
   WAL or copy only the database after an unclean stop. Keep each PC/server's
   bundle separately; do not merge unrelated SQLite directories.
4. Include the following inventory. Copy externally stored items as private
   companion files with explicit restoration paths. Do not assume a data-volume
   copy contains mounted secrets, environment files or the identity provider.

| Component | Preserve and check |
| --- | --- |
| SQLite + WAL/SHM | All match perspectives/profiles, manual days, trainings, ranks, users and OIDC subjects, settings, device/session hashes, `InstallationID`, the v8 delivery journal, v9 persistent destination origins and v10 training source keys. |
| Configuration | `config.json`, `agent.json`, deployment environment/config files, service identity, data paths and version/artifact reference. Device tokens live in agent configuration; server hashes cannot reconstruct them. |
| File queues | `outbox/` including `destination`, rejected files and all `outbox-*` aside directories; preserve unknown historical destinations too. |
| Delivery intentions | `PendingDeliveries(ctx, destination)` payload/key/revision, held/rejected state and preserved legacy documents; these live in SQLite (`delivery_intents`/`legacy_deliveries`), not a separate export. |
| Identity and secrets | Server installation identity, server URL + server installation ID + account ID for each destination, IdP issuer/client configuration and secret files, device credentials and Discord webhook settings. Preserve or separately recover IdP state; an app backup does not back up the IdP. |

5. Use a private directory (0700 directories / 0600 files on POSIX, equivalent
   restricted ACLs on Windows), encrypted storage and an independently retained
   copy. Generate SHA-256 checksums for **every copied file**, including WAL,
   configuration and private companions. A manifest contains sensitive paths;
   protect it like the backup. Reject symlinks/special files or explicitly
   inventory their actual private targets; do not accidentally copy unrelated
   files through a link.
6. Verify the copy against the manifest before restarting the original service.
   Keep the source backup immutable during all restore trials. Select retention
   and acceptable data loss with the owner; no RTO/RPO is asserted here.

The agent uses a persistent `agent.json.lock` companion for cooperating setup
and background updates. Never unlink it while any process is running: the open
OS handle provides exclusion and process exit releases it. Restore only after
all processes have stopped. An old heartbeat is rejected when the setup
configuration generation or target changed; confirm the intended setup rather
than copying credentials from an old response. Older executables and external
editors do not implement this lock protocol.

## Restore into isolation first

1. Create a fresh private destination directory on an isolated host or test
   environment, with outbound network and agent auto-start disabled. Restore
   the complete matching bundle, never onto a running application. Keep the
   original backup and previous data directory intact.
2. Verify checksums **before opening SQLite**, because opening/checkpointing can
   legitimately change database/WAL bytes. Restore the recorded executable
   version first. For a newer version, review/test migrations on another copy;
   never run an older executable blindly against a v8 database. Rollback uses
   the complete old bundle with its old executable, not a schema downgrade.
3. Open only the isolated copy. Check SQLite integrity and the expected users,
   profiles, counts, manual days, trainings, settings and device ownership.
   Confirm that `InstallationID` is unchanged. Do not run both the original
   installation and its restored clone as active uploaders with the same ID.
4. Inspect `DeliveryStatus(ctx)` and `PendingDeliveries(ctx, destination)` for
   each **verified** destination. Verify payloads, keys and revisions against
   the saved baseline. A same-URL replacement server with a different server
   installation ID is a different destination, as is a different account.
   The current pending API returns at most 100 entries per call. A list of 100
   is not proof of a complete inventory; do not acknowledge real entries just
   to page through them. For a larger journal, have its owner inspect all rows
   on the isolated SQLite copy and reconcile them with status totals before
   authorizing live recovery. The synthetic fixture contains two pending rows.
5. Missing/unknown destination or account: retain the entries held, preserve
   their original sources and obtain an explicit owner decision. Neither the
   current token nor the URL alone proves their old account. No automatic
   migration/rebinding of unknown destinations, copying them into today's
   pending set, or automatic upload is permitted. `PreserveLegacyOutbox` can
   preserve legacy documents without making them sendable; it retains sources.
6. Exercise acknowledgement/replay only against synthetic transports until
   identity and destination checks pass. Re-enable only the intended instance
   after owner validation. A local restore test does not validate production
   connectivity, the real IdP, upload convergence or secret rotation.

### Executable synthetic exercise (Go, existing dependencies)

Run from the repository root; this creates only `testing.T.TempDir` fixtures:

```sh
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local go test ./internal/server -run '^TestOperations(StoppedBackupRestore|SQLiteWALCopy)$' -count=1 -mod=readonly -v
```

`TestOperationsStoppedBackupRestore` uses the application's real SQLite store
and journal APIs. It closes the store, copies the full synthetic directory to
a backup, copies it again to an isolated restore, verifies SHA-256 bytes before
opening, and reopens the store. It asserts installation ID, pending payloads,
keys and revisions, manual/training data, settings/device ownership, complete
queue/config/secret files, held legacy state and destination separation. An
old-revision acknowledgement cannot remove the restored newer revision. It
never starts an agent, IdP or network transport.

`TestOperationsSQLiteWALCopy` separately constructs a real stopped SQLite WAL
bundle and proves that copying the DB/WAL/SHM together recovers the WAL-only
row and passes `PRAGMA integrity_check`. Its tiny schema tests the copying
procedure, not application migration. These Go tests are a repeatable exercise,
not a production backup utility. See the temporary
[L10 evidence record](implementation/steps/L10-operations.md) for actual commands,
results, outstanding review and qualification limits.

## Discord delivery and shutdown

Server shutdown cancels and joins owned purge, recap and highlight workers before
closing SQLite. Highlights and session recaps remain best effort. A cancelled
weekly recap leaves its slot pending. Per-destination progress is persisted using
hashed webhook identities, so a retry skips destinations already processed in
that slot. Ordinary webhook failures keep the existing best-effort policy; they
are logged and consume that destination's attempt. A crash between a successful
remote post and the local checkpoint can duplicate a post: this is not an
exactly-once transport. Include the Discord state file in the stopped backup.

## Local recording errors

A failed match/training save is retained as an immutable in-memory snapshot and
retried on following observations and during flush. Logs expose pending/error
state, and desktop shutdown returns an unresolved flush error before closing the
store. Fix storage permissions, disk space or invalid setup and retry while the
process remains alive. This is recovery from a transient save failure, not a
durable precommit journal: a forced exit or power loss can lose uncommitted
snapshots. Once committed, result and delivery intention share the SQLite
transaction and the stopped backup procedure applies. Setup and recording share
the configuration lock through commit; external editors/older executables do
not participate in that protocol.

## Timer recovery export

A rejected or out-of-bounds timer stays in browser storage with retry, JSON export
and confirmed discard actions. Export preserves the frozen raw timer payload;
keep that original immutable. For manual recovery, validate its activity and
calendar bounds and supply the timer source plus the queue envelope expected by
`Importer.QueueEntry` before importing into an explicitly selected account.
An overlong session needs an explicit recovery decision; export does not invent
valid replacement sessions or a profile destination. A confirmed discard frees
the browser timer for a new session.
