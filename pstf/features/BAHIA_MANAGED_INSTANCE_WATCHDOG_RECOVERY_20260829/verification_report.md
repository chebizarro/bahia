# Stage 4 verification report

Date: 2026-08-29

## Scope verified

- Tier 2, organization-scoped managed-instance health list/detail/history API.
- Authenticated service-operator maintenance set and clear API.
- Instance Health web page and REST client integration.
- Disposable Docker integration coverage for stopped, unhealthy, OOM-killed, exact-target restart, decoy isolation, and maintenance suppression.
- Migration, Signet edge-01 canary, relay fixture, acceptance, and rollback runbook.

## Evidence

- `go build ./...` — passed.
- `go test ./internal/api/... ./internal/service/...` — passed.
- `go vet ./...` — passed.
- `go vet -tags=integration ./test/integration/...` — passed.
- `go test -tags=integration ./test/integration/... -run '^TestManagedInstanceSupervisionDocker$' -v` — compiled and skipped as designed because `INTEGRATION_TEST=1` was not set.
- `npm run lint` — passed; `svelte-check` reported 0 errors and 0 warnings.
- Focused Vitest client, page-model, navigation, and route-access suite — 5 files and 31 tests passed.

The integration runtime remains opt-in to prevent unrequested Docker mutation. It registers unique disposable containers with `t.Cleanup` when `INTEGRATION_TEST=1` and Docker are available.

## Follow-up verification: bahia-l9fmb

- The instance-health collection now uses one bounded offset-paginated repository query that joins service and environment ownership and loads active maintenance overrides without per-row lookups.
- `TestInstanceHealthHandlerListPassesOrganizationFiltersAndCapsPagination` proves organization/filter propagation, the hard limit cap, pagination metadata, one repository call, and response sanitization.
- `TestInstanceHealthHandlerListUsesDefaultLimitAndNonNegativeOffset` proves the default limit and offset normalization.
- `TestPgManagedInstanceHealthRepositoryListHealthScopesBoundsAndLoadsOverride` proves the ownership joins, repository hard cap, and active-override scan in the collection query.
- Handler coverage also proves both event and recovery-attempt history limits are capped.
- `go build ./...` — passed.
- `go test ./internal/api/... ./internal/repository/... ./internal/service/...` — passed.
- The new `web/src/routes/instance-health/+page.svelte` is classified as an explicitly non-signer-first `rest_compatibility` surface in the Nostr audit route transport matrix and its `$lib/api/client.js` import is allowlisted with rationale.
- `npm run test:unit` — passed: 93 files and 702 tests.
