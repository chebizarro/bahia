# bahia-dskm Verification Report

## Evidence

- `internal/reconcile/reconciler.go` marks `approval_required` drift as `remediation_needed` and calls `AutoRemediateDesiredState` only for `auto_apply`.
- `internal/service/runtime_lifecycle.go` exposes `AutoRemediateDesiredState`, which uses the existing desired-state deploy helper with non-blocking lock acquisition.
- `internal/service/runtime_apply_lock.go` implements `TryLock` with PostgreSQL `pg_try_advisory_lock` using the same advisory key as blocking deploy/apply locks.
- `environment_service_state` persists `reconcile_failure_metadata`, `reconcile_backoff_until`, and `reconcile_consecutive_failures` via migration `000042`.
- `docs/deployment.md` documents observe_only, approval_required, auto_apply, lock contention, and failure/backoff behavior.

## Verification commands

Completed 2026-05-30:

```sh
go test ./internal/reconcile ./internal/service
```

Result:

```text
ok  github.com/openagentsinc/bahia/internal/reconcile
ok  github.com/openagentsinc/bahia/internal/service
```
