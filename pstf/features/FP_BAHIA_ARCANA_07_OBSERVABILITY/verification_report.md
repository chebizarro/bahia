# FP_BAHIA_ARCANA_07_OBSERVABILITY verification

## Implemented

- Coordinator-only execution for approved intents, persisted direct-runtime phases, durable active-run claims, and restart recovery for both non-terminal runs and approved intents whose run was not created before interruption.
- Health convergence with safe timeout/hash-mismatch classification; Compose observations read the applied desired-hash label and only healthy matching observations complete successfully as `in_sync`.
- Policy re-evaluation at approval and explicit prior-artifact rollback through a fresh protected-environment-aware intent.
- Safe intent/run/state projections with deployment target, digest, policy, phase, failure, health, observation, and environment-specific state coordinates.
- Deterministic browser merging across relay delay, reconnect, duplicate projection, corrected coordinates, and tombstones.
- One linkable deployment aggregate and run/log view with approval, runtime observation, drift, completion, and one-click health-failure rollback.
- Stored log redaction across retained referenced-secret versions before tailing or stream filtering.

## Verification

- `go test ./internal/service ./internal/controlplane ./internal/workflow ./internal/adapters/nostr ./internal/adapters/runtime ./internal/repository ./pkg/client ./cmd/cli` — PASS.
- `go test ./internal/mcp -run TestCallTool_RunLifecycle` — PASS.
- `go build ./...` — PASS.
- `npm run test:unit` in `web/` — PASS (80 files, 623 tests).
- `npm run lint` in `web/` — PASS (0 errors, 0 warnings).
- `npm run build` in `web/` — PASS.
- `go test ./...` — all deployment-observability and other packages pass; the suite remains non-green only because of the known unrelated `internal/soulfactory.TestOpenClawCommandDriverDefaultsToWrapperSupportedMethods` failure tracked by `bahia-csxyx`.
