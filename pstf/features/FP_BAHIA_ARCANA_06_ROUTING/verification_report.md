# FP_BAHIA_ARCANA_06_ROUTING verification

## Implemented

- Six-step signed deployment wizard with a distinct public-route selection step and exact final route review.
- Provider-neutral signed route plan embedded in desired-state schema v4 and its canonical SHA-256 hash.
- Managed-zone ownership/protection, collision, port exposure/allowlist, upstream HTTP, and managed TLS validation.
- Cloudflare API adapter for remote Tunnel ingress and proxied CNAME DNS, with catch-all preservation and secret-safe errors.
- App-first apply, HTTPS health verification, signed DNS-before-tunnel provider compensation, and previous-application restoration on route failure. DNS compensation failure retains tunnel ingress and returns a retryable error instead of publishing a dead upstream.
- Rollback intents carry the superseded public-route plan so the rollback hash continues to describe the live public surface.
- `service/route-attach` creates a policy-checked intent from the current deployed desired state and executes a recoverable `runtime:route-only` run containing only the routing/HTTPS verification phase; runtime artifact convergence is not called.
- Follow-up second-provider adapter tracked by `bahia-5teth`.

## Verification

- PASS (2026-09-03): `go test ./internal/controlplane ./internal/workflow ./pkg/client ./cmd/cli`.
- PASS (2026-09-03): `go build ./...`.
- PASS (2026-09-03): `go test ./...`.

- PASS: `go test ./internal/domain ./internal/adapters/routing ./internal/service ./internal/controlplane ./internal/config ./internal/workflow ./internal/app`.
- PASS: review-fix rerun `go test ./internal/adapters/routing ./internal/service ./internal/workflow ./internal/app`.
- PASS: post-review regression `go test ./internal/adapters/routing ./internal/controlplane ./internal/workflow`.
- PASS: `go build ./...`.
- PASS: `cd web && npm run lint` (0 errors, 0 warnings).
- PASS: `cd web && npm run build`.
- PASS: `cd web && npm run test:unit -- --run tests/unit/public-controlplane.test.js` (20 tests).
- FULL-SUITE EXCEPTIONS: `go test ./...` reached an unrelated concurrent `internal/mcp` run-lifecycle fixture failure and the known `internal/soulfactory` failure tracked by `bahia-csxyx`; all routing and affected packages pass.
- LIVE PROVIDER NOTE: no production hostname or Cloudflare credential was supplied to this development session. The production adapter performs real Cloudflare API mutations and gates success on an HTTPS 2xx; mock-provider tests verify collision, ordering, TLS failure, and compensation semantics.
