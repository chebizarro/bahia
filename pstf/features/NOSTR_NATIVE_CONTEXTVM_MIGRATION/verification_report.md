# Verification Report — NOSTR_NATIVE_CONTEXTVM_MIGRATION

## Status
Complete. ContextVM/canonical Nostr migration streams A/B/C are complete and docs/PSTF have been finalized for the final runtime behavior.

## Evidence
- `bahia-dgju` CLOSED: CEP-4/NIP-59 random-key gift-wrap support for `1059`/`21059` around inner ContextVM `25910` was implemented in `internal/controlplane/encrypted_transport*`; focused transport tests passed.
- `bahia-f0uw` CLOSED: production runtime legacy kind reactor/subscriber support was removed; legacy custom kinds are isolated outside production runtime except startup migration/fixtures.
- `bahia-viys` CLOSED: web/CLI client cutover to ContextVM/canonical observables was completed.
- `web/src/lib/nostr/kinds.gen.js` exposes ContextVM (`25910`, `1059`, `21059`, `11316`-`11320`) and canonical observable constants (`30900`, `4903`, `30315`, `30316`, `30002`, `30078`), while labelling legacy ranges migration-only.
- Docs finalized: `docs/user-guide/nostr-integration.md`, `docs/control-planes.md`, `docs/user-guide/mcp-tools.md`, `docs/user-guide/cli-reference.md`, `docs/nostr-commands.md`, `docs/event-spec.md`, `docs/protocol-compatibility.md`, `docs/operator-assistant-protocol.md`, and `AGENTS.md`.

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
None identified for `NOSTR_NATIVE_CONTEXTVM_MIGRATION` after the `bahia-prcf` encrypted-result semantics pass.

## Encrypted result terminal semantics pass — bahia-prcf — 2026-06-07

Observed behavior before this pass: `web/src/lib/nostr/encrypted-controlplane.js` still exposed `ENCRYPTED_RESULT_TIMEOUT_MS`, scheduled a `setTimeout` inside `awaitEncryptedResult(...)`, and the focused unit test asserted timeout-based terminal failure when no correlated encrypted result arrived.

Intended behavior: encrypted ContextVM result waiting terminates from correlated result `EVENT` success/error, explicit relay `CLOSED`/AUTH lifecycle failure, publish failure cleanup, or caller-provided operation cancellation. Historical `EOSE` is handled as subscription lifecycle metadata and does not become terminal completion for an open realtime result subscription.

Changes verified:
- `awaitEncryptedResult(...)` no longer uses timeout or `setTimeout` for terminal result completion.
- The subscription handler set includes `onEvent`, `onEose`, and `onClosed`; tests inject those callbacks directly, including relay-less and unknown-relay `CLOSED` paths.
- Assistant encrypted request callers now pass caller-provided `AbortSignal` cancellation instead of long `timeoutMs` values.
- Already-aborted operation signals are rejected before relay connect/publish/subscribe work begins.

Verification:
- PASS: `npm run test:unit -- --run tests/unit/encrypted-controlplane.test.js` — 1 file / 16 tests passed.
- PASS: focused static search for `timeout`, `setTimeout`, `ENCRYPTED_RESULT_TIMEOUT_MS`, and `Timed out waiting for ContextVM` in `web/src/lib/nostr/encrypted-controlplane.js`, `web/tests/unit/encrypted-controlplane.test.js`, and encrypted assistant call sites returned no matches.

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

## Relay sidecar NIP-23 draft allow-list pass — bahia-h1tz — 2026-06-11

Observed behavior before this pass: the relay sidecar readable/publishable canonical-kind policy allowed NIP-23 long-form content (`30023`) but not the NIP-23 draft companion kind (`30024`). Operators need the sidecar allow-list to include both standard NIP-23 long-form event kinds.

Intended behavior: NIP-23 `LongFormContent=30023` and `LongFormDraft=30024` are standard Nostr kinds consumed directly where Bahia publishes/reads long-form documents or drafts. The sidecar should allow service-signed publishes and readable filters for both, while preserving rejection of unauthorized publishers and legacy runtime kinds.

Changes verified:
- `internal/kinds.IsCanonicalObservableKind` now includes `LongFormDraft` beside `LongFormContent`.
- `internal/relaysidecar` tests prove both NIP-23 kinds are accepted from the service pubkey, rejected from unauthorized pubkeys, readable via kind-scoped filters, and queryable from the sidecar store.

Verification:
- PASS: `GOCACHE=/tmp/bahia-go-build-cache GOMODCACHE=/tmp/bahia-go-mod-cache go test ./internal/kinds ./internal/relaysidecar -count=1`.

## Migration manifest standard-kind omission pass — bahia-8j5h — 2026-06-11

Observed behavior before this pass: `go test ./... -count=1` failed in `internal/nostrmigration` because `internal/kinds.LongFormDraft=30024` was neither mapped in the migration manifest nor explicitly justified. The focused rerun also exposed the companion NIP-23 constant `LongFormContent=30023` as uncovered.

Intended behavior: the migration manifest covers every `internal/kinds` constant either with a legacy-to-canonical disposition or an explicit omission/alias justification. Standard NIP-23 long-form content and draft events (`30023`, `30024`) are consumed directly when applicable and are not Bahia legacy control-plane/read-model inputs to rewrite.

