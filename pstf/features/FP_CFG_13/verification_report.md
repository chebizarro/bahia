# FP_CFG_13 Verification

Implemented the six required Bahia configuration-fabric and relay-administration behaviors.

Evidence is provided by the named tests in `test_matrix.json` and the repository-wide `go build ./...`, `go vet ./...`, and `go test ./...` quality gates recorded at completion.

## Concurrent status publication (bahia-bvish)

The reported CI run 35817151495 was the first race job to complete, not a previously passing gate: normal tests failed before it through the pre-`21ccd1de` history, then the job timed out until `43be5be1` split race testing into a separate job.

### Diagnosis and fix

- `ConfigConsumer.Handle` releases its state mutex, signals activation, and publishes `accepted`; `processPending` separately publishes `applied` after releasing the same mutex. Concurrent publication is legitimate, and completion order is not guaranteed.
- The sole production binding is `relayConfigPublisher` in `internal/relaysidecar/server.go`. It has no mutable per-call state and delegates to Khatru `AddEvent`. The pinned `fiatjaf.com/nostr` revision `27e395a0f6e7` uses request-local events, the configured policy/store callbacks, and locked expiration tracking. Bahia's policy uses locked admin state; `sqliteStore` writes through `database/sql` with one write connection and an atomic replaceable-event UPSERT; `OnEventSaved` sends on channels. The production signer holds an immutable secret and signs separately owned events.
- The defect was the service test double's redundant shared `events` slice, not a missing production lock. Its post-`Record` append raced with the other publication, and the test could also read the slice before the activation publisher completed. The in-memory repository itself already locks `Record`.
- Removed that slice. Successfully recorded status events now travel through the completion channel; the end-to-end test waits for both `accepted` and `applied`, checks their desired-event IDs, and then verifies cleared drift. No production execution, test serialization, race exclusions, or skips were introduced. Both publisher and signer interfaces now state the concurrent-use requirement.

### Regression coverage (AC6, AC7)

`TestConfigConsumerPublishesStatusesConcurrently` uses a channel rendezvous that requires both `Handle` and the activation loop to enter publication before either can finish. It checks signed statuses and then delegates to the real production publisher, policy, and SQLite store, checking successful results, applied state, and persisted status. It does not infer publication order or retain a shared test slice. The timer is only a deadlock/failure bound, never a completion signal.

`TestConfigFabricPublishApplyStatusClearsDriftEndToEnd` verifies both completion events and the console's final applied version/drift result.

### Verification

Environment: Go 1.26.3, darwin/arm64, starting at `master` / `43be5be1`.

- Before the fix, `go test -race -run TestConfigFabric ./internal/service/ -count=50 -cpu=1,2,4` **failed** in 11.232s with `WARNING: DATA RACE` at `config_fabric_test.go:89`, including `runtime.growslice`, matching the supplied CI report.
- After the fix, the same command **passed** in 11.777s (50 repetitions for each of 1, 2, and 4 CPUs).
- `go test -race ./internal/relaysidecar -run TestConfigConsumerPublishesStatusesConcurrently -count=50 -cpu=1,2,4` **passed** in 11.334s (150 forced-overlap executions).
- `go build ./...` **passed** (21s wall time).
- `go vet ./...` **passed** (4s wall time).
- `go test ./...` **passed** (56s wall time; existing package caches permitted).
- `make race` **passed** (91s wall time), running `CGO_ENABLED=1 go test -race ./... -count=1` without exclusions.
- `git diff --check` and JSON validation of the updated criteria/matrix **passed**.

### Separate follow-up

`bahia-1antv` tracks source-observed replaceable-status precedence: accepted and applied use one coordinate and second-resolution timestamps, while SQLite replacement requires a strictly newer timestamp. Same-second accepted-first publication can therefore leave accepted stored instead of applied. This is not a Go data race and serializing publication alone would not solve it. The concurrency regression proves memory safety and storage of the coordinate, not terminal-status precedence; the service test proves drift clearing against its append-only repository, not the production replaceable store. This distinct behavior is not changed here.

Linux CI revalidation belongs to the user's subsequent push; no push is performed in this session.
