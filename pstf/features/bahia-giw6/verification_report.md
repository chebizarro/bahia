# bahia-giw6 verification report

## Evidence

- `web/src/lib/nostr/continuity.ts` queries scoped Nostr filters for continuity kind 30351/30353 status/progress, 31400-31404 definitions, 30315 continuity heartbeat, and 30900 worker state.
- `web/src/routes/continuity/+page.ts` calls `loadContinuityDashboardFromNostr()` and disables SSR so the browser relay client is the transport.
- `web/src/routes/continuity/SimulationPanel.svelte` derives worker-failure impact from the loaded Nostr event set instead of POSTing to REST.
- `internal/api/router/router.go` no longer mounts `/api/continuity/status`, `/api/continuity/topology`, or `/api/continuity/simulate`.
- `internal/service/continuity_status_projector.go` and `internal/adapters/nostr/catalog.go` remain the backend Nostr serving/decoding path for continuity read-model kinds.

## Verification commands

- `cd web && npm run test:unit -- --run tests/unit/continuity-nostr.test.ts tests/unit/route-transport-matrix.test.js` — passed, 2 files / 8 tests.
- `go test ./internal/api/router -run 'TestContinuityRESTRoutesAreRemoved'` — passed.
- `go test ./internal/api/router ./internal/api/handlers ./internal/app` — passed.
- `cd web && npm run build` — passed. Existing warnings remain in `src/routes/policies/+page.svelte` and `src/lib/components/assistant/AssistantPlanApproval.svelte`; no continuity build errors.
