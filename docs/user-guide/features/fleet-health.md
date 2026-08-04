# Fleet Health

Fleet Health is Bahia's resource-pressure operations view. It turns worker telemetry, admission posture, and cleanup lifecycle state into a single fleet map so operators can decide whether to deploy, clean up, cordon, or investigate.

## What it shows

Open **Operations → Fleet Health** in the web app.

The page contains:

- **Fleet weather map** — workers grouped into pressure lanes:
  - `blocked`: normal deployments should be denied.
  - `cleanup_only`: cleanup is required before normal placement.
  - `reduced`: deploy cautiously and preserve continuity reserve.
  - `open`: normal scheduling capacity.
- **Topology pressure cards** — each worker card shows liveness, capacity class, pressure level, recommended action, dominant pressure signal, telemetry chips, assignment count, and active cleanup state.
- **Cleanup status/history** — durable cleanup execution state projected from Bahia cleanup orchestration events.
- **Action rail** — prioritized blocked, cleanup-needed, and missing-telemetry workers.

## Cleanup mode flow

Fleet Health and Workers both use the same cleanup dialog.

Available modes:

| Mode | Use when | Behavior |
|------|----------|----------|
| `reclaimable_only` | Normal storage pressure remediation | Prunes reclaimable local runtime material while preserving Bahia protected refs. |
| `aggressive` | Operator-approved deeper cleanup | Requires explicit confirmation and still preserves continuity and standby artifacts protected by Bahia policy. |

Submitting the dialog publishes an encrypted ContextVM `worker/cleanup` intent. The ContextVM response is only an acknowledgment. Durable progress comes from Nostr cleanup state projections.

## Nostr state

Fleet Health consumes:

- Loom worker advertisements, kind `10100`, for host telemetry and capability state.
- Bahia canonical state, kind `30900`, schema `bahia.state.worker.v1`, for worker scheduling and pressure projections.
- Bahia canonical state, kind `30900`, schema `bahia.state.worker-cleanup.v1`, for cleanup lifecycle history.

Cleanup state tags include:

```json
[
  ["d", "worker:cleanup:<worker-pubkey>:<loom-job-or-start-time>"],
  ["domain", "worker"],
  ["schema", "bahia.state.worker-cleanup.v1"],
  ["worker", "<worker-pubkey>"],
  ["status", "completed"],
  ["cleanup_mode", "reclaimable_only"]
]
```

Cleanup state content includes `cleanup_id`, `worker_pubkey`, `cleanup_mode`, `reason`, `loom_job_id`, `protected_refs`, `target_free_gb`, `status`, `capacity_rejected`, `error`, `started_at`, `completed_at`, and `updated_at`.

## Fleet hygiene policy

The optional hygiene runner is configured with an enable flag, policy file, interval, and worker scope. Policy files use schema `hygiene/v1` and require absolute `scan_roots` and `protected_paths`. Defaults are 85% disk pressure, 90% inode pressure, and 14-day retention.

Tier-one automation may quarantine duplicate or cruft material and run garbage collection when enabled by policy. Relocation and purge remain operator-controlled tier-two actions; Bahia does not perform them automatically.

## Metrics and alerts

Fleet telemetry exports these operator-facing gauges:

- `bahia_worker_capacity_class_workers{class="open|reduced|cleanup_only|blocked"}`
- `bahia_worker_telemetry_freshness_workers{state="fresh|stale|absent"}`
- `bahia_worker_heartbeat_lag_seconds{worker="..."}`
- `bahia_worker_pressure_recommendations{action="none|cleanup_recommended|operator_intervention"}`
- `bahia_fleet_health_drift_states{status="..."}`
- `bahia_fleet_health_drift_age_seconds_max`
- `bahia_fleet_health_drift_stuck`
- `bahia_fleet_health_services{health="..."}`
- `bahia_fleet_health_entities`
- `bahia_fleet_health_nostr_entities{domain="...",status="healthy|degraded|unhealthy|unknown"}`
- `bahia_fleet_health_nostr_heartbeat_lag_seconds{entity="..."}`
- `bahia_fleet_health_projector_subscription_active`
- `bahia_fleet_health_projector_caught_up`
- `bahia_fleet_health_projector_last_event_timestamp_seconds`
- `bahia_fleet_health_projector_last_ingested_timestamp_seconds`
- `bahia_fleet_health_projector_relay_closed_total`
- `bahia_fleet_health_projector_errors_total`

The `nostr` gauges are projected directly from validated, persisted relay events of kinds `30315`, `30316`, `30317`, `30900`, and `4903`. Relay subscription and catch-up gauges deliberately remain separate from subject-health gauges: losing the relay says the observer is impaired, not that every observed agent or service failed. Labels are fixed domains and normalized health states; heartbeat entities use fixed-width signer pubkeys, and event content and coordinates are never exported as labels.

Only node, process, and GPU exporters are scraped directly. Semantic fleet state must arrive through signed Nostr observables rather than polling Bahia's database read models.

Use the shipped WS6 alert rules and Grafana dashboards to interpret these metrics together; a single pressure gauge is a scheduling signal, not proof of host failure.

## Operational model

Bahia owns pressure intelligence, admission policy, cleanup recommendations, and orchestration. Workers own local execution: image pruning, stopped container cleanup, cache eviction, log vacuuming, and runtime-local storage management.

Fleet Health intentionally does not SSH into hosts or expose arbitrary shell execution. Cleanup is requested through Bahia's Nostr-native mutation path and verified through canonical observables.
