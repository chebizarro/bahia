# OpenClaw Provisioning Saga Verification

Task: `bahia-openclaw-transaction-reconciliation-20260819`

The saga package persists cross-process-locked, compare-and-swap JSON checkpoints with mode 0600 files in a mode 0700 state directory. Stage and compensation idempotency keys derive from the immutable Soul Factory request/run identity. Persisted external identifiers are one-way public references. Current-version writes cannot rewrite run identity or append-only lineage.

Every driver mutation is bracketed by inspection. Ambiguous responses are refetched and correlated. Matching reality must have the requested spec and request correlation. Compensation inspects each individual resource and follows Signet policy/binding → transient credential → runtime account/route → container → projection order; only resources created by the same run are eligible.

The terminal driver must inspect and correlate both kind 7950 provisioning-result lineage and kind 31951 agent-soul lineage with the authoritative running, rolled-back, or failed-terminal checkpoint. Terminal reconciliation also uses inspect-before-publish and refetch-after-publish behavior.

Deterministic tests cover exact replay, response loss, wrong-spec conflicts, multi-resource rollback, adopted-resource preservation, stale and current-version rewrite rejection, retry and process restart, concurrent isolation, every-forward-stage injected failure, dry-run commands, secret-free reports, correlated terminal state, and terminal retention; recoverable ownership lineage remains until reconcile or safe-abort.

Verification commands:

- `go build ./...` — passed
- `go test ./...` — passed
- `golangci-lint run ./internal/soulfactory/saga/...` — passed
- `golangci-lint run ./...` — reports 154 pre-existing findings outside this feature package
