# Bahia SQL migration CLI verification

Issue: `bahia-cmiix`. Owner decision (2026-09-25): support rollback; retain all 80 `.down.sql` files. The hand-rolled runner remains authoritative and keys migrations by complete filename stem.

| Acceptance criterion | Evidence |
| --- | --- |
| `status` is read-only and returns nonzero with pending migrations | `TestMigrationStatusReadOnly` verifies no `schema_migrations` table is created; `TestStatusExitCodeAndNoWrites` verifies exit 2 with pending and 0 when current. |
| Up, status, and down share the existing advisory lock | All enter `withMigrationLock`; `TestMigrationDownSharesStartupLock` observes a blocked down on the same PostgreSQL lock and cancellation without mutation. Existing `TestMigrateConcurrentRunners` covers concurrent up. |
| Down requires confirmation and follows ordered full-stem history | `TestMigrationDownGuardedRoundTrips`, `TestMigrationDownRefusesOutOfOrderHistory`, and `TestAvailableMigrationsUseFullStem`. `--to` retains the named stem. |
| Missing SQL refuses before any rollback | `TestMigrationDownMissingAndAtomicFailure` removes a later plan script and verifies all version rows remain. |
| SQL and version deletion are atomic | `TestMigrationDownMissingAndAtomicFailure` makes down SQL create a table then fail; both DDL and version deletion roll back. |
| Guarded `000066` and `000070` round-trip | `TestMigrationDownGuardedRoundTrips` verifies schema and version state before/after down and re-up; a populated `000070` guard refuses. |
| All down scripts remain executable in order | `TestMigrationAllDownScripts` applied all 80, rolled all 80 down on an empty disposable PostgreSQL 16 database, then reapplied all 80. None were missing or unrunnable under those conditions. |
| Make target invokes the CLI, not the server | `make migrate MIGRATE_CONFIG=.migration-sanity.yaml` and `make migrate MIGRATE_CONFIG=.migration-sanity.yaml MIGRATE_ACTION=status` succeeded against disposable PostgreSQL 16. The temporary config was removed. |

Integration command: `BAHIA_MIGRATE_TEST_DATABASE_URL=<disposable PostgreSQL 16 DSN> go test -tags=integration -p 1 ./internal/db ./cmd/bahia-migrate -count=1` — passed. Packages use `-p 1` because `pgcrypto` creation can collide across fixtures. The same targeted packages passed under `-race -count=3`.

Repository gates: `go build ./...` passed; `go vet ./...` passed; `golangci-lint run --max-issues-per-linter=0 --max-same-issues=0` reported exactly the existing `handleStandbyNodeDefinition` unused finding. `go test ./...` did not complete: `internal/api/router` stalled for more than 11 minutes, then was stopped; all other packages reported passing. `make race` repeated the same router stall and was stopped after more than 2 minutes; all other packages reported passing. A separate `go test -v -timeout=30s ./internal/api/router` isolated the timeout to `TestRouterRateLimitDoesNotTrustForwardedClientIP` (`router_test.go:928`), outside the migration code. These full-suite gates are **not** acceptance passes.
