# bahia-ilio HITL Decisions

## 2026-06-29 — Option A asynchronous acknowledgment

Decision owner: chebizarro.

Decision: Convert `sbom/generate` and `sbom/import` ContextVM handlers to asynchronous acceptance acknowledgments. The handler response must include the accepted idempotency/status coordinate and must not wait for `30315`, `4903`, `30078`, or `30004` publication. Terminal truth remains canonical Nostr observables: progress via `30315`/`4903`, completion through `30078` SBOM references and `30004` availability lists.

Implementation evidence:

- `internal/controlplane/sbom_handlers.go` now calls `EnqueueGenerate` / `EnqueueImport` and returns `service.SBOMAcceptedAck`.
- `internal/service/sbom_async_runner.go` defines the managed channel-driven runner and accepted acknowledgment shape.
- `internal/app/app.go` registers the runner with `BackgroundManager` and wires the handlers to the runner.
- Deterministic tests inject OK, CLOSED, and AUTH outcomes without sleeps.
