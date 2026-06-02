# Verification Report: REST service/environment mutation deprecation

Feature: `bahia-c7s5`

## Implementation evidence

- Removed six route registrations from `internal/api/router/router.go`:
  - `POST /api/v1/services`
  - `PUT /api/v1/services/{id}`
  - `DELETE /api/v1/services/{id}`
  - `POST /api/v1/environments`
  - `PUT /api/v1/environments/{id}`
  - `DELETE /api/v1/environments/{id}`
- Deleted `ServiceHandler.Create`, `ServiceHandler.Update`, `ServiceHandler.Delete` from `internal/api/handlers/services.go`.
- Deleted `EnvironmentHandler.Create`, `EnvironmentHandler.Update`, `EnvironmentHandler.Delete` from `internal/api/handlers/environments.go`.
- Removed dead REST-only service repository and environment targeting helper functions/tests left behind by the handler deletion.
- Updated router/client tests so removed REST mutations are not used as setup and are asserted unavailable.
- Updated docs to direct service/environment mutations to Nostr command events.

## Automated verification

Commands:

```bash
go test ./internal/api/... ./pkg/client
go build ./...
```

Result: pass.

Packages passed:

- `github.com/openagentsinc/bahia/internal/api/dto`
- `github.com/openagentsinc/bahia/internal/api/handlers`
- `github.com/openagentsinc/bahia/internal/api/middleware`
- `github.com/openagentsinc/bahia/internal/api/router`
- `github.com/openagentsinc/bahia/pkg/client`

## Remaining work

- `bahia-9sbb`: migrate service/environment CLI and MCP mutations to signer-first Nostr or remove/deprecate those commands/tools.
