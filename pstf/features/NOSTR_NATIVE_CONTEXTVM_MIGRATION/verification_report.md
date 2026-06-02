# Verification Report — NOSTR_NATIVE_CONTEXTVM_MIGRATION

## Status
In progress. Client surface and documentation are updated for ContextVM/canonical Nostr strategy; backend ContextVM mutation handlers remain a dependency.

## Evidence
- `web/src/lib/nostr/encrypted-controlplane.js` now builds encrypted ContextVM JSON-RPC 2.0 payloads and defaults request/result filters to kind `25910`.
- `web/src/lib/nostr/kinds.gen.js` exposes ContextVM (`25910`, `1059`, `21059`, `11316`-`11320`) and canonical read constants (`30900`, `4903`, `30315`, `30002`, `30078`), while labelling legacy ranges migration-only.
- `web/src/routes/workers/[pubkey]/+page.svelte` imports worker action constants instead of hardcoding `5997`-`6003`.
- Docs updated: `docs/user-guide/nostr-integration.md`, `docs/control-planes.md`, `docs/user-guide/mcp-tools.md`, `docs/user-guide/cli-reference.md`.
- `pkg/client/operator_nostr.go` documents the `bahia-viys` backend dependency for removing legacy operator request publication.

## Tests run
- PASS: `go test ./internal/nostrmigration ./internal/repository ./internal/app ./internal/controlplane ./internal/kinds ./internal/adapters/nostr ./internal/relaysidecar ./internal/mcp`.
- PASS: `npm run test:unit -- --run tests/unit/encrypted-controlplane.test.js` — 13 tests passed.
- PASS: static search for `5997|5998|5999|6000|6001|6002|6003` under `web/src/routes/workers` found no hardcoded worker action literals in touched paths.
- PASS: static search confirmed `web/src/lib/nostr/kinds.gen.js` exposes ContextVM/canonical constants.

## Remaining dependencies and gaps
- `bahia-viys`: implement backend ContextVM kind `25910` JSON-RPC handlers for service, worker, package, DNS, backup, ML, and adoption mutations, and align the generated kind-source path so `web/src/lib/nostr/kinds.gen.js` regeneration preserves ContextVM/canonical constants.
- `bahia-itrq`: keep docs/PSTF current as backend handlers replace legacy request-kind paths.
- Full removal of legacy backend subscriptions is not complete in this slice.
