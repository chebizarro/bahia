# Verification Report — bahia-pbjq

## Evidence

- `internal/controlplane.PolicyCommandPublisher` now publishes signed public Nostr events for `PolicyCreate` 5986, `PolicyUpdate` 5987, `PolicyDelete` 5988, and `PolicyEvaluate` 5989 and requires at least one relay OK acceptance before returning follow metadata.
- `internal/controlplane.ToolApprovalCommandPublisher` publishes signed `ToolApprovalResponse` 7977 events and requires relay OK acceptance.
- MCP policy mutation tools now delegate to the signer-first policy command publisher; policy list/get remain read-only projection queries.
- MCP tool provisioning approve/reject now delegate to the signer-first tool approval publisher; status remains read-only, and denylist administration is documented as a distinct admin path rather than approval semantics.
- CLI `bahia policies create` now uses the operator signer/control-plane path and returns publish/follow metadata instead of invoking REST policy mutation fallback.
- Documentation was updated in `docs/user-guide/cli-reference.md`, `docs/user-guide/mcp-tools.md`, and `docs/user-guide/features/policies.md`.

## Commands

```bash
go test ./internal/controlplane -run 'PolicyCommandPublisher|ToolApprovalCommandPublisher'
go test ./internal/mcp -run 'Policy|ToolApproval'
go test ./cmd/cli -run 'PolicyCreate|ParsePolicyRules'
go test ./internal/controlplane
go test ./internal/mcp
go test ./cmd/cli
```

Result: passed.

## Scope Guard

Deployment and artifact CLI/MCP mutation sections were not intentionally modified for this bead because concurrent bead `bahia-hygp` owns those surfaces. Existing unrelated working-tree changes in deployment/artifact files were left untouched.

## Remaining Work

No remaining work is tracked for `bahia-pbjq` after this verified slice.
