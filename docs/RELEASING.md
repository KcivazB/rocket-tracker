# Building and releasing artifacts

The pipelines build candidates, test them, then publish those candidates.
PRs and branch validation do not need registry credentials. Release creation
and image publication are separate jobs with explicit repository/ref guards.

## Stages and triggers

| Workflow | Build | Tests | Release |
| --- | --- | --- | --- |
| `ci.yml` → `binaries.yml` | Windows/amd64 desktop and Linux/amd64 server, in parallel | Go vet/tests and all four Node 22 scripts on Linux and Windows start alongside Build; downloaded binaries are checked after Build | None |
| `release.yml` → `binaries.yml` | Same targets, version from the tag | Same packages/scripts and downloaded-binary checks | GitHub assets, after every Build/Tests job succeeds |
| `docker.yml` | One Linux/amd64 Docker archive; starts alongside package Tests | Portable Go vet/tests, all four Node 22 scripts, then offline image version and health checks | Load/tag/push the tested archive after both Tests jobs succeed |

CI runs for every PR and branch push. Docker validation runs for every PR and
pushes to `main`, `self-hosted` or `v*` tags. The release workflow runs for `v*`
tags. Tag validation accepts `vMAJOR.MINOR.PATCH` with numeric components;
other `v*` tags fail validation without publication.

Publication is allowlisted to **`KcivazB/rocket-tracker`** and a push event whose
repository is not a fork. GitHub releases require a version tag. Image pushes
retain upstream `main`, `self-hosted` and version tags. Exact-case shell guards
run before the release action or registry login. A fork, PR, work branch or
manual workflow call cannot publish through these jobs. Changing publication
ownership requires an explicit review of these guards and registry settings.

All workflows default to `contents: read`. Only the GitHub release publisher
has `contents: write`. Only the image publisher receives `HARBOR_USERNAME` and
`HARBOR_PASSWORD`; its token stays read-only. Checkouts do not persist Git
credentials. No `pull_request_target`, privileged `workflow_run`, manual
publication trigger or cross-run artifact download is used.

## Version contract

`internal/buildinfo.Version` is the only version variable; its source default
is `dev`. Plain builds remain development builds and the desktop updater skips
its release polling for `dev`. Release/tagged-image builds inject the tag
without `v` using:

```sh
go build -trimpath -ldflags '-X rocket-tracker/internal/buildinfo.Version=1.2.3' -o /tmp/rltracker-server ./cmd/rltracker-server
/tmp/rltracker-server --version
```

The explicit frontend checks in both workflows are `node web/tests/i18n.test.js`,
`node web/tests/analytics.test.js`, `node web/tests/profiles.test.js` and
`node web/tests/polling.test.js`.

The desktop command is `rltracker.exe version`. CI checks the actual desktop
candidate built with `-H windowsgui`, plus console smoke builds. On each target,
plain and injected smokes must print `dev` and `1.2.3`. The retired
`-X main.Version=1.2.3` may still link successfully but must print `dev`; it is
kept only as a negative test. Migrate external build scripts to the package
path above. Do not bump source literals or require versioned commit titles.
The Dockerfile also checks the version of the binary it just built.

## Artifact identity and publication

Binary Build uploads four binaries and `SHA256SUMS` per target as one immutable
v4 workflow artifact, named with the run ID and attempt. Tests download that
same artifact, restore executable permissions, verify hashes and execute the
candidate/version smokes. Tests verify the hashes again after execution.

The GitHub publisher waits for all Build/Tests jobs, downloads those same-run
artifacts, verifies their checksums and copies the two candidates unchanged.
The public assets remain `rltracker.exe` and `rltracker-server-linux-amd64`,
with an additional `SHA256SUMS`. It has no checkout or compiler. Users can
verify downloaded release assets with `sha256sum -c SHA256SUMS`; the Linux
binary needs `chmod +x` after download. These hashes establish byte identity
within the run; they are not signatures or independent provenance attestations.

Docker Build uses `type=docker,dest=.../image.tar`, with no registry login or
push. The artifact contains `image.tar`, `image-id.txt`, `version.txt` and
`SHA256SUMS`. Downstream jobs use the immutable artifact ID and compare the
archive checksum against the Build job output, in addition to the manifest
checksums. Tests load the archive and require its loaded config digest to match
Build's digest, then run the version command and an actual healthy server:

- `--network none`, no published ports, loopback listen, temporary data only;
- read-only root filesystem, nonroot image user, no Linux capabilities;
- insecure development login only inside this isolated smoke container;
- success requires a Docker `health_status: healthy` event and a successful
  `healthcheck` command; timeout, unhealthy or exit is failure. Cleanup always
  removes the container. The timeout is a failure guard, not a success delay.

The publisher reloads and verifies the same archive, tags its tested image ID
and calls `docker push`. It never invokes a build. For each generated tag it
resolves the registry manifest digest, reads that immutable manifest and
requires `.config.digest` to equal the tested image ID. The job summary records
the published immutable references. Metadata tags preserve `main`/`self-hosted`,
`latest` for main and SemVer releases, SemVer tags and a commit tag; branch images contain `dev`.

