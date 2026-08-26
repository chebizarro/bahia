# Verification: bahia-77j93

## Implemented

- Added trusted kind `31953` reactor subscription and serialized, bounded fleet reconciliation.
- Added active OpenClaw kind `31951` discovery, applied-revision persistence, and replay skipping.
- Added per-soul `soulfactory.config.reload` dispatch with distinct apply/rollback idempotency keys.
- Added soul-scoped kind `6950` progress and kind `7950` terminal evidence carrying fleet revision and status.
- Extended the OpenClaw reload bridge to rebuild the effective fleet configuration without writing Compose or restarting.

## Evidence

- `GOCACHE=/tmp/bahia-go-cache go build ./...` passes.
- `GOCACHE=/tmp/bahia-go-cache go test ./internal/soulfactory/...` passes.
- Focused fan-out, skip-already-applied, failure/rollback, subscription-filter, and wrapper fleet-reload tests pass.
