# Go modules wave 3 verification

Date: 2026-09-23. Base: `master` at `2dbfa863f1441692e399e707489fedbf1a8cdb53`.
Branch: `deps/go-modules-wave3`. Wave: `bahia-h9r4z`; IPv6 defect: `bahia-pxdnz`.

## Scope and invariants

Reapplied all **32 exact targets** from [Dependabot #29](https://github.com/chebizarro/bahia/pull/29) using one explicit-version `go get` against current master, then `go mod tidy`. No stale branch was cherry-picked. The Go directive remains `1.26.3`. Both `go.mod` and `go.sum` retain **`fiatjaf.com/nostr v0.0.0-20260916040958-27e395a0f6e7`** unchanged; the race/checkptr fix is not relaxed or bypassed.

Necessary compatibility additions beyond the requested group:

- `github.com/fvbommel/sortorder` **1.1.0 → 1.2.0**, matching Docker CLI 29.8.0's `vendor.mod`. CLI's `+incompatible` module does not propagate this requirement; the first build failed with `undefined: sortorder.NaturalCompare`.
- User-approved **test-only** `github.com/pashagolub/pgxmock/v4` 4.9.0 → `github.com/pashagolub/pgxmock/v5` 5.2.0. The old mock fails to implement pgx 5.11's `Rows.TypeMap`. All 29 existing test-file migrations change only the import path; existing expectations and assertions are retained.
- OTel log constructors moved to `attribute.StringValue` / `attribute.String`, preserving existing string-valued lifecycle attributes rather than changing telemetry semantics.
- Corrected pre-existing IPv6 host/port construction in database DSNs and server listener addresses. This is a Bahia bug, not a new pgx defect: pgx's stricter URI parser exposed malformed unbracketed IPv6 URLs previously tolerated by its old parser.

## Behavioral review and evidence

| Dependency | Upstream change reviewed | Bahia-specific check and residual risk |
| --- | --- | --- |
| pgx **5.7.4 → 5.11.0** | [Changelog](https://github.com/jackc/pgx/blob/v5.11.0/CHANGELOG.md): libpq-compatible URI/keyword parsing, `Rows.TypeMap`, JSON/array decoding, cached statements, pool expiry/ping, transaction error recovery, text timestamp location behavior. | New parser-backed DSN test covers IPv4, raw/bracketed IPv6 and escaped credentials. Counterfactual raw IPv6 failed with `invalid port`; corrected construction passes. Real PostgreSQL tests cover migrations, persistence, CAS, concurrent deduplication, rollback fencing, admission, approval and advisory-lock/pool behavior. No source use of simple-query mode or custom `ScanLocation` was found. Text-format timestamps now use client-local/explicit scan location; nonstandard connection strings and timezone-sensitive external consumers still warrant rollout attention. |
| pgxmock **4.9.0 → 5.2.0** | [v5.0](https://github.com/pashagolub/pgxmock/releases/tag/v5.0.0), [v5.2](https://github.com/pashagolub/pgxmock/releases/tag/v5.2.0): codec-based values, SQL NULL delivery, per-query cursors, `TypeMap`, query-option handling, concurrency fixes and optional closed-connection enforcement. | The same six negative controls pass against isolated v4 and the upgraded v5: wrong SQL, wrong arguments, unmet required expectations, unexpected operations, default ordering, and required row closure. The existing repository/auth suites pass without loosening any expectations. These checks supplement, not replace, real PostgreSQL evidence. |
| koanf **2.1.2 → 2.3.6**, env **1.0.0 → 1.1.0**, file **1.1.2 → 1.2.1** | [2.3.0](https://github.com/knadh/koanf/releases/tag/v2.3.0), [2.3.6](https://github.com/knadh/koanf/releases/tag/v2.3.6): mapstructure updates, thread safety and strict scalar/map merge handling. | Bahia still uses `koanf.New(".")`, YAML followed by environment overlay, then unmarshal onto defaults; it does not enable strict merging. Config tests cover environment precedence, nested runtime maps, durations, defaults and validation. No config-provider v2 migration is included. |
| CUE **0.11.0 → 0.17.1** | [0.13](https://github.com/cue-lang/cue/releases/tag/v0.13.0), [0.15](https://github.com/cue-lang/cue/releases/tag/v0.15.0), [0.17.1](https://github.com/cue-lang/cue/releases/tag/v0.17.1): evaluator/closedness transition, removal of the old evaluator and YAML implementation changes. | Ran actual embedded ML-recipe schema compilation, YAML extraction, concrete unification and JSON normalization: two valid recipe fixtures, required-field/step/retry rejection, normalized persistence. Seven test/subtest passes. Arbitrary user-authored recipes are not exhaustively proven by these fixtures. |
| Compose **5.3.1 → 5.5.1**, compose-go **2.13.0 → 2.15.0** | [Compose 5.4](https://github.com/docker/compose/releases/tag/v5.4.0), [5.5](https://github.com/docker/compose/releases/tag/v5.5.0), [5.5.1](https://github.com/docker/compose/releases/tag/v5.5.1), [compose-go 2.15](https://github.com/compose-spec/compose-go/releases/tag/v2.15.0): network/volume lifecycle planning, image-digest reconciliation, refresh-window pull policy, override/ulimit merging, optional dependencies and invalid pull-policy rejection. | Runtime/desired-state tests exercise real offline SDK project loading, overlays, service selection, desired-state rendering/hashing, pull-policy normalization and SDK option construction. **First `up` after upgrade may recreate existing containers as digests are re-evaluated.** Offline loader tests and recorded `Up` calls are not proof of a disruption-free live upgrade; no live deployment was touched. |
| Docker CLI **29.6.1 → 29.8.0**, connections **0.7.0 → 0.8.1** | [CLI comparison](https://github.com/docker/cli/compare/v29.6.1...v29.8.0): client initialization/error handling, registry authentication handling and Moby client/API updates; checked its vendor requirements. | SDK endpoint/TLS initialization and runtime tests pass. The missing sortorder requirement was fixed explicitly. No claim of live remote-daemon/TLS acceptance is made. |
| Syft **1.45.1 → 1.52.0** | [1.46](https://github.com/anchore/syft/releases/tag/v1.46.0), [1.48](https://github.com/anchore/syft/releases/tag/v1.48.0), [1.51](https://github.com/anchore/syft/releases/tag/v1.51.0), [1.52](https://github.com/anchore/syft/releases/tag/v1.52.0): added catalogers/SPDX 3 support, package identity/PURL/relationship fixes and removed phantom JavaScript package findings. | Real repository-fixture scans now assert **SPDX 2.3**, **CycloneDX 1.6**, and **left-pad 1.3.0** in both formats; attestation/tamper/parser tests pass. Bahia explicitly selects those encoder versions. Package counts/identities for other source ecosystems may legitimately change; byte-identical SBOM output is not promised. |
| btcec **2.3.4 → 2.5.0** | [Source comparison](https://github.com/btcsuite/btcd/compare/btcec/v2.3.4...btcec/v2.5.0): chainhash module move, additive low-S ECDSA helper, MuSig/ElligatorSwift changes, corrected Schnorr nonce documentation. | Ran the upstream Schnorr package's three tests under race plus Bahia's DSSE signature verification and tamper tests. Bahia's pinned Nostr implementation is unchanged. This is regression evidence, not a cryptographic audit. |
| OTel stable **1.44.0 → 1.46.0**, logs **0.20.0 → 0.22.0** | [1.45](https://github.com/open-telemetry/opentelemetry-go/releases/tag/v1.45.0), [1.46](https://github.com/open-telemetry/opentelemetry-go/releases/tag/v1.46.0): removed log value constructors, endpoint-path changes, retry timing, log flush/shutdown and attribute handling. | Lifecycle success/failure record regression checks preserve body, severity, outcome, bounded error type and string attributes. Real loopback HTTP exports verify all three `/v1/*` signal paths and idempotent shutdown. Bahia already appends explicit signal paths, so the new root-path default does not redirect exports. |
| etcd client **3.6.11 → 3.7.1**, SQLite **1.52.0 → 1.58.0**, remaining group | Reviewed [etcd upgrade notes](https://etcd.io/docs/v3.7/upgrades/upgrade_3_7/) and [SQLite OFD-locking contract](https://pkg.go.dev/modernc.org/sqlite#OFDLocking). | Only the etcd client is updated, not a server/data directory. SQLite OFD locking remains opt-in; relay-store/storage tests run in the full gates. No live etcd cluster upgrade or all-platform SQLite acceptance is claimed. Chi/testify/zap/x-crypto/x-sync/x-sys are covered by the full repo gates. |

## Local gate results

Go 1.26.3, Darwin/arm64. Package count: **88**. Counts distinguish top-level tests from subtests, and skips are not counted as passes.

| Gate | Result |
| --- | --- |
| `go build ./...` | Pass, all 88 packages in scope |
| `go vet ./...` | Pass |
| `go test ./...` | Pass |
| `go test -json -count=1 ./...` | 80 packages pass; 8 have no test files; **4,366 top-level passes + 2,540 subtest passes; 12 top-level skips; 0 failures** |
| `GOFLAGS=-json make race` | Same **4,366 + 2,540 passes, 12 skips, 80 tested packages, 8 no-test packages; 0 failures**; Makefile retains `CGO_ENABLED=1 go test -race ./... -count=1`; no checkptr suppression |
| PostgreSQL tagged integration | **22 top-level + 4 subtest passes**, 3 packages, **0 skips/failures** |
| PostgreSQL tagged integration with race | **22 top-level + 4 subtest passes**, 3 packages, **0 skips/failures** |
| Isolated pgxmock v4 negative controls / repository v5 negative controls | Each: 1 top-level + 6 subtest passes under race |
| Upstream btcec Schnorr | 3 tests pass under race |
| Syft strengthened format/content checks | 2 tests pass under race |

The initial compile failures (OTel constructors, pgxmock interface, missing sortorder) and the failing IPv6 regression were reproduced before their fixes. Final build/vet/test/cold-counted-test/race wall times were **31.49 / 2.05 / 6.35 / 86.90 / 102.58 seconds**. PostgreSQL normal/race gates took **98.70 / 256.79 seconds** including compilation. `go mod verify` passed; `go mod tidy -diff` produced zero bytes. A final automated assertion verified all 32 target versions, the exact Nostr pin, the unchanged Go directive and the v5-only mock dependency. Detailed local logs and final reruns: `/tmp/bahia-wave3-gates/`; initial dependency diagnostics: `/tmp/bahia-wave3-precompat.log` and `/tmp/bahia-wave3-gates/build.log`.

### PostgreSQL fixture and exact test invocation

Used a disposable **PostgreSQL 16.15 Alpine** container, image ID `sha256:cf78e76683b9ca8c5733cbbdce6c9262b45b6767934dd0a95e671f9a0fc20685`, published only at **127.0.0.1:61233**. No existing database was used. Waited for a successful TCP SQL query against the created database (the temporary initialization server is not sufficient readiness). The container and its anonymous volume were removed after both runs.

```sh
docker run -d --name bahia-wave3-postgres \
  -e POSTGRES_PASSWORD="$FIXTURE_PASSWORD" -e POSTGRES_DB=bahia_wave3 \
  -p 127.0.0.1::5432 postgres:16-alpine
# Obtain the loopback endpoint with docker port; wait for a successful TCP
# psql SELECT 1 against bahia_wave3 before invoking Go.
export BAHIA_VM_TEST_DATABASE_URL="postgres://postgres:$FIXTURE_PASSWORD@127.0.0.1:$FIXTURE_PORT/bahia_wave3?sslmode=disable"

go test -json -p 1 -tags=integration \
  ./internal/app ./internal/repository ./internal/service \
  -run '^(TestVirtualizationPostgres|TestVMControlPlane|TestPersistentVMPostgres|TestVMAdoption)' \
  -count=1 -timeout=240s
go test -json -p 1 -race -tags=integration \
  ./internal/app ./internal/repository ./internal/service \
  -run '^(TestVirtualizationPostgres|TestVMControlPlane|TestPersistentVMPostgres|TestVMAdoption)' \
  -count=1 -timeout=240s

docker rm -fv bahia-wave3-postgres
```

Packages are deliberately serialized (`-p 1`) to avoid concurrent `pgcrypto` extension creation. Tests create/drop their own schemas using the real migrations. The initial fixture attempt was removed after a readiness check raced database initialization; no test result from that attempt is counted.

## IPv6 construction audit

`bahia-pxdnz` covers the fixed DSN and listener bugs. The DSN regression includes raw IPv6, already-bracketed IPv6, IPv4 and credentials containing URI delimiters; listener tests additionally cover hostname and wildcard binding. Other matching host/port constructions were inspected rather than blindly replaced:

- **`bahia-fccqb`**: `RouteCanaryTarget.URL` / `ControlURL` leave IPv6 literals unbracketed, including the default-port path.
- **`bahia-ecjqa`**: public-route preview concatenates `Proxy.UpstreamHost` and port, unlike the canonical origin URL builder, which already uses `net.JoinHostPort`.
- **`bahia-4zdz0`**: Docker discovery creates ambiguous short-form port mappings for nonwildcard IPv6 bindings.

These pre-existing, separately tracked route/discovery defects are not bundled into the Go dependency wave. Existing LLM host/port and probe transport builders use `net.JoinHostPort`; colon-separated event identifiers, image references and container-port pairs are not socket addresses and were left alone.

## CI and acceptance boundary

GitHub Go CI's **Build, vet, and test** and **Race** jobs must both pass for the PR head; local success does not substitute for these jobs. Workflow run IDs and conclusions are recorded in the PR body after publication. The known-red Web Playwright suite (`bahia-y22r0`, reported baseline 103 failing specs) is neither positive nor negative acceptance evidence for these Go updates. No workflows are changed. No merge, master push, live deployment, or service database mutation is authorized or performed.

The independent Oracle review reported no blocker in the OTel constructor patch but received only that limited diff; it is not represented as a whole-wave review. The module graph, IPv6 changes, test migration and evidence were inspected directly in this session.

Context-filtering handoff: Jev `find-lines` / `filter-search` used 44 successful requests, 461,142 input tokens (about $0.01937), narrowing the integration invocation, config/telemetry entry points and upstream change review. Its low-confidence Docker CLI result was not treated as absence; source comparison exposed the sortorder requirement. A later HTTP 403 caused an explicit fallback to bounded source reads.
