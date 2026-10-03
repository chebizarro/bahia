# Web store-first core views — W2-S1 verification

Issue: `bahia-irsry.12.4`. Design: `docs/designs/phase4-web-store-first.md` §§2, 7, 8, 12–14.

## Intended behavior and evidence

| Acceptance | Production path | Verification |
| --- | --- | --- |
| Core list/detail navigation uses the persisted event store without a page-level load gate | `collections/core-query.js`; service, environment, and deployment collection bindings; core routes | `web/tests/e2e/core-store-views.spec.js` navigation test; `web/tests/unit/store-first-services.test.js` hydration cases |
| Live canonical 30900 changes project within one animation frame by `t` topic | `BahiaEventStore.subscribe` → coordinate index → RAF-flushed Svelte collection | `web/tests/e2e/core-store-views.spec.js` live tests for services, environments, policies; unit tests for all seven core topics |
| Kind-5 deletion removes an entity and prevents stale resurrection | `store.js` NIP-09 tombstone indexes; `core-query.js` `a` coordinate cutoff and exact `e` ID | `web/tests/unit/bahia-event-store.test.js` deletion/late-event/reopen cases; `web/tests/unit/store-first-services.test.js` tombstone cases; Playwright live-delete tests |

The core domains no longer have applicators in `controlplane/events.svelte.js`, `loadX()` aliases in `stores/index.svelte.js`, or page-level collection loading gates. The shared router and legacy cache machinery remain for unmigrated domains, as required by the Wave 2 shared-file rule. REST/ContextVM mutation handlers were not migrated in this slice.

## Gates

- `cd web && pnpm install --frozen-lockfile`: passed.
- `cd web && pnpm run test:unit`: passed, 1050 tests / 1 skipped (118 test files passed, 1 skipped).
- `cd web && pnpm run lint`: passed, 0 errors / 0 warnings.
- `cd web && pnpm run build`: passed (third-party Rollup annotation and existing chunk-size warnings only).
- `CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...`: passed.
- `CGO_ENABLED=0 go test ./internal/archtest -count=1 -v -run TestNoNew`: passed, 0 new violations.
- `CGO_ENABLED=0 CI=1 npx playwright test`: passed, 201 tests / 4 skipped. Focused core-view (4/4) and CRUD regression (26/26) runs also passed.

Issue tracker state was not changed: this slice forbids writes to `.beads/`. The branch commit was not pushed, per the slice instructions.
