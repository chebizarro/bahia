# Transactional zero-restart internal config verification

## Implemented behavior

- `SIGHUP` loads and validates a complete candidate before changing the active
  application.
- The Hive-CI mirror-read credential reference validates and swaps in place;
  unsupported deltas continue through the existing same-process application
  reconstruction path.
- Replacement construction occurs while the current application remains live.
  The current application then performs its graceful HTTP and background-runner
  drain before the replacement starts.
- Failed candidate load, validation, adapter reload, or application construction
  leaves the current runtime active.
- Active configuration publication and shutdown-time reads are synchronized.

## Automated evidence

- `go test ./cmd/server ./internal/app ./internal/adapters/gitea`
- `go test -race ./cmd/server ./internal/app ./internal/adapters/gitea`
- `go test ./...`
- `go vet ./...`

## Independent live acceptance still required

Production Bahia is intentionally stopped during implementation. An independent
reviewer must stage one low-risk mounted-config change through `SIGHUP`, prove
unchanged PID, healthy `/health` and `/ready`, completion of an in-flight
request, preserved durable state, and byte-identical rollback through the same
reload path. The reviewer must also apply an invalid candidate and prove the
prior runtime remains active.
