# Verification Report: REST deployment/observation/artifact mutation deprecation

Feature: `bahia-wwnd`

## Implementation evidence

- Removed six route registrations from `internal/api/router/router.go`:
  - `POST /api/v1/deployments/intents`
  - `POST /api/v1/deployments/intents/{id}/approve`
  - `POST /api/v1/deployments/intents/{id}/reject`
  - `POST /api/v1/rollback`
  - `POST /api/v1/observations`
  - `POST /api/v1/artifacts`
- Deleted the REST mutation handler methods from `internal/api/handlers/deployments.go`, `internal/api/handlers/state.go`, and `internal/api/handlers/artifacts.go`.
- Updated router tests to seed setup state through `RegistryService` instead of removed REST mutation endpoints.
- Added route-removal assertions covering all six endpoints.
- Updated client methods for deployment intent creation and rollback to fail before network calls with signer-first Nostr guidance.
- Updated user docs and the REST deprecation checklist.

## Automated verification

Commands:

```bash
go test ./internal/api/... ./pkg/client
go build ./...
```

Result: pass.

Packages passed in the focused test run:

- `github.com/openagentsinc/bahia/internal/api/dto`
- `github.com/openagentsinc/bahia/internal/api/handlers`
- `github.com/openagentsinc/bahia/internal/api/middleware`
- `github.com/openagentsinc/bahia/internal/api/router`
- `github.com/openagentsinc/bahia/pkg/client`

## Remaining work

- `bahia-hygp`: migrate deployment/artifact CLI and MCP mutations to signer-first Nostr publish/follow behavior.
