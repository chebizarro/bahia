# Resource Pressure Mixed-Version Rollout

Resource pressure orchestration is additive. New Bahia versions accept old worker advertisements, and older Bahia versions ignore the additional telemetry fields emitted by new workers.

## Old workers talking to new Bahia

Old workers do not advertise `telemetry` or `pressure`. New Bahia treats these workers conservatively:

- pressure assessment: `warning`
- capacity class: `reduced`
- admission: standard non-dynamic placements remain eligible at reduced capacity
- dynamic headroom requests that require telemetry are rejected because Bahia cannot prove free memory, disk, or VRAM

## New workers talking to old Bahia

New workers add telemetry fields to existing worker advertisements. Old Bahia readers that do not know these fields continue reading the known worker data and ignore the additions. During rollout, do not rely on old Bahia to enforce resource-pressure admission or cleanup-only behavior.

## Partial collector scenarios

Workers may run on hosts without every collector source available:

- no Docker data: disk telemetry may still report filesystem capacity; Docker reclaim fields are zero or omitted, so critical disk pressure without reclaimable data requires operator intervention
- no GPU/accelerator: accelerator telemetry is omitted, and Bahia does not evaluate VRAM signals for that worker
- no thermal sensor: thermal telemetry is omitted, and Bahia evaluates the remaining signals
- memory telemetry unavailable: Bahia records the memory signal as `unknown` and reduces capacity because memory is required for safe headroom decisions
- all telemetry unavailable: Bahia reduces capacity and requires operator attention instead of assuming the host is healthy

## Stale telemetry handling

Telemetry older than ten minutes is treated as degraded. Bahia emits a `telemetry_stale` unknown signal, marks the worker `warning` / `reduced`, and avoids making open-capacity decisions from old samples. Worker advertisement writes are guarded by `last_advertisement_at` so relay reordering cannot regress a newer pressure or telemetry snapshot with an older event.

## Rollout ordering

Recommended order:

1. Deploy new Bahia schema/domain/repository/projector code first. Old workers remain eligible with reduced capacity.
2. Roll out worker telemetry collectors. New fields are additive for older Bahia versions.
3. Enable pressure-aware admission broadly after enough workers publish fresh telemetry.
4. Enable cleanup automation selectively with labels.

## Cleanup feature gate

Automatic cleanup dispatch is enabled per worker with:

```text
bahia.cleanup.auto=true
```

Without this label, Bahia may recommend cleanup and allow cleanup-scoped jobs, but operators must explicitly trigger cleanup.