Changes verified:
- `internal/nostrmigration/manifest.go` now documents explicit `standard` omissions for `LongFormContent` and `LongFormDraft`.
- `internal/nostrmigration/manifest_test.go` requires both NIP-23 constants in the requested omission/alias documentation test, while `TestKindConstantsAreMappedOrJustified` continues to parse all `internal/kinds` constants.

Verification:
- PASS: `GOCACHE=/tmp/bahia-go-build-cache GOMODCACHE=/tmp/bahia-go-mod-cache go test ./internal/nostrmigration -count=1`.
- PASS: `GOCACHE=/tmp/bahia-go-build-cache GOMODCACHE=/tmp/bahia-go-mod-cache go test ./... -count=1`.

## Work item D verification — bahia-6xxd.4

Docs/PSTF scope updated in this pass: `docs/control-planes.md`, `docs/user-guide/nostr-integration.md`, `docs/api.md`, `docs/deployment.md`, `docs/architecture.md`, `docs/designs/dns-orchestration-layer.md`, `docs/designs/nostr-native-system-discovery.md`, and `pstf/features/NOSTR_NATIVE_CONTEXTVM_MIGRATION/*`. Production documentation now describes ContextVM `25910` with CEP-4/NIP-59 wrappers (`1059`/`21059`) and canonical observables (`30900`, `4903`, `30315`, `11316`-`11320`, `30002`, `30078`, plus NIP-09 `5`). Legacy Bahia request/status/result/read-model/encrypted ranges are either removed from production instructions or explicitly marked historical/migration-only.

Sanity checks for this pass:

- PASS: static old-kind grep over target docs/PSTF returned only migration/historical-labelled occurrences.
- PASS: static production-contract grep found no stale instructions to publish old deploy request kinds, subscribe to old service-state kinds, or depend on legacy system-discovery wording in target docs.
- PASS: PSTF JSON artifacts parse with `python3 -m json.tool`.
- PASS: markdown link sanity check for target docs found no broken relative `.md` links.

## Documentation completion pass — bahia-8w7t

User/agent/operator documentation was updated to reflect the final ContextVM/canonical-kind contract and to document startup migration app usage:

- `docs/user-guide/nostr-integration.md`: added operator-facing migration section covering legacy inputs, canonical outputs, idempotency, relay backfill, signing, and failure handling.
- `docs/control-planes.md`: added runtime startup migration app section and clarified that migration failures must be fixed rather than reintroducing legacy subscribers.
- `docs/nostr-commands.md`: rewritten from the old `596x/696x/796x/3196x` production contract to ContextVM `25910`, CEP-4/NIP-59 wrappers, canonical observables, discovery, and migration-only legacy families.
- `docs/event-spec.md`: rewritten around production families (`25910`, `1059`/`21059`, `30900`, `30078`, `30315`, `4903`, `11316`-`11320`, `30002`, `5`) and startup migration behavior.
- `docs/protocol-compatibility.md`: replaced stale old-kind compatibility tables and removed an unresolved merge-conflict marker; now distinguishes ContextVM/canonical observables from external Loom/Hive-CI protocol interop.
- `docs/operator-assistant-protocol.md`: updated the assistant-safe catalog, receipts, signing model, and validation requirements to ContextVM methods and canonical observables.
- `AGENTS.md`: clarified that ad hoc RPC-over-Nostr remains forbidden while ContextVM `25910` is the approved mutation transport, and expanded required doc-maintenance scope for migration/kind changes.

Verification for this pass:

- PASS: static stale production-kind grep over updated user/agent/operator docs returned only migration/historical-labelled occurrences.
- PASS: static search confirmed no conflict markers remain in `docs/protocol-compatibility.md`.
- PASS: markdown link sanity check for updated docs found no broken relative `.md` links.

## Soul Factory ContextVM adapter checkpoint — fp-30 — 2026-07-19

Observed behavior before this checkpoint: Soul Factory provisioning and lifecycle mutations entered Bahia only through domain-specific request kinds even though ContextVM `25910` is the canonical mutation transport for new control-plane clients.

Implemented behavior:

- Registered `soul-factory/provision` and `soul-factory/action` on Bahia's existing verified ContextVM transport.
- Adapted accepted requests into the existing event-driven Soul Factory reactor through its established parsers and handlers; no REST, polling, or fake completion path was introduced.
- Preserved the original signed `25910` event id, author, and timestamp for lifecycle correlation.
- Returned an immediate acceptance acknowledgment only; existing correlated lifecycle events remain available during contraction.
- Projected ContextVM provisioning progress and terminal outcomes onto replaceable `30900` state at `soul-factory:provisioning:<request-event-id>` plus append-only `4903` audit facts, signed by the configured Soul Factory signer.
- Kept malformed params fail-closed before reactor dispatch.

Verification:

- PASS: `GOFLAGS=-buildvcs=false go test ./internal/soulfactory ./internal/controlplane ./internal/app -count=1` in the Go 1.26 build container.
- PASS: focused adapter/projection tests cover provisioning correlation, action tag projection, parser compatibility, malformed-param rejection, signed `30900`/`4903` publication, and direct-interop isolation.

Remaining `fp-30` work is explicitly staged: project lifecycle actions and authoritative Soul read models onto canonical `30900`/`4903`, migrate browser/CLI publishers and subscribers, contract direct request-kind ingress, and complete the max sidecar deployment proof.
