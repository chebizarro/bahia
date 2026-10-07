# Managed-instance supervision

## Purpose

Bahia observes explicitly managed runtime targets, persists current health and sanitized transition history, and can perform policy-bounded restarts of the exact target. It does not rebuild images or change desired configuration. Current state and recent recovery evidence are available in the web **Instance Health** and **Fleet Health** pages, which read the projected relay state; maintenance changes require an authenticated operator with service-write permission.

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
  observation_timeout: 30s
  memory_threshold: 0.90
  instances: []
```

A canary target is explicit and exact:

```yaml
supervision:
  enabled: true
  observe_only: true
  interval: 30s
  observation_timeout: 30s
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
- `observation_timeout` bounds every runtime observation, readiness probe, and restart command so one blocked target cannot indefinitely stall the supervision pass.
- `memory_threshold` marks sustained memory pressure as degraded after consecutive observations.
- A maintenance override suppresses recovery for only its full service/environment/deployment-unit/runtime-target key. Observation and alerting remain active.

Do not enable recovery with missing or zero budgets. Configuration validation rejects incomplete enabled targets and invalid recovery settings.

## Recovery apply lock and multi-daemon operation

A supervised restart takes the environment's runtime apply lock before acting,
so it never restarts an instance in the middle of a deploy. Two layers are
involved:

- a **process-local** lock per environment, always taken, which serializes
  recoveries with every apply driven by *this* daemon;
- the **shared PostgreSQL advisory lock** that deploys hold, taken when the
  daemon has a database, which additionally serializes recoveries with applies
  driven by *any* daemon of the fleet.

When PostgreSQL is unreachable the shared lock cannot be taken and recovery
proceeds under the process-local lock only. The daemon logs
`shared runtime apply lock unavailable; recovery proceeds under the
process-local lock ...` and the `supervision_apply_lock` health check turns
`warn` with `fallback=true`, `fallback_since` and `last_error`; it also warns
with `shared_lock=false` on a daemon that started without a database. The check
returns to `pass` on the first shared-lock attempt that gets an answer.

**Implication:** while the check is `warn`, a deploy driven by another daemon
that can still reach PostgreSQL is not excluded, so a recovery may restart an
instance that daemon is deploying. With a single daemon per environment there
is nothing to exclude and the fallback is safe; a deploy from the same daemon
needs the same unreachable lock and cannot be running. With several daemons
supervising or deploying into one environment, respond to a sustained `warn`
by either pausing deploys into that environment until the check passes, or
setting a maintenance override on the instances under recovery
(`observe_only` is the blunt alternative). Do not "fix" the warning by
removing the shared lock from the deploy path.

## Operator surfaces

Health reads are relay state, not HTTP: supervision projects current health
into the `runtime-instance-health` family (`30900`) with `30315` status and
`4903` audit facts, and the web Instance Health and Fleet Health pages render
that state from the local store.

Maintenance windows are the HTTP write surface
([HTTP reference](../api.md)):

- `POST /api/v1/services/{serviceId}/environments/{envId}/managed-instances/{deploymentUnitId}/maintenance?runtime_target_name=...`
- `DELETE` on the same path closes the window.

Writes are organization-scoped through both service and environment
ownership and require an authenticated service operator; the actor comes from
the authenticated principal rather than the request body.

## Enabling recovery on a target

Adopt targets one at a time, observe-only first:

1. Record the exact service, environment, deployment-unit, and runtime-target
   identity for the target.
2. Enable supervision for only that target with `observe_only: true`.
3. Compare Bahia observations against what is actually happening on the host:
   stopped states, failed health probes, OOM-like exits, restart counts,
   memory pressure, and timestamps.
4. Confirm the evidence is sanitized, the target identity is exact, and the
   `30315`, `30900`, and `4903` projections agree with the persisted history.
5. Exercise a maintenance override and confirm observation continues while
   recovery remains suppressed.
6. Continue the canary through representative healthy and failure intervals;
   investigate every mismatch before proceeding.
7. Set `observe_only: false` only after operator acceptance of the comparison
   evidence. If another mechanism can restart the same target, place it in a
   non-active posture so the two never restart simultaneously.
8. Prove exact-target recovery with a labelled decoy target and verify the
   decoy's start time and identity do not change.
9. Verify restart budgets, backoff, alerts, and maintenance suppression under
   recovery-enabled operation.

Do not expand to additional targets until the current one meets these
acceptance criteria.

## Rollback

Rollback recovery without losing observation or alerting:

1. Set `supervision.observe_only: true` and restart Bahia. This immediately disables Bahia restarts while current health, transition persistence, projections, and alerts continue.
2. If the supervisor itself is causing operational load, set `supervision.enabled: false`; retain the health tables and projected audit events.
3. If another restart authority covers the target, re-enable it, ensuring only one system can restart the target.
4. Confirm no pending Bahia recovery attempt is active and verify the target and decoy identities.
5. Preserve database health events, recovery attempts, and maintenance audit events for incident review.
6. Correct the configuration or runtime integration, return Bahia to observe-only, and repeat the acceptance process before recovery is re-enabled.

A rollback does not require dropping the managed-instance health migrations. Leaving the schema and observation evidence intact is the preferred and safest rollback posture.
