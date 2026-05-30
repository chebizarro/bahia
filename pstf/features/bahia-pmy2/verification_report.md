# Verification Report: bahia-pmy2

## Evidence
- Added migration `000041_adopted_runtime_identity.up.sql` with `adopted_runtime_identity.fingerprint` unique constraint and `org_ownership_repair` marking for unresolved ownership.
- Added `PgAdoptedRuntimeIdentityRepository` and wired it into adoption transactions.
- Added org resolution to adoption import: explicit `org_id`, existing environment org, or exactly one organization; multiple organizations fail closed.
- Docker discovery now carries `endpoint_ref` into discovered container responses; adopted runtime config stores `image_digest`.

## Tests Run
- `go test ./internal/service ./internal/repository ./internal/adapters/runtime` — PASS

## Remaining Work
- None identified in the touched scope.
