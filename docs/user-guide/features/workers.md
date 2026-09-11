# Workers

**Workers** in Bahia execute deployments, run ML inference, and perform operational tasks. They are typically Loom workers with Nostr identities.

## Overview

Workers provide:
- **Deployment execution** — Pull images, apply to runtimes
- **Runtime observation** — Report what's actually running
- **ML inference hosting** — Serve model endpoints
- **Task execution** — Run recipes, scripts, jobs

## Key Concepts

### Worker

A **Worker** is a Loom compute worker discovered from its kind `10100` advertisement and identified by its Nostr pubkey. Abbreviated read model:

```yaml
pubkey: "<worker-pubkey-hex>"
name: "prod-worker-1"
architecture: "linux/amd64"
max_concurrent_jobs: 4
current_queue_depth: 0
status: "online"            # advertisement freshness
scheduling_state: "active"  # operator scheduling intent
capabilities:
  workload_kinds: ["deployment"]
  runtimes: ["docker", "compose"]
  accelerators: []
labels:
  zone: "home-lab"
```

### Worker Status

`status` reflects advertisement freshness:

| Status | Description |
|--------|-------------|
| `online` | Recent advertisement |
| `stale` | No advertisement for more than 5 minutes |
| `offline` | No advertisement for more than 30 minutes |

`scheduling_state` reflects operator intent: `active`, `cordoned`, `draining`, `maintenance`, or `disabled`. Heartbeat freshness is tracked separately in `heartbeat_status`.

### Capabilities

Workers advertise a generic placement capability view (`workload_kinds`, `runtimes`, `artifact_formats`, `accelerators`, `toolchains`, `features`) plus `software`, `resources`, `accelerators`, and a normalized `ml_capabilities` view used for [ML placement](ml-models.md).

## Viewing Workers

### Web UI

Navigate to **Workers** in the sidebar:
- View all discovered workers
- See status and capabilities
- Check current jobs
- Use **Request cleanup** to open the cleanup mode dialog

Navigate to **Fleet Health** for the dedicated resource-pressure view:
- See the fleet weather map grouped by capacity class
- Review cleanup history and active cleanup status
- Open cleanup remediation for workers with cleanup recommendations

Click a worker (`/workers/<pubkey>`) to see:
- **Scheduling** state and lifecycle actions
- **Capabilities**, **Resources**, and **Accelerator inventory**
- **Labels & Placement** with example selectors
- **Active Assignments** and **Drain blockers**
- **Loom Jobs** (active and recent)
- **Pricing Tiers**, **Execution Details**, **Software**, **Preferred Relays**, and **Timestamps**

### CLI

```bash
# List workers
bahia workers list

# Show worker details
bahia workers show npub1worker...
```

### MCP Tool

```json
{
  "tool": "bahia_list_workers",
  "arguments": {}
}
```

```json
{
  "tool": "bahia_get_worker",
  "arguments": {
    "pubkey": "<worker-pubkey-hex>"
  }
}
```

## Worker Selection

Worker placement is driven by environment and worker metadata rather than a global `deployment.worker_selection` config block (which does not exist):

- The environment's `loom_worker_selector` (set with `--loom-worker-selector-file` or the **Worker Placement Policy** section) constrains non-Compose placement.
- Worker `labels` (updated via `bahia_worker_labels_update`) and advertised capabilities are matched against selectors.
- Workers that are `cordoned`, `draining`, `maintenance`, or `disabled` are excluded from new assignments.
- `bahia_worker_preview_eligibility` previews which workers qualify before you commit.

## Worker Pricing

Workers can have pricing for task execution. The current CLI does not register `bahia workers pricing`; view pricing through the web UI and payment/read-model surfaces.

Pricing comes from each worker's advertisement as a list of tiers:

```yaml
pricing:
  - mint_url: "https://mint.example.com"
    price_per_second: 1
    unit: "sat"
```

### Cost Estimation

Before deployment, use the web UI or the `bahia_estimate_cost` MCP tool to compare worker-sensitive deployment costs.

## Worker Registration

Workers self-register via Nostr: Bahia discovers any Loom worker whose replaceable kind `10100` advertisement reaches its configured Loom relays (`loom.relays`). There is no Bahia-side registration step.

> **NOTE (2026-09-11):** installing and configuring the Loom worker itself is documented in the Loom worker project, not in Bahia. The advertisement payload schema is owned by the Cascadia/Loom protocol (`cascadia.CAS_WORKER_AD`); see `internal/adapters/loom/` for the fields Bahia parses.

## Worker Commands

### MCP Tools for Worker Management

```json
{
  "tool": "bahia_worker_drain",
  "arguments": {
    "worker_pubkey": "npub1worker..."
  }
}
```

```json
{
  "tool": "bahia_worker_undrain",
  "arguments": {
    "worker_pubkey": "npub1worker..."
  }
}
```

### Nostr events

Worker mutations use signed ContextVM requests and canonical `30900`, `30315`, and `4903` observables. Historical `5976`/`6976`/`7976` tool-provision events are migration inputs, not the current production transport.

## Read Models

Worker state is published as Nostr events:

| Kind | Content |
|------|---------|
| 10100 | Loom worker advertisement and capabilities |
| 30900 | Canonical projected worker state |

Subscribe to kind `10100` for Loom advertisements:

```json
{
  "kinds": [10100]
}
```

Kind `31989` is a legacy ML runtime-capability profile, not the Loom worker advertisement kind.

## Health Monitoring

### Advertisements and heartbeats

Worker `status` is derived from advertisement freshness (`online` → `stale` after 5 minutes → `offline` after 30 minutes). Active heartbeats are tracked separately as `heartbeat_status` / `last_heartbeat_at`. Resource pressure is summarized on [Fleet Health](fleet-health.md).

### Notifications

Configure an organization-scoped webhook or Nostr DM channel for the worker event types emitted by your deployment. See [Notifications](notifications.md); Slack is not a distinct Bahia channel type.

## Best Practices

1. **Run multiple workers** — Redundancy and load distribution
2. **Label workers** — Make placement selectors explicit
3. **Drain before maintenance** — Use drain/maintenance instead of stopping workers abruptly
4. **Monitor health** — Alert on stale or offline workers

## Troubleshooting

### Worker Offline

- Check network connectivity
- Verify relay connection
- Check worker process running
- Review worker logs

### Task Stuck

- Check worker status
- Review task logs
- Verify worker has required capability

### Capability Missing

- Update worker configuration
- Restart worker to re-announce
- Verify required software installed

## Related

- [Deployments](deployments.md) — Worker execution
- [ML Models](ml-models.md) — ML inference hosting
- [Payments](payments.md) — Worker costs
