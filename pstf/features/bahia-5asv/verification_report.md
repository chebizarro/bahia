# Verification Report — bahia-5asv

## Verification run

- `npm run test:unit -- --run tests/unit/ml-page-model.test.js tests/unit/controlplane-store.test.js`
  - Result: passed; 2 files, 19 tests.
- `npm run build`
  - Result: passed.
  - Notes: build emitted unrelated existing Svelte warnings in `src/routes/policies/+page.svelte` and `src/lib/components/assistant/AssistantPlanApproval.svelte`; no warnings from the touched worker placement scope.

## Evidence mapped to acceptance criteria

- AC1/AC3: `web/tests/unit/ml-page-model.test.js` verifies deploy placement payload includes `pinned_worker`, `label_selector`, `worker_selector`, and rollout labels.
- AC2: `web/tests/unit/ml-page-model.test.js` verifies eligible/rejected worker preview and selected winner behavior.
- AC4: `web/src/routes/ml/+page.svelte` publishes `workload.pin.request` via `publishCommand()` for existing endpoint pins and otherwise carries the pin in the deploy placement policy.
- AC5: `web/src/routes/environments/[id]/+page.svelte` publishes `worker-policy.apply.request` via `publishCommand()` for environment placement policy edits.
- AC6: `web/tests/unit/controlplane-store.test.js` verifies worker state and eligibility preview read-model events are accepted into frontend stores.
