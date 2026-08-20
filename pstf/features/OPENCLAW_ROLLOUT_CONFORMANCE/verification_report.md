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

Final integration verification at merge `e1f48e7498245d2ba26de5855af77333914c3e19`:

- readiness UX `a8985ea0` and rollout conformance `c543e4e1` are both ancestors
- `go build ./...`, `go test ./...`, and `go vet ./...` — passed
- focused readiness and saga race tests — passed
- full `internal/soulfactory` race tests with only the known third-party checkptr instrumentation disabled — passed
- broad race/checkptr run — blocked by the pre-existing `fiatjaf.com/nostr` Go 1.26 crash tracked as `bahia-openclaw-complete-agent-provisioning-20260819.3`
- web lint — 0 errors and 0 warnings; unit tests — 88 files / 675 tests passed; production build — passed

Track B must capture immutable deployed OCI digests, exercise the harness against isolated real dependencies, rehearse rollback, and independently verify incumbent reachability; Track A does not contact live systems.

Gallery gate runtime verification on branch `fix/soul-gallery-gate-node-runtime-20260820`:

- the gate prefers a server-side Node global `WebSocket` and falls back to the pinned `ws` package when the global is absent;
- `npm run check:soul-gallery-gate` validates startup without contacting a relay;
- `npm test` deterministically covers both global-present and global-absent startup paths;
- `.github/workflows/deploy-edge.yml` installs the pinned dependency and runs the startup preflight before any deployment mutation.
