# Resource Pressure Orchestration: Plan

## Goal

Add resource-pressure intelligence to Bahia so it can reason about host capacity (disk, memory, VRAM, thermal, docker cache), enforce deployment admission control as a universal layer, and orchestrate cleanup recipes via Loom jobs — extending loom-worker's existing Kind 10100 advertisement with live telemetry fields.

## Background

### Prior art

The worker management plan (`docs/plans/bahia-worker-management-nostr-native-2026-05-22.md`) covers scheduling states, operator controls (cordon/drain/maintenance), labels, placement policies, and read models — but does NOT address resource pressure telemetry, deployment admission control, cleanup orchestration, or capacity classes. This plan is the complementary resource-intelligence layer that builds on that foundation.

### Existing worker communication model

- **Kind 10100 Worker Advertisement** (replaceable): loom-worker publishes every 60s with content JSON (`name`, `description`, `max_concurrent_jobs`, `current_queue_depth`) and tags (`S`, `A`, `default_shell`, `price`, `metric`, `min_duration`, `max_duration`, optional `g`). Ref: `loom-worker/src/nostr/service.ts:261-297`
- **Bahia parser** already accepts extended content fields beyond the Loom spec: `resources` (CPUCores/MemoryGB/DiskGB), `accelerators`, `runtime_target`, `ml_capabilities`. Unknown fields are silently ignored. Ref: `internal/adapters/nostr/processor.go:281-309`
- **Kind 30350 Heartbeat Observation** (parameterized replaceable): `d=heartbeat:<worker_pubkey>`, used for continuity liveness. Ref: `internal/domain/heartbeat.go:6-12`, `internal/adapters/nostr/continuity_serialization.go:348-407`
- **Advertisement publish timer**: `setInterval(advertise, 60_000)` in `loom-worker/main.ts:574-575`. No event-driven trigger for queue depth changes.

### Existing resource fields on Worker domain

- `WorkerResources`: `CPUCores`, `MemoryGB`, `DiskGB` — `internal/domain/worker.go:85-91`
- `WorkerAccelerator`: `Vendor`, `Model`, `Count`, `MemoryGB`, `Driver` — `internal/domain/worker.go:92-99`
- `MaxConcurrentJobs`, `CurrentQueueDepth` — `internal/domain/worker.go:126-127`
- `SchedulingState`: `active`, `cordoned`, `draining`, `maintenance`, `disabled` — `internal/domain/worker.go:17-25`
- Worker liveness derived from `LastAdvertisementAt`: online/stale/offline — `internal/domain/worker.go:173-189`

### Existing placement logic (fragmented)

Three separate placement implementations (`llm_placement.go`, `ml_placement.go`, `backup_placement.go`) plus core service reconciliation (`reconciler.go:96-133`) with no capacity check. Common checks duplicated across all three: worker liveness, scheduling state admission, runtime target requirement, selector/label matching, price ceiling, resource constraints. LLM placement notably does NOT enforce scheduling state.

### Loom job dispatch mechanism

- Bahia builds `JobRequest` and publishes Kind 5100 via `internal/adapters/loom/client.go:150-247`
- Worker subscribes to Kind 5100 `#p=[workerPubkey]`, validates whitelist/payment/capacity, executes via external adapter socket — `loom-worker/main.ts:105-371`
- Status updates as Kind 30100, terminal results as Kind 5101 — `loom-worker/src/nostr/service.ts:305-413`
- Worker concurrency: `MAX_CONCURRENT_JOBS` (default 3), queue depth guard rejects when full — `loom-worker/main.ts:276-296`
- Job type is currently just an env var `BAHIA_DEPLOY_TYPE`, not a first-class protocol concept — `internal/adapters/loom/client.go:200-207`

### Event bus and derived-state patterns

- Internal event bus: `Event{Type, EntityID, Data}`, async dispatch via goroutines — `internal/events/events.go:72-146`
- Heartbeat monitor: in-memory map, computes freshness (fresh/stale/expired) from observation age vs. `ExpiresAfter` — `internal/service/heartbeat_monitor.go:27-137`
- Continuity graph: combines workers + heartbeats + definitions → derived `Survivability`, `ReplicationFreshness`, `StandbyHealth` — `internal/service/continuity_graph.go:13-118`
- Failover trigger engine: phases `healthy` → `suspect` → `firing` → `active`, derived from heartbeat snapshots — `internal/service/failover_trigger_engine.go:20-269`

