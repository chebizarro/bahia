# Verification Report — NOSTR_NATIVE_CONTEXTVM_MIGRATION

## Status
Complete. ContextVM/canonical Nostr migration streams A/B/C are complete and docs/PSTF have been finalized for the final runtime behavior.

## Evidence
- `bahia-dgju` CLOSED: CEP-4/NIP-59 random-key gift-wrap support for `1059`/`21059` around inner ContextVM `25910` was implemented in `internal/controlplane/encrypted_transport*`; focused transport tests passed.
- `bahia-f0uw` CLOSED: production runtime legacy kind reactor/subscriber support was removed; legacy custom kinds are isolated outside production runtime except startup migration/fixtures.
- `bahia-viys` CLOSED: web/CLI client cutover to ContextVM/canonical observables was completed.
- `web/src/lib/nostr/kinds.gen.js` exposes ContextVM (`25910`, `1059`, `21059`, `11316`-`11320`) and canonical observable constants (`30900`, `4903`, `30315`, `30002`, `30078`), while labelling legacy ranges migration-only.
- Docs finalized: `docs/user-guide/nostr-integration.md`, `docs/control-planes.md`, `docs/user-guide/mcp-tools.md`, `docs/user-guide/cli-reference.md`.

## Tests run
- PASS (`bahia-dgju`): focused transport tests for CEP-4/NIP-59 random-key gift-wrap around inner ContextVM `25910`.
- PASS (`bahia-f0uw`): `go test ./internal/adapters/nostr ./internal/controlplane ./internal/kinds`.
- PASS (`bahia-viys`): `go test ./pkg/client ./cmd/cli` and targeted web unit tests.
- PASS (prior PSTF/client slice): `go test ./internal/nostrmigration ./internal/repository ./internal/app ./internal/controlplane ./internal/kinds ./internal/adapters/nostr ./internal/relaysidecar ./internal/mcp`.
- PASS (prior PSTF/client slice): `npm run test:unit -- --run tests/unit/encrypted-controlplane.test.js` — 13 tests passed.
- PASS (final integrated): `go test ./internal/nostrmigration ./internal/repository ./internal/app ./internal/controlplane ./internal/kinds ./internal/adapters/nostr ./internal/relaysidecar ./internal/mcp ./pkg/client ./cmd/cli`.
- PASS (final integrated): `npm run test:unit -- tests/unit/encrypted-controlplane.test.js tests/unit/public-controlplane.test.js tests/unit/dns-controlplane.test.js tests/unit/workers-actions.test.js tests/unit/nostr-client-parsing.test.js` — 5 files / 70 tests passed.
- PASS: static search for `5997|5998|5999|6000|6001|6002|6003` under `web/src/routes/workers` found no hardcoded worker action literals in touched paths.
- PASS: static search confirmed `web/src/lib/nostr/kinds.gen.js` exposes ContextVM/canonical constants.

## Remaining dependencies and gaps
None identified for `NOSTR_NATIVE_CONTEXTVM_MIGRATION` after the `bahia-6xxd` fixer pass.

## Legacy-kind fixer pass — bahia-6xxd

Closed fixer Beads:
- `bahia-6xxd.1`: backend runtime legacy kind cleanup.
- `bahia-6xxd.2`: web runtime legacy kind cleanup.
- `bahia-6xxd.3`: manifest alias/coverage repair.
- `bahia-6xxd.4`: docs rewrite.
- `bahia-6xxd`: fixer epic.

Final integrated verification after the fixer pass:
- PASS: `go test ./internal/nostrmigration ./internal/repository ./internal/app ./internal/controlplane ./internal/kinds ./internal/adapters/nostr ./internal/relaysidecar ./internal/mcp ./internal/service ./pkg/client ./cmd/cli`.
- PASS: `npm run test:unit -- --run tests/unit/llm-page.test.js tests/unit/controlplane-store.test.js tests/unit/fips-mesh-store.test.js tests/unit/dns-store-subscriptions.test.js tests/unit/dns-store-commands.test.js tests/unit/nostr-client-parsing.test.js tests/unit/encrypted-controlplane.test.js tests/unit/public-controlplane.test.js tests/unit/dns-controlplane.test.js tests/unit/workers-actions.test.js tests/unit/assistant/assistant-store.test.js` — 11 files / 106 tests passed.

## Work item D verification — bahia-6xxd.4

Docs/PSTF scope updated in this pass: `docs/control-planes.md`, `docs/user-guide/nostr-integration.md`, `docs/api.md`, `docs/deployment.md`, `docs/architecture.md`, `docs/designs/dns-orchestration-layer.md`, `docs/designs/nostr-native-system-discovery.md`, and `pstf/features/NOSTR_NATIVE_CONTEXTVM_MIGRATION/*`. Production documentation now describes ContextVM `25910` with CEP-4/NIP-59 wrappers (`1059`/`21059`) and canonical observables (`30900`, `4903`, `30315`, `11316`-`11320`, `30002`, `30078`, plus NIP-09 `5`). Legacy Bahia request/status/result/read-model/encrypted ranges are either removed from production instructions or explicitly marked historical/migration-only.

Sanity checks for this pass:

- PASS: static old-kind grep over target docs/PSTF returned only migration/historical-labelled occurrences.
- PASS: static production-contract grep found no stale instructions to publish old deploy request kinds, subscribe to old service-state kinds, or depend on legacy system-discovery wording in target docs.
- PASS: PSTF JSON artifacts parse with `python3 -m json.tool`.
- PASS: markdown link sanity check for target docs found no broken relative `.md` links.
