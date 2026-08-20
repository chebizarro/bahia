# OpenClaw rollout conformance verification

Task: **bahia-openclaw-rollout-conformance-20260819**

Track A adds a disposable, production-shaped saga conformance scenario using deterministic external-system doubles. It covers sequential and concurrent souls, isolated DM references, replay/conflict, reconstructed Bahia/Signet/runtime state, relay disconnect followed by durable retry/backfill behavior, and compensation at every forward stage.

Append-only sanitized failure history makes retry and repeated Signet policy-denial evidence restart-stable. The read-only monitor scans durable state without external contact and emits structured logs plus Prometheus text for build/instance/run/stage identity, age, progress, retry/reconciliation/rollback, readiness, DM gate, terminal projection, false-running, orphan, denial, and correlation evidence.

Repository alert rules and operator documentation cover the requested unsafe states and operations. The inventory explicitly separates repository-observed facts from Track B live discovery and protects Marjam/SNR as non-created incumbents.

Verification results:

- go build ./... — passed
- go test ./... — passed
- go vet ./internal/soulfactory/saga/... — passed
- Prometheus rule YAML parse — passed
- promtool rule evaluation — not run because promtool is not installed in this worktree environment

Track B must capture immutable deployed OCI digests, exercise the harness against isolated real dependencies, rehearse rollback, and independently verify incumbent reachability; Track A does not contact live systems.