### User decisions (Phase 1)

1. **Extend loom-worker** — no separate bahia-node-agent process
2. **Extend Kind 10100** — richer content in existing advertisement (not new event kind)
3. **Universal admission layer** — all deployments pass through unified admission
4. **Loom jobs** — cleanup recipes dispatched as standard Loom jobs

---

## Approach

### Architectural model

This is a **targeted refactor plus additive telemetry/orchestration work**, not a platform rewrite. The runtime model:

1. **loom-worker** samples host telemetry locally on a 15s interval.
2. **loom-worker** embeds that telemetry in Kind 10100 content JSON, alongside static inventory and existing queue data. Advertisements continue publishing every 60s, plus immediate republish on queue/job lifecycle changes.
3. **Bahia** parses the ad, computes a Bahia-owned pressure assessment (thresholds and policy belong to the control plane), persists both raw telemetry and derived pressure on `domain.Worker`, then emits an internal `worker.telemetry.observed` event.
4. An in-memory **pressure monitor** detects capacity-class transitions and emits `worker.pressure.changed` events.
5. A **cleanup orchestrator** reacts to pressure changes — default mode is recommend-only; opt-in auto mode dispatches Loom cleanup jobs to affected workers.
6. A **shared admission service** is called from every worker-backed placement path and the final deployment dispatch path.
7. Worker-state read-model publication is extracted from the Reactor so it fires on every ad ingest (not only operator commands).
8. UI surfaces fleet health client-side from per-worker state — no separate aggregate backend in phase 1.

### Telemetry extension (loom-worker)

New `loom-worker/src/telemetry/service.ts` module with pluggable collectors for memory, disk, docker cache, GPU/VRAM, and thermal. Collectors are best-effort — unavailable metrics are omitted, never fail the advertisement.

Content JSON gains a `telemetry` block:

```json
{
  "telemetry": {
    "sampled_at": "2026-05-24T12:00:00Z",
    "memory": { "total_bytes": ..., "available_bytes": ..., "used_percent": ... },
    "disk": { "path": "/", "total_bytes": ..., "available_bytes": ..., "used_percent": ..., "docker_cache_bytes": ..., "docker_reclaimable_bytes": ... },
    "accelerators": [{ "index": 0, "memory_total_bytes": ..., "memory_free_bytes": ..., "temperature_c": ... }],
    "thermal": { "max_temperature_c": ..., "throttled": false }
  }
}
```

Static `resources` and `accelerators` also move into the advertisement content (Bahia already parses these).

Replaceable ordering fix: same-second `created_at` mining (matching existing job-status pattern) prevents relay reordering from regressing telemetry state.

### Pressure evaluation (Bahia-owned)

Pure stateless evaluator at `internal/service/worker_pressure_evaluator.go`. Thresholds are config-backed. Suggested starting defaults (implementation should make these tunable):

| Signal | Warning | Critical |
|--------|---------|----------|
| Memory free | < max(4 GiB, 20% total) | < max(2 GiB, 10% total) |
| Disk free | < max(40 GiB, 15% total) | < max(20 GiB, 8% total) |
| VRAM free | < max(4 GiB, 20% total) | < max(2 GiB, 10% total) |
| Thermal | ≥ 85°C | ≥ 92°C or throttled |
| Queue utilization | > 0.80 | ≥ 1.0 |

**Important edge-case rules:**
- If docker reclaimable data is missing from telemetry, disk pressure maps to `operator_intervention` (not auto cleanup) because Bahia cannot estimate reclaim potential
- Thermal, memory, and VRAM pressure are NOT cleanupable — they result in `operator_intervention` recommended action, never auto cleanup dispatch
- These are existing production bugs that this work fixes: (a) stale ads can overwrite newer ones due to missing timestamp guard in `pg_worker.go`, (b) loom-worker doesn't mine monotonic `created_at` for same-second Kind 10100 updates

Capacity-class mapping:

| Class | Condition |
|-------|-----------|
| `open` | All known signals nominal |
| `reduced` | Warning-level signal(s) OR missing telemetry |
| `cleanup_only` | Critical disk AND reclaimable docker bytes sufficient to recover |
| `blocked` | Critical memory/VRAM/thermal, queue full, or critical disk with no reclaim path |

Workers without telemetry → `reduced` capacity class with conservative admission. This enables safe mixed-version rollout.

Standby-aware: workers with `StandbyAssignments` in hot/warm tier use the warning floor as effective reserve, making standby workers more conservative without a new policy store.

