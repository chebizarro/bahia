# Canonical runtime and DNS reconciliation

Normal daemon startup waits for the first relay catch-up before runtime
observation or DNS zone reconciliation. Service, environment, deployment-unit,
runtime state, artifact, deployment intent/run, worker, LLM, and ML inputs used
by these reconcilers are read from validated, service-authored 30900 events in
the local event store. PostgreSQL rows are an optional index: an absent,
divergent, or SQL-only row cannot authorize a runtime-state publication, DNS
endpoint, or zone sync. A tombstone in the local canonical snapshot removes
the corresponding projection even if the SQL index still has a live row.

Runtime observations and material state changes continue to publish through
the canonical outbox; unchanged observations do not re-sign after restart.
DNS configuration, policy, manual endpoints, and overrides remain in the local
control store, and backend sync plus endpoint publication retain their normal
idempotence behavior.

**Current safety hold:** `auto_apply` runtime remediation is suspended. The
legacy deployment lifecycle still resolves some deployment inputs through
PostgreSQL; using it from canonical reconciliation could apply a divergent SQL
desired row. Drift is still observed and published, but operators must use the
governed deployment intent path to remediate until a canonical-input lifecycle
adapter and its crash/restart tests land. `/health` reports this as a warning
under `runtime_reconciliation` (`auto_apply=suspended`); do not treat the
warning as evidence that auto-apply is functioning.

If relay catch-up does not complete, runtime and DNS side effects stay paused.
Troubleshoot relay quorum and catch-up status before investigating PostgreSQL;
restoring PostgreSQL alone does not release the canonical readiness barrier.
