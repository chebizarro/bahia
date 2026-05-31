# Verification report: bahia-9za4

## Evidence

- Split `web/src/lib/stores/controlplane.svelte.js` into a compatibility re-export plus focused modules under `web/src/lib/stores/controlplane/` and `web/src/lib/stores/collections/`.
- `events.svelte.js` keeps Nostr event routing/dedupe and delegates collection mutation to collection modules.
- `bootstrap.svelte.js` preserves subscription-based bootstrap behavior: scoped filters, immediate EVENT application, EOSE-gated live state, CLOSED handling, reconnect resubscription, and stale EOSE generation checks.
- Added module-instance cleanup for deterministic tests and HMR/module reloads.

## Commands run

```bash
wc -l web/src/lib/stores/controlplane.svelte.js web/src/lib/stores/controlplane/*.js web/src/lib/stores/controlplane/*.svelte.js web/src/lib/stores/collections/*.js web/src/lib/stores/collections/*.svelte.js
npm --prefix web run test:unit -- --run tests/unit/controlplane-store.test.js
npm --prefix web run build
```

## Results

- Line count gate passed: largest owned modules are `bootstrap.svelte.js` at 149 lines and `events.svelte.js` at 146 lines.
- Unit gate passed: 17/17 tests in `tests/unit/controlplane-store.test.js`.
- Build gate passed: `npm --prefix web run build` completed successfully. Svelte emitted existing warnings in unrelated files (`src/routes/policies/+page.svelte`, `src/lib/components/assistant/AssistantPlanApproval.svelte`, `src/routes/settings/+page.svelte`).

## Remaining work

No remaining bahia-9za4 work identified in the touched scope.