### Universal admission layer

New `internal/service/worker_admission.go` evaluates all worker-backed placements through a single ordered check sequence:

1. Worker present → liveness check
2. Scheduling state (active only for standard; any non-offline for cleanup)
3. Pinned worker mismatch
4. Selector/label mismatch
5. Runtime target requirement
6. Price ceiling
7. Capacity-class gate (`cleanup_only`/`blocked` reject standard placements)
8. Dynamic headroom (memory, VRAM, disk vs. request minimums minus reserve)
9. If eligible → hand back to family-specific scoring

LLM `external_api` backend explicitly bypasses worker admission with a documented workerless decision.

Existing placement services delegate common checks to admission, keeping their unique scoring/capability/preference logic.

### Cleanup orchestration

New `internal/service/worker_cleanup_orchestrator.go` dispatches standard Loom Kind 5100 jobs:

- **Default mode**: `recommend_only` — surface cleanup recommendations, don't auto-execute
- **Auto mode** (opt-in via worker label `bahia.cleanup.auto=true`): dispatch `reclaimable_only` cleanup when `capacity_class == cleanup_only`
- **Operator mode**: `worker.cleanup.request` command triggers cleanup at any time

Cleanup modes:
- `reclaimable_only` (safe/auto): prune builder cache, stopped containers, dangling images
- `aggressive` (operator-only): remove unused non-protected images beyond dangling

Protected keep-list derived from existing data:
1. Active assignment metadata (`image_ref`, `artifact_id`)
2. `Worker.StandbyAssignments` artifacts (hot/warm standby)
3. Worker label `bahia.cleanup.protect=true` disables aggressive mode

Per-worker cooldown (default 30m). Does NOT bypass `MAX_CONCURRENT_JOBS` — queue fullness is enforced worker-side (Loom rejects the Kind 5100). The cleanup orchestrator must distinguish Loom capacity-rejection from other job failures: on capacity rejection, skip cooldown burn and re-attempt on the next pressure evaluation after queue clears. On other failures, apply cooldown normally.

**Payment path:** Internal cleanup jobs reuse the same payment-token injection path used by deployment jobs in `internal/adapters/loom/client.go:220-227`. Validate during implementation whether this path requires a billing record or can submit with a zero/internal token. If payment is required, the simplest resolution is a Bahia-issued internal mint token — not a Loom protocol change.

### Fleet health UX

No separate backend aggregate in phase 1. Workers page gains:
- Fleet summary cards (total, blocked, cleanup_only, reduced)
- Pressure/capacity filters
- Per-row telemetry chips (disk free, VRAM free, thermal, capacity class)
- Cleanup action button

ML preview updates to reject/penalize workers matching backend admission rules.

---

## Work Items

### Item 1 — Worker telemetry/pressure data model and persistence

**Goal:** Make `domain.Worker` able to store raw telemetry and Bahia-derived pressure state, with safe persistence that prevents stale advertisements from overwriting newer ones.

**Done when:**
- `Worker` struct has `Telemetry *WorkerTelemetry` and `Pressure *WorkerPressureAssessment`
- Pressure/capacity enums defined (`WorkerPressureLevel`, `WorkerCapacityClass`, `WorkerPressureAction`)
- DB migration adds `telemetry` and `pressure` JSONB columns
- `pg_worker.go` marshals/unmarshals both fields
- `Upsert` SQL uses `WHERE EXCLUDED.last_advertisement_at >= workers.last_advertisement_at` guard

**Key files:** `internal/domain/worker.go`, `internal/db/migrations/0000xx_worker_telemetry_pressure.up.sql` (new), `internal/repository/pg_worker.go`

**Dependencies:** None

**Size:** M

---

### Item 2 — loom-worker telemetry collection and advertisement extension

**Goal:** Make loom-worker publish static inventory plus live host telemetry inside Kind 10100 content JSON.

**Done when:**
- Telemetry sampler module exists (`loom-worker/src/telemetry/service.ts`)
- Collectors for memory, disk, docker cache, GPU, thermal (best-effort)
- `publishAdvertisement()` refactored to accept payload object including `resources`, `accelerators`, `telemetry`
- Same-second monotonic `created_at` ordering for Kind 10100
- Queue/job lifecycle changes trigger immediate republish
- Config knobs: `TELEMETRY_SAMPLE_INTERVAL_MS` (default 15000), `ADVERTISEMENT_INTERVAL_MS` override

