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

A **Worker** is an agent identified by its Nostr pubkey:

```yaml
pubkey: "npub1worker..."
name: "prod-worker-1"
status: "online"
capabilities:
  - docker
  - kubernetes
  - vllm
hardware:
  cpu_cores: 32
  memory_gb: 128
  gpu: "NVIDIA A100"
```

### Worker Status

| Status | Description |
|--------|-------------|
| `online` | Connected and available |
| `offline` | Not connected |
| `busy` | Currently executing a task |
| `draining` | Finishing current work, not accepting new |

### Capabilities

Workers declare what they can do:

| Capability | Description |
|------------|-------------|
| `docker` | Docker container deployment |
| `kubernetes` | Kubernetes deployment |
| `compose` | Docker Compose |
| `vllm` | vLLM inference |
| `onnx` | ONNX runtime |
| `rknn` | Rockchip NPU |

## Viewing Workers

### Web UI

Navigate to **Workers** in the sidebar:
- View all registered workers
- See status and capabilities
- Check current tasks

Click a worker to see:
- **Overview**: Status, capabilities, hardware
- **Tasks**: Current and recent tasks
- **Pricing**: Cost per task (if configured)
- **Logs**: Recent activity

### CLI

```bash
# List workers
bahia workers list

# Get worker details
bahia workers get npub1worker...

# Check worker pricing
bahia workers pricing npub1worker...
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
    "worker_pubkey": "npub1worker..."
  }
}
```

## Worker Selection

When creating a deployment:

1. **Automatic** — Bahia selects based on capabilities and availability
2. **Preferred** — Suggest workers, fall back if unavailable
3. **Required** — Must use specified worker(s)

### Configuration

```yaml
deployment:
  worker_selection:
    mode: "preferred"
    workers:
      - "npub1worker1..."
      - "npub1worker2..."
    capabilities_required:
      - "kubernetes"
```

## Worker Pricing

Workers can have pricing for task execution:

### Viewing Pricing

```bash
bahia workers pricing npub1worker...
```

```yaml
pricing:
  base_cost: 0.01  # per task
  per_minute: 0.001
  gpu_multiplier: 2.0
  currency: "sats"
```

### Cost Estimation

Before deployment:

```bash
bahia payments estimate \
  --service-id svc-123 \
  --environment-id env-456 \
  --worker-pubkey npub1worker...
```

## Worker Registration

Workers self-register via Nostr:

### Loom Worker Setup

```bash
# Install Loom worker
loom-worker install

# Configure
loom-worker config \
  --relays wss://relay.example.com \
  --bahia-pubkey npub1bahia...

# Start
loom-worker start
```

### Registration Event

Workers publish capability announcements:

```json
{
  "kind": 31989,
  "content": {
    "capabilities": ["docker", "kubernetes"],
    "hardware": {
      "cpu_cores": 32,
      "memory_gb": 128
    }
  },
  "tags": [
    ["d", "worker:npub1worker...:ai-capability"],
    ["t", "loom-worker"]
  ]
}
```

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
  "tool": "bahia_worker_resume",
  "arguments": {
    "worker_pubkey": "npub1worker..."
  }
}
```

### Nostr Events

Worker command events go through the Nostr control plane:

| Kind | Name | Description |
|------|------|-------------|
| 5976 | ToolProvisionRequest | Request tool setup |
| 6976 | ToolProvisionStatus | Provisioning progress |
| 7976 | ToolProvisionResult | Provisioning result |

## Read Models

Worker state is published as Nostr events:

| Kind | d-tag | Content |
|------|-------|---------|
| 31989 | `worker:<pubkey>:ai-capability` | Worker capabilities |

Subscribe for updates:

```json
{
  "kinds": [31989],
  "#t": ["loom-worker"]
}
```

## Health Monitoring

### Heartbeats

Workers send periodic heartbeats:
- Update online status
- Report current load
- Announce availability changes

### Notifications

Configure alerts for worker issues:

```yaml
notifications:
  channels:
    - type: slack
      events:
        - worker.offline
        - worker.online
```

## Best Practices

1. **Run multiple workers** — Redundancy and load distribution
2. **Match capabilities to needs** — GPU workers for ML
3. **Monitor health** — Alert on offline workers
4. **Plan capacity** — Ensure workers for expected load
5. **Secure workers** — Limit network access

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
