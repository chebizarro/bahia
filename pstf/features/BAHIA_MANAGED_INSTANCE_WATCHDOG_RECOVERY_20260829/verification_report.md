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