**Key files:** `loom-worker/main.ts`, `loom-worker/src/nostr/service.ts`, `loom-worker/src/config/env.ts`, `loom-worker/src/config/constants.ts`, `loom-worker/src/telemetry/` (new)

**Dependencies:** Can land independently of Item 1 (old Bahia ignores unknown content fields)

**Size:** L

---

### Item 3 — Bahia-side telemetry parsing, pressure evaluation, and events

**Goal:** Turn raw Kind 10100 telemetry into canonical worker rows plus internal telemetry events.

**Done when:**
- `processor.go` parses `telemetry` content field
- Pure pressure evaluator computes `Worker.Pressure` on ingest with configurable thresholds
- Canonical worker row reloaded after upsert (preserves operator-owned fields)
- `worker.telemetry.observed` event published only for current (non-stale) ads
- Internal event contracts defined (`worker_pressure_events.go`)

**Key files:** `internal/adapters/nostr/processor.go`, `internal/service/worker_pressure_evaluator.go` (new), `internal/events/events.go`, `internal/events/worker_pressure_events.go` (new)

**Dependencies:** Items 1 and 2

**Size:** L

---

### Item 4 — Pressure transition monitor and worker-state publication extraction

**Goal:** Detect capacity-class transitions and republish Bahia worker state on every ad ingest.

**Done when:**
- In-memory pressure monitor tracks last-seen pressure per worker
- App wiring subscribes to `worker.telemetry.observed` and routes to monitor
- `worker.pressure.changed` emitted on class transitions
- Worker-state publisher extracted from Reactor into reusable `worker_state_publisher.go`
- Worker-state events include `telemetry` and `pressure` fields plus filtering tags
- Same-second replaceable timestamp mining per worker pubkey

**Key files:** `internal/service/worker_pressure_monitor.go` (new), `internal/controlplane/worker_state_publisher.go` (new), `internal/controlplane/worker_handlers.go`, `internal/app/app.go`

**Dependencies:** Item 3

**Size:** M

**Splitting note:** The state-publisher extraction is a standalone refactor that could land earlier as a preparatory PR. The pressure monitor is the dependent piece. Implementation agent may split this into two PRs.

---

### Item 4b — Assignment metadata enrichment for cleanup keep-list

**Goal:** Ensure active assignment projections carry the artifact/image references needed by the cleanup keep-list.

**Done when:**
- `WorkerAssignment.Metadata` in the assignment projector populates `artifact_id` and `image_ref` keys from deployment run/intent data
- `WorkerStandbyAssignment` struct gains an optional `ArtifactRef string` field for hot/warm standby image references
- Existing assignment read-model consumers are unaffected (additive fields)

**Key files:** `internal/domain/worker_read_models.go`, `internal/domain/worker.go` (standby struct), assignment projector (validate exact file during implementation)

**Dependencies:** None (can proceed in parallel with Items 3–4)

**Size:** M

---

### Item 5 — Cleanup orchestration and operator cleanup command

**Goal:** Allow Bahia to dispatch cleanup as standard Loom jobs, both operator-triggered and pressure-triggered.

**Done when:**
- Cleanup orchestrator with dedupe/cooldown exists
- `worker.cleanup.request` command handled in control plane
- Operator-triggered cleanup returns `loom_job_id` in result event
- Auto mode defaults to off (`recommend_only`)
- Protected keep-list derived from enriched assignments + standby state
- Loom capacity-rejection distinguished from other failures (no cooldown burn on queue-full)
- Cleanup completion/failure events published
- `reclaimable_only` and `aggressive` modes implemented
- Payment-token path for internal jobs validated and working

**Key files:** `internal/service/worker_cleanup_orchestrator.go` (new), `internal/controlplane/worker_handlers.go`, `internal/controlplane/reactor.go`, `internal/adapters/loom/client.go` (payment path), `internal/app/app.go`

**Dependencies:** Items 4 and 4b

**Size:** L

---

### Item 6 — Universal admission service and placement integration

**Goal:** Remove duplicated worker availability/capacity checks and make all placement families pressure-aware.

**Done when:**
- Shared `worker_admission.go` with typed request/decision exists
- `worker_policy.go` delegates common eligibility to admission service
- ML placement calls admission before ML-specific scoring with byte-level headroom checks
- LLM placement calls admission for worker-backed backends; scheduling state enforced (currently missing); `external_api` explicitly bypasses with documented reason
- Backup placement calls admission before capability/label checks
- Telemetry-missing workers handled conservatively (`reduced` class)

