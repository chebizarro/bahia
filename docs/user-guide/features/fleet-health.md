# Fleet Health

**Fleet Health** (`/fleet-health`) summarizes runtime pressure, cleanup candidates, cleanup executions, and operational state across the fleet.

## What it shows

The page combines trusted worker observations and service-authored control-plane records. It shows pressure by target, reclaimable resources, cleanup mode, recent cleanup results, and health summaries. Missing or stale evidence is unknown, not healthy.

Workers return scan candidates and pressure measurements only for a Bahia-authored request addressed to that worker. Bahia checks the authenticated response author, request event tag, and JSON-RPC correlation ID before accepting it.

## Cleanup

A cleanup preview identifies reclaimable images, containers, volumes, or other supported resources without changing the target. Applying cleanup creates a governed operation whose scope and mode are visible in status and audit records.

```bash
bahia workers cleanup <worker-pubkey> --mode reclaimable_only --reason "capacity pressure"
bahia workers cleanup-orphans
bahia workers cleanup-orphans --apply
```

Use aggressive mode only after reviewing the candidate set. Cleanup is not a substitute for retention policy or managed ownership.

## Metrics and alerts

Fleet health metrics report the current projected state by domain and status. Alert on sustained unhealthy or unknown counts, repeated cleanup failures, stale worker advertisements, and relay delivery warnings. Confirm an alert against canonical relay records before treating a single in-memory metric as complete evidence.

## Related

- [Workers](workers.md)
- [Instance Health](instance-health.md)
- [Route Canaries](route-canaries.md)
- [Troubleshooting](../troubleshooting.md)