Only the image publisher is serialized. Its concurrency group isolates
`self-hosted`; `main` and all version tags share another group because they
also write `latest`. This serializes every target ref and the shared alias,
without serializing Build/Tests. `cancel-in-progress: false` lets an active
publication finish. Queue order is not used as a freshness guarantee.

Inside that lock, before registry login and again before **each** image push,
the publisher reads the exact ref from the authoritative GitHub Git References
API using its read-only token. Branch commits and lightweight/annotated tags
must resolve to `github.sha`, the tested commit. Moved/deleted refs, API errors,
invalid responses and unresolved tag chains fail closed. No checkout ref,
cached response or previous successful check can authorize a later push.
An older queued run therefore cannot overwrite an already-published newer
commit of the same ref. A ref can still move during a registry request: GitHub
and Harbor do not offer an atomic ref-check-and-push transaction. Rechecking
each tag limits that window; a failure can leave a partial tag set.

`latest` retains its existing policy: the last successful serialized current
`main` or version-tag publication writes it. This is not a SemVer precedence
policy across different version refs. External/manual registry writers do not
participate in the workflow lock and require their own owner policy.

The archive SHA-256 identifies the transferred file; the image ID is the config
digest, which binds runtime configuration and uncompressed filesystem layers;
the registry digest identifies its manifest and compressed layer references.
They are deliberately recorded separately. Docker transport can change layer
compression/manifest serialization, so archive bytes are not claimed to equal
registry bytes. Publication uses the tested image and verifies that config
identity, without rebuilding. This is one platform; no multiarch index or
attestation is claimed.

## Validation and recovery

Review workflow syntax locally with an up-to-date `actionlint` (v1.7.12 was
used for this lane). The source inputs for artifact actions and the Docker
archive handoff are documented by [GitHub download-artifact](https://github.com/actions/download-artifact/tree/v4)
and [Docker's cross-job image example](https://docs.docker.com/build/ci/github-actions/share-image-jobs/).
The digest check uses [Buildx manifest inspection](https://docs.docker.com/reference/cli/docker/buildx/imagetools/inspect/).
Publisher serialization and remote ref resolution follow
[GitHub concurrency](https://docs.github.com/en/actions/concepts/workflows-and-actions/concurrency)
and the [Git References API](https://docs.github.com/en/rest/git/refs#get-a-reference).

A first trusted CI run must demonstrate the hosted Linux and Windows commands,
artifact transfer and offline Docker smoke. Local static validation or a
Windows cross-build does not establish native Windows execution. Actual
publication requires separate authorization; never push a test tag or image
from a fork/work branch to gather evidence. Branch protection and registry
credential policies must be configured by the repository owner; YAML cannot
prove those settings.

For binary workflow retries, rerun **all jobs**: names include the attempt, so
retrying only a downstream job cannot silently consume a previous attempt.
Artifacts are retained for seven days. If expired, rebuild and retest in a new
complete run before publishing; do not substitute another run's artifacts.
A missing artifact, checksum mismatch, failed smoke or rejected ref blocks its
publisher. Inspect the failed stage; do not bypass its dependency gate.
For a freshness rejection, build/test the current ref instead of retrying the
old commit. With the default concurrency queue, a newly pending publisher can
replace an existing pending one even though active jobs are not cancelled;
if the current commit's publisher was displaced, rerun that complete current
run. A freshness rejection intentionally prevents stale publication even when
that leaves a mutable tag awaiting a new successful run.
Publication is not transactional across GitHub and Harbor or across image
tags: if an external write fails partway, inspect already-created assets/tags
and their recorded hashes/digests before an authorized retry. Do not move an
existing version tag or silently replace an earlier release's assets.

Slow qualification (real game, browser, OIDC, updater installation, restoration)
is separate from the required PR path. **Deploy: N/A**. The workflow inventory
is `ci.yml`, `binaries.yml`, `release.yml` and `docker.yml`; none deploys a
service or updates a running environment. Publishing an image or a release
neither deploys it nor qualifies those external scenarios.

## Compiler and dependency security gate

`go.mod` requires Go 1.26.9 and Docker builds with `golang:1.26.9-alpine`.
`binaries.yml` scans maintained portable source and the exact downloaded desktop
and server candidates using `govulncheck@v1.9.0`; `go version -m` records their
compiler metadata. Docker verification extracts and scans `/rltracker-server`
from the loaded candidate image before its version and health checks. A finding
fails Tests and blocks publication. The publisher still consumes the verified
immutable candidate; it does not rebuild it.

Dependabot proposes weekly Go module, Docker and Actions updates. Maintainers
must also check Go security releases and update the `go.mod` minimum and Docker
patch together; dependency automation is not evidence that a compiler patch was
adopted. Rebuild and rescan after a patch. A source scan on a different compiler
does not establish the security state of a previously built candidate.
