# Verification Report: REST policy/tool mutation deprecation

Feature: `bahia-3s39`

## Implementation evidence

- Removed six route registrations from `internal/api/router/router.go`:
  - `POST /api/v1/policies`
  - `PUT /api/v1/policies/{id}`
  - `DELETE /api/v1/policies/{id}`
  - `POST /api/v1/policies/evaluate`
  - `POST /api/v1/tools/{id}/approve`
  - `POST /api/v1/tools/{id}/reject`
- Deleted `PolicyHandler.Create`, `PolicyHandler.Update`, `PolicyHandler.Delete`, and `PolicyHandler.Evaluate`.
- Deleted `ToolHandler.ApproveIntent` and `ToolHandler.RejectIntent`.
- Preserved tool denylist keep-section routes: `POST /api/v1/tools/denylist` and `DELETE /api/v1/tools/denylist/{package}/{manager}`.
- Updated router tests to seed state through in-memory repos and assert removed routes for all six endpoints.
- Updated client `CreatePolicy` to return signer-first Nostr guidance without making a network call.
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

- `bahia-pbjq`: migrate policy/tool CLI and MCP mutation surfaces to signer-first Nostr publish/follow behavior.
