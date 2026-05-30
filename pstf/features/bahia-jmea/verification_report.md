# Verification Report: bahia-jmea

## Scope
Task 6 adds reconcile policy persistence semantics and scheduler selection for due observations while keeping reconciliation observe-only.

## Evidence
- `internal/domain/deployment_unit.go` defines `ReconcileMode` values and validates them.
- `internal/db/migrations/000038_deployment_units.up.sql` stores `deployment_units.reconcile_mode` with a value check.
- `internal/db/migrations/000039_environment_targeting.up.sql` stores `environments.targeting.default_reconcile_mode`.
- `internal/repository/pg_state.go` selects due non-disabled state rows for observation scheduling.
- `internal/reconcile/reconciler.go` resolves unit/environment reconcile policy, skips disabled policy, records observations, and does not call runtime remediation methods.
- `docs/deployment.md` documents policy semantics.

## Tests
Passed on 2026-05-30:

```bash
GOCACHE=/Users/bizarro/Documents/Projects/bahia/.gocache go test ./internal/reconcile ./internal/domain ./internal/repository
```

Initial execution without `GOCACHE` failed because the sandbox could not write to `/Users/bizarro/Library/Caches/go-build`; rerunning with a workspace-local Go build cache passed.
