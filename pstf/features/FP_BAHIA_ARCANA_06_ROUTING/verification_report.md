# FP_BAHIA_ARCANA_06_ROUTING verification

## Implemented

- Six-step signed deployment wizard with a distinct public-route selection step and exact final route review.
- Provider-neutral signed route plan embedded in desired-state schema v4 and its canonical SHA-256 hash.
- Managed-zone ownership/protection, collision, port exposure/allowlist, upstream HTTP, and managed TLS validation.
- Cloudflare API adapter for remote Tunnel ingress and proxied CNAME DNS, with catch-all preservation and secret-safe errors.
- App-first apply, HTTPS health verification, provider compensation, and previous-application restoration on route failure.
- Follow-up second-provider adapter tracked by `bahia-5teth`.

## Verification

- PASS: `go test ./internal/domain ./internal/adapters/routing ./internal/service ./internal/controlplane ./internal/config ./internal/workflow ./internal/app`.
- PASS: review-fix rerun `go test ./internal/adapters/routing ./internal/service ./internal/workflow ./internal/app`.
- PASS: `go build ./...`.
- PASS: `cd web && npm run lint` (0 errors, 0 warnings).
- PASS: `cd web && npm run build`.
- PASS: `cd web && npm run test:unit -- --run tests/unit/public-controlplane.test.js` (20 tests).
- FULL-SUITE EXCEPTIONS: `go test ./...` reached an unrelated concurrent `internal/mcp` run-lifecycle fixture failure and the known `internal/soulfactory` failure tracked by `bahia-csxyx`; all routing and affected packages pass.
- LIVE PROVIDER NOTE: no production hostname or Cloudflare credential was supplied to this development session. The production adapter performs real Cloudflare API mutations and gates success on an HTTPS 2xx; mock-provider tests verify collision, ordering, TLS failure, and compensation semantics.
