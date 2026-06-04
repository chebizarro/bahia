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

## Operational model

Bahia owns pressure intelligence, admission policy, cleanup recommendations, and orchestration. Workers own local execution: image pruning, stopped container cleanup, cache eviction, log vacuuming, and runtime-local storage management.

Fleet Health intentionally does not SSH into hosts or expose arbitrary shell execution. Cleanup is requested through Bahia's Nostr-native mutation path and verified through canonical observables.
