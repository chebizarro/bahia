# Verification report — bahia-xere

## Evidence

- Removed DNS REST load paths from `web/src/routes/dns/+page.ts`; it now supplies relay URL and Bahia service pubkey configuration only.
- Replaced DNS REST collection loaders in `web/src/lib/stores/dns.svelte.js` with a long-lived Nostr subscription using scoped filters for kinds 31975, 31976, 31977, and 31978 plus `#t: bahia` and `authors`.
- `EVENT` callbacks parse JSON, validate required author/d-tag/type-tag shape, dedupe by event id, and respect parameterized replaceable latest-by-kind/pubkey/d-tag semantics including tombstones.
- `EOSE` updates loading/catch-up state; `CLOSED` and `AUTH` paths set visible connection errors.
- `web/src/routes/dns/+page.svelte` connects on mount, disconnects on unmount, removes the Refresh button, and filters locally over live state.

## Commands run

- `npm run test:unit -- tests/unit/dns-store-commands.test.js tests/unit/dns-store-subscriptions.test.js tests/unit/dns-controlplane.test.js` — passed, 15 tests.
- `npm run build` — passed. Existing unrelated Svelte warnings appeared in `src/routes/policies/+page.svelte` and `src/lib/components/assistant/AssistantPlanApproval.svelte`.
- RepoPrompt searches found no `/api/v1`, DNS REST fetch helper, `requestSeq`, Refresh, or `refreshTab` references in touched DNS dashboard files.

## Result

CRIT-3 and CRIT-4 are fixed for the owned DNS dashboard scope. No fake, stubbed, hardcoded production-path behavior was introduced in the touched DNS dashboard path.
