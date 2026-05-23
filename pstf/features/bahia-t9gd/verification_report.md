# Verification Report — bahia-t9gd

## Evidence

- Audited `32000-32010`; no Nostr event-kind use was found. The only in-scope match was JSON-RPC error code `-32000` in `internal/api/handlers/mcp.go`.
- Audited requested `31994-31997`; these are already occupied by backup read-model kinds (`KindBackupRetentionRegistry`, `KindBackupRecipeRegistry`, `KindBackupRunState`, `KindBackupVerificationState`), so worker read models were assigned to `32000-32003` after human confirmation.
- `go test ./internal/adapters/nostr ./internal/controlplane` passed in the live worktree.
- `npm test -- --run tests/unit/controlplane-store.test.js` passed in `web/`.

## Result

The touched Nostr kind registry, discovery compatibility map, backend compatibility helper, and web read-model consumer path satisfy the acceptance criteria for this focused slice.
