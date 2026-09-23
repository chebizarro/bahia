# Container base images wave 4 verification

Date: 2026-09-23. Bead: `bahia-jomfn`. Base: `ae4547e1`; branch: `deps/base-images-wave4`.
Scope: replace Dependabot #18, #31, and #30 directly on current master, without changing application code, the module language floor, or dependencies.

## Acceptance and evidence

| Criterion | Verification | Result |
| --- | --- | --- |
| Consistent image inputs | All tracked Dockerfiles and Compose YAML inspected | Root Go builder 1.27.0-alpine, root/recovery Alpine 3.24, web builder Node 26-alpine, all digest-pinned. Compose has no direct pins to these bases; its prebuilt application references were not retagged. PostgreSQL and nginx bases unchanged. |
| Preserve Go language floor | `git diff HEAD -- go.mod go.sum`; mount module files into pinned Go builder and run `go version` / `go env GOTOOLCHAIN GOVERSION` | Module remains `go 1.26.3`, with no explicit toolchain directive. Builder executes Go 1.27.0 with `GOTOOLCHAIN=local`, not a downloaded replacement. All nine binaries compile with CGO disabled. |
| Alpine package availability | Actual builder/runtime/recovery `apk add` steps; separate union-of-packages installation | `git`, `ca-certificates`, `tzdata`, `wget`, `docker-cli`, and `docker-cli-compose` install without renamed/dropped-package workarounds. Final runtime is Alpine 3.24.2, musl 1.2.6-r2. |
| Node/CI agreement | Frozen-lockfile container install/build; both web workflows use Node 26 | Pinned image runs Node 26.10.0. Existing engines `^22.22.2 || ^24.15.0 || >=26.0.0` already permits it. Package/lockfile unchanged. |
| Node image bootstrap | Original Corepack command tested in Node 26 image | Original command fails with `sh: corepack: not found`, exit 127. Direct `npm install --global pnpm@10` fixes the actual image build, retaining CI's pnpm major. |
| Every modified image builds | Commands below, Docker Desktop cross-platform `linux/amd64` builds | Backend, web, and recovery all passed. No arm64 final image build claimed. |
| Go regression gates | Host Go 1.26.3 darwin/arm64 | `go build ./...`, `go vet ./...`, `go test ./...`, and `make race` all exit 0. Race uses `CGO_ENABLED=1 go test -race ./... -count=1`. |
| Compose remains valid | `docker compose -f docker-compose.yml -f docker-compose.test.yml config --quiet`, with fixture required environment | Exit 0; no deployment performed. |

[Go toolchain selection](https://go.dev/doc/toolchain) permits a newer bundled compiler while the module retains its declared language version. [Corepack's installation documentation](https://github.com/nodejs/corepack#default-installs) documents its absence from Node 25 and later.

## Image builds and smoke checks

```sh
docker build --pull --platform linux/amd64 --progress plain -t bahia:wave4-amd64 -f Dockerfile .
docker build --pull --platform linux/amd64 --progress plain -t bahia-web:wave4-amd64 -f web/Dockerfile web
```

For recovery, the two binaries were copied out of the successfully built backend image into a temporary context containing the unchanged `Dockerfile.recovery` and `.recovery-bin/{bahia-server,bahia-relay}`. `file` confirms statically linked x86-64 ELF binaries; `go version -m` confirms Go 1.27.0. No generated binaries were added to the repository.

```sh
docker build --pull --platform linux/amd64 --progress plain \
  -t bahia-recovery:wave4-amd64 \
  -f /tmp/bahia-wave4/recovery-context/Dockerfile.recovery \
  /tmp/bahia-wave4/recovery-context
```

| Built image | Local image ID | Runtime smoke |
| --- | --- | --- |
| `bahia:wave4-amd64` | `sha256:1b58e37b0cf3f1a061c82e702ac4b901547dcfef719e294785fe198070f6c56d` | Non-root UID 100/GID 101; server, relay, and CLI help entrypoints execute. |
| `bahia-web:wave4-amd64` | `sha256:a069929aa24b12975e58345d48c78d608f8e81409f9aaac52fce045a1a1c2c55` | nginx configuration valid; health event healthy, HTTP 200, bootstrap placeholders replaced with fixture environment. |
| `bahia-recovery:wave4-amd64` | `sha256:cc69546f0a0af44c645843ef00621fa9e432896ecaa525bec8dd9902687d3c0f` | Server/relay help execute; Docker CLI 29.5.3 and Compose 5.1.4 run; read-only version query negotiates API 1.48 with local Docker Engine 28.0.4. |

Web smoke initially queried before nginx readiness; the final smoke waited for Docker's health event and supplied the required bootstrap environment. No production service or relay was contacted. Builds use local verification tags/default version metadata, not published production artifacts. Logs are in `/tmp/bahia-wave4/` on the verification host.

## CI and deployment boundaries

Initial PR Go CI run `35838512887` passed build/vet/test but failed Race at
`internal/app/virtualization_test.go:158` (`context canceled`). All Go source,
module files, Makefile, and Go CI configuration are unchanged from `ae4547e1`.
The focused test passed 100 repetitions under normal scheduling, but
`GOMAXPROCS=1 go test -race ./internal/app -run '^TestVirtualizationCompositionAdmissionProjectionAndShutdown$' -count=20`
reproduced three identical failures. This scheduling-sensitive shutdown failure
is tracked separately as `bahia-z206p`; no test suppression or application-code
fix is included in this batch. Final CI conclusions are recorded in the PR.
Initial Node 26 Vitest run `35838512819` passed all 97 test files.

- PR Go CI uses `go-version-file: go.mod` (1.26.3), with separate build/vet/test and Race jobs. It does not verify the Go 1.27 container builder.
- PR web jobs use Node 26 but do not run `docker build`. The web Vitest and Playwright workflows cannot establish nginx/container startup correctness.
- `deploy-edge.yml` and `hive-ci-build.yml` build backend/web only through manual/deployment execution; the sidecar deployment builds the backend image on release/manual events. These require deployment infrastructure and are not PR gates.
- `loom-release.yml` builds web only on `v*` tag pushes. Recovery has no automated workflow build and consumes externally prepared binaries.
- Workflow run IDs and final conclusions belong in the PR. Known-red Playwright (`bahia-y22r0`, reported baseline 103 failing specs) is not passing acceptance evidence.
- Deploy-only acceptance is tracked in `bahia-8p0qf`: actual target architecture and image provenance; health/readiness, relay and database behavior, filesystem/socket permissions, DNS/TLS/CA/timezone behavior, and recovery CLI/Compose compatibility with production daemons. A recovery build cannot validate arbitrary externally supplied dynamic binaries against changed musl.

No image publication, deployment, merge, or push to master was performed. No application stubs, mocks, or production placeholders were introduced.