**Key files:** `internal/service/worker_admission.go` (new), `internal/service/worker_policy.go`, `internal/service/ml_placement.go`, `internal/service/llm_placement.go`, `internal/service/backup_placement.go`

**Dependencies:** Item 3

**Size:** L

---

### Item 7 — Final dispatch-time admission gate

**Goal:** Ensure a worker can still be blocked at execution time if pressure changed after selection.

**Done when:**
- Admission gate inserted between worker selection (`coordinator.go:143`) and job submission (`coordinator.go:167`)
- Rejected dispatches surface a stable admission reason to the intent lifecycle (deployment run marked failed with admission code)
- No retry/re-select loop in phase 1 — rejection is terminal for the current run attempt
- No false rejections for workerless paths

**Key files:** `internal/workflow/coordinator.go:143-167`

**Dependencies:** Item 6

**Size:** M

**Design note:** The coordinator currently has no retry/re-select path. In phase 1, dispatch-time rejection is terminal — the deployment run fails with a pressure-admission reason. A future enhancement could re-trigger placement, but that's out of scope here.

---

### Item 8 — Fleet health and capacity-aware UI

**Goal:** Make operators able to see fleet pressure, act on it, and get consistent previews.

**Done when:**
- Workers page shows fleet summary cards (total, blocked, cleanup_only, reduced)
- Pressure/capacity/recommended-action filters available
- Per-worker rows show telemetry, capacity class, and recommended action
- Cleanup action button dispatches `worker.cleanup.request`
- ML preview rejects/penalizes pressured workers consistent with backend rules
- Telemetry-unavailable workers show graceful fallback display

**Key files:** `web/src/routes/workers/+page.svelte`, `web/src/routes/ml/page-model.js`, `web/src/lib/stores/controlplane.svelte.js`

**Dependencies:** Items 4, 5, 6

**Size:** M

---

### Item 9 — Rollout hardening and integration testing

**Goal:** Verify mixed-version safety and end-to-end behavior.

**Done when:**
- Mixed old/new worker behavior documented and tested
- Hosts without docker/GPU telemetry degrade cleanly (collectors omit, pressure evaluator handles)
- Stale-write guard tested under relay reordering conditions
- End-to-end scenario: worker under disk pressure → admission blocks → operator triggers cleanup → pressure resolves → admission unblocks

**Key files:** Cross-cutting validation across items 1–8

**Dependencies:** Items 1–8

**Size:** M

---

## Rollout Strategy

This is an additive, mixed-version-safe rollout:

1. **Bahia schema/domain/repo/processor** ships first — old workers send no telemetry, Bahia marks them `reduced`
2. **loom-worker telemetry** ships independently — old Bahia ignores unknown content fields
3. **Admission enforcement** ships after telemetry is visible on enough workers (or behind a config gate for partial rollout)

Backward compatibility:
- New Bahia reading old worker ads: supported (missing telemetry → `reduced`)
- Old Bahia reading new worker ads: supported (JSON unknown fields ignored)
- REST/Nostr clients: additive fields only

---

## Open Questions

1. **Telemetry cadence for pressure detection** — 60s advertisement interval means pressure detection has up to 60s latency. If critical thresholds should trigger immediate republish from loom-worker, Item 2 grows and gains a soft dependency on Item 3's threshold definitions. Current recommendation: accept 60s latency in phase 1; revisit after observing real-world pressure patterns.
2. **Payment-token path for internal jobs** — If the existing path in `client.go:220-227` requires a real billing record, a resolution (Bahia-issued internal token or zero-amount bypass) must be designed before Item 5 can complete. Named as a risk, not a blocker — validate early in implementation.

---

## References

- Worker management plan: `docs/plans/bahia-worker-management-nostr-native-2026-05-22.md`
- Loom protocol spec: `loom-protocol/SPECIFICATION.md`
- Domain models: `internal/domain/worker.go`
- Heartbeat monitor pattern: `internal/service/heartbeat_monitor.go`
- Continuity graph pattern: `internal/service/continuity_graph.go`
- Failover trigger pattern: `internal/service/failover_trigger_engine.go`
- Worker placement: `internal/service/llm_placement.go`, `ml_placement.go`, `backup_placement.go`
- Loom client: `internal/adapters/loom/client.go`
- Worker state publication: `internal/controlplane/worker_handlers.go`
