# Instance Health

**Instance Health** (`/instance-health`) shows current managed-runtime health, bounded recovery history, and maintenance overrides.

## Dashboard

The summary counts total, operational, attention, recovery-needed, recently recovered, and maintenance-suppressed targets. Search by target, host, service, or environment, and filter by:

- `healthy` or `running`
- `degraded`, `stopped`, or `unhealthy`
- `oom_killed` or `restart_loop`
- `unknown`
- `manual_override`

Each row shows restart counts, memory usage, observation time, last recovery attempt, and maintenance state. The detail panel shows recent health events and recovery attempts.

The page reads service-authored relay records through the browser store. Missing or stale observation is unknown.

## Maintenance override

Select an instance to set a reason and optional expiry. While the override is active, supervised recovery is suppressed for that target. Clear the override when maintenance finishes.

Maintenance changes use authenticated HTTP writes at:

```text
POST   /api/v1/services/{serviceId}/environments/{envId}/managed-instances/{deploymentUnitId}/maintenance
DELETE /api/v1/services/{serviceId}/environments/{envId}/managed-instances/{deploymentUnitId}/maintenance
```

They require the backing repositories, the service/environment organization permission, and a valid signer. A reason is required to enable maintenance.

## Investigation

1. Confirm the observation timestamp and runtime target.
2. Compare failure reason, restart counters, and memory.
3. Inspect recent recovery attempts.
4. Check the related deployment and service-state record.
5. Use a maintenance override only when automatic recovery would interfere with operator work.

## Related

- [Fleet Health](fleet-health.md)
- [Environment States](environment-states.md)
- [Services](services.md)
- [Deployments](deployments.md)
