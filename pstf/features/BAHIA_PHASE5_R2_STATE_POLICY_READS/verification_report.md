# Phase 5 R2 state/policy CLI reads

Issue: `bahia-irsry.13.5`. Worktree: `p5-r2-statepol`.

| Criterion | Evidence |
| --- | --- |
| Nostr default and REST parity | `TestStatePolicyReadsMatchRESTGolden` runs real CLI commands against a signed-event test relay and legacy HTTP server for state list/drifted and policies list/get, comparing table and JSON output. The default path makes no REST request; `--http-fallback` requests the old endpoints. |
| Drift semantics | The CLI selects only `DriftStatusDrifted`, matching `PgEnvironmentServiceStateRepository.ListDrifted`'s `drift_status = 'drifted'` predicate. The golden includes drifted and in-sync records. |
| Cursor and freshness | Golden test observes a nonzero `since` on later REQs. `TestStateReadStaleStoreWarnsAndSucceeds` verifies a no-EOSE read renders the persisted snapshot, warns on stderr, and returns no error (CLI exit 0). |
| Producer/decoder fidelity | `TestDecodeStateProducerRoundTrip` and `TestDecodePolicyProducerRoundTrip` use the production record builders, including tombstones and microsecond timestamps. `TestStateSyncRejectsTamperedEvent` verifies invalid signed content is not cached. |
| Public payload safety | The state producer omits free-form reconciliation failure metadata because it can carry arbitrary runtime error text; the state round-trip test checks that such text does not appear in public content. The typed desired snapshot, backoff time and failure count are projected. |

Full Go gate passed with `CGO_ENABLED=0`: `go build ./...`, `go vet ./...`, `go test ./...`, and `go test ./internal/archtest -count=1 -v -run TestNoNew`. The five `TestNoNew` ratchets all passed with zero new findings. `gofmt` and `git diff --check` passed.
