# Verification Report: BAHIA_WORKER_MANAGEMENT_BUCKET_A

## Observed behavior

- The Operations nav now labels `/ml` as `Inference`.
- The Inference page heading and deployment copy describe inference endpoints targeting Bahia's shared worker pool.
- The Workers list includes the required shared execution pool subtitle and uses `Task Type` for workload filtering.
- The Worker detail page renders fields from the current `domain.Worker` JSON shape: liveness status, queue/concurrency, last advertisement timestamp, ML placement capabilities, resources, accelerators, software, runtime target, pricing, preferred relays, and timestamps.
- The touched worker detail scope no longer reads stale pseudo-fields `price_per_sec`, `last_seen`, `worker.capabilities`, or `metadata`.

## Verification evidence

- `npm run test:unit -- --run tests/unit/workers-list-utils.test.js tests/unit/nav.test.js` passed: 2 files, 9 tests.
- `npm run build` passed. Existing warnings were emitted from `src/routes/policies/+page.svelte`, `src/lib/components/assistant/AssistantPlanApproval.svelte`, and `src/routes/settings/+page.svelte`; these are outside Bucket A.

## Boundaries observed

- No backend Go files were modified.
- No scheduling state badges or operator action menus were added.
- Concurrent backend areas (`internal/domain/worker.go`, `internal/repository/pg_worker.go`, `internal/db/migrations/`) were read only or untouched for implementation.
