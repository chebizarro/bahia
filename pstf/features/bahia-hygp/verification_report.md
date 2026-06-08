# Verification Report: bahia-hygp

Date: 2026-06-08

## Verified behavior

- MCP `bahia_deploy` / `bahia_create_intent` now require `ServiceCommandPublisher` and return signer-first ContextVM correlation receipts.
- MCP `bahia_rollback` now requires `ServiceCommandPublisher` and returns a signer-first ContextVM correlation receipt.
- MCP `bahia_approve_deployment` / `bahia_reject_deployment` and aliases now publish approval/rejection requests through `ServiceCommandPublisher`.
- MCP `bahia_register_artifact` now requires `ArtifactCommandPublisher` and returns a signed kind `5985` receipt instead of directly registering in `RegistryService`.
- CLI `bahia deployments deploy` and `bahia deployments rollback` now use the operator Nostr client rather than the removed REST client methods.

## Quality gates run

Passed:

```bash
go test ./pkg/client ./internal/mcp ./internal/controlplane ./internal/app
```

Passed:

```bash
go test ./cmd/cli ./pkg/client ./internal/mcp ./internal/controlplane ./internal/app
```

## Remaining work

- No remaining `bahia-hygp` work is known from the verified focused scope.
- If sibling work changes shared files again (`cmd/cli/main.go`, `cmd/cli/operator_nostr.go`, `internal/mcp/server.go`), rerun the focused package set before committing.
