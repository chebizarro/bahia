# Managed-instance supervision

## Purpose

Bahia observes explicitly managed runtime targets, persists current health and sanitized transition history, and can perform policy-bounded restarts of the exact target. It does not rebuild images or change desired configuration. Current state and recent recovery evidence are available in the **Instance Health** UI and the Tier 2 REST query surface; maintenance changes require an authenticated operator with service-write permission.

Supervision recognizes `healthy`, `running`, `degraded`, `stopped`, `unhealthy`, `oom_killed`, `restart_loop`, `unknown`, and `manual_override`. Health and recovery facts are projected as sanitized Nostr `30315`, `30900`, and `4903` events.

## Database migration

Apply the managed-instance migrations before enabling the feature:

- `000055_managed_instance_health` creates current health, append-only health events, recovery attempts, and maintenance overrides.
- `000056_managed_instance_recovery_pending` adds the durable pending recovery-claim state used for idempotent restart completion.

Verify both migrations succeeded before starting an observe-only canary. Normal recovery rollback leaves these additive tables in place so observation and audit evidence remain available; do not run the destructive down migrations as part of an operational rollback.

## Configuration

Supervision is disabled and observe-only by default:

```yaml
supervision:
  enabled: false
  observe_only: true
  interval: 30s
  memory_threshold: 0.90
  instances: []
```

A canary target is explicit and exact:

```yaml
supervision:
  enabled: true
  observe_only: true
  interval: 30s
  memory_threshold: 0.90
  instances:
    - service_id: "<service UUID>"
      environment_id: "<environment UUID>"
      deployment_unit_id: "<deployment unit UUID>"
      runtime_target_name: "<container, compose service, or systemd unit>"
      host: "edge-01"
      supervisor_type: "docker"
      desired_running: true
      docker_host: "unix:///var/run/docker.sock"
      restart_max_attempts: 3
      restart_window: 1h
      backoff_base: 1m
      backoff_cap: 10m
      warning_min_interval: 15m
```

For Compose use `supervisor_type: compose` and set `compose_dir`. For system services use `systemd` or `user-systemd`. Configure `probe_url` and `probe_timeout` only when a readiness probe is required.

### Budgets and alerts

- `restart_max_attempts` and `restart_window` cap restarts for one target.
- `backoff_base` and `backoff_cap` prevent rapid retry loops.
- `warning_min_interval` rate-limits warning notifications; error and critical recovery events remain immediate according to the built-in supervision alert policy.
- `memory_threshold` marks sustained memory pressure as degraded after consecutive observations.
- A maintenance override suppresses recovery for only its full service/environment/deployment-unit/runtime-target key. Observation and alerting remain active.

Do not enable recovery with missing or zero budgets. Configuration validation rejects incomplete enabled targets and invalid recovery settings.

## Operator API

The UI uses these Tier 2 endpoints:

- `GET /api/v1/instance-health` with optional `service_id`, `environment_id`, and `unhealthy` filters.
- `GET /api/v1/services/{serviceId}/environments/{envId}/managed-instances/{deploymentUnitId}/health?runtime_target_name=...`.
- The same health path with `/events` or `/recovery-attempts`, optionally with bounded `limit`.
- `POST` and `DELETE` on `/api/v1/services/{serviceId}/environments/{envId}/managed-instances/{deploymentUnitId}/maintenance?runtime_target_name=...`.

All reads are organization-scoped through both service and environment ownership. Maintenance writes require an authenticated service operator; the actor comes from the authenticated principal rather than the request body.

## Signet edge-01 first-target migration

Use Signet `edge-01` as the first target. The existing bespoke watchdog under `/home/agent/.local/state/signet-watchdog` remains the recovery authority during the comparison canary.

1. Record the exact service, environment, deployment-unit, and runtime-target identity used by both systems.
2. Deploy Bahia with supervision enabled for only this target and `observe_only: true`.
3. Keep the bespoke watchdog enabled as the fallback and sole restart authority.
4. Compare Bahia observations with the watchdog records for stopped state, failed health probes, OOM-like exits, restart counts, memory pressure, and recovery timestamps.
5. Confirm Bahia evidence is sanitized, the target identity is exact, and `30315`, `30900`, and `4903` projections agree with the persisted history.
6. Exercise a Bahia maintenance override and confirm observation continues while recovery remains suppressed.
7. Continue the canary until observations match through representative healthy and failure intervals. Investigate every mismatch before proceeding.
8. Set `observe_only: false` only after independent operator acceptance of the comparison evidence. Keep the bespoke watchdog available as fallback during the initial Bahia recovery window, but prevent both systems from restarting simultaneously by placing the fallback in a non-active/standby posture.
9. Prove exact-target recovery with a labelled decoy target and verify the decoy start time and identity do not change.
10. Verify restart budgets, backoff, alerts, and maintenance suppression under recovery-enabled operation.
11. After independent acceptance of Bahia recovery evidence, disable the bespoke Signet watchdog. Preserve its state directory for rollback evidence until the rollout is accepted.

## Prior relay watchdog second fixture

After Signet acceptance, repeat the same observe-only comparison against the prior relay watchdog deployment as the second fixture:

1. Inventory its exact runtime target, restart conditions, restart budget, and evidence location.
2. Add only that relay target to Bahia while keeping Bahia observe-only for it.
3. Keep the prior relay watchdog active as fallback and recovery authority during comparison.
4. Compare stopped, unhealthy, OOM-like, memory, restart, and recovery observations, including sanitized Nostr projections.
5. Test maintenance suppression and exact-target selection with a decoy.
6. Enable Bahia recovery only after the fixture observations match and an independent operator accepts the evidence.
7. Move the relay watchdog to standby during the Bahia recovery canary, then disable it after acceptance while retaining its evidence for rollback.

Do not expand to additional targets until both fixtures meet the same acceptance criteria.

## Rollback

Rollback recovery without losing observation or alerting:

1. Set `supervision.observe_only: true` and restart Bahia. This immediately disables Bahia restarts while current health, transition persistence, projections, and alerts continue.
2. If the supervisor itself is causing operational load, set `supervision.enabled: false`; retain the health tables and projected audit events.
3. Re-enable the accepted bespoke watchdog for the affected fixture as the recovery authority, ensuring only one system can restart the target.
4. Confirm no pending Bahia recovery attempt is active and verify the target and decoy identities.
5. Preserve database health events, recovery attempts, maintenance audit events, and the former watchdog state directories for incident review.
6. Correct the configuration or runtime integration, return Bahia to observe-only, and repeat the fixture acceptance process before recovery is re-enabled.

A rollback does not require dropping the managed-instance health migrations. Leaving the schema and observation evidence intact is the preferred and safest rollback posture.
