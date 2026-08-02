# Payments

**Payments** in Bahia track costs for deployment runs, worker usage, and resource consumption.

## Overview

Payments documentation currently covers the web and MCP surfaces. Bahia does not currently register a top-level `bahia payments` CLI command.

Payment features include:
- **Cost estimation** — Predict deployment costs
- **Usage tracking** — Record actual consumption
- **Payment history** — View past transactions
- **Worker pricing** — Per-worker cost models

## Key Concepts

### Cost Estimate

A **Cost Estimate** predicts deployment cost:

```yaml
service_id: "svc-123"
environment_id: "env-456"
worker_pubkey: "npub1worker..."
estimated_cost:
  amount: 1000
  currency: "sats"
breakdown:
  base: 500
  compute_minutes: 300
  storage_mb: 200
```

### Payment Record

A **Payment Record** tracks actual cost:

```yaml
run_id: "run-789"
worker_pubkey: "npub1worker..."
amount: 950
currency: "sats"
status: "completed"
paid_at: "2024-01-15T10:30:00Z"
```

## Estimating Costs

### Before Deployment

Use the web UI or the `bahia_estimate_cost` MCP tool. The current CLI does not register `bahia payments estimate`.

### MCP Tool

```json
{
  "tool": "bahia_estimate_cost",
  "arguments": {
    "service_id": "svc-123",
    "environment_id": "env-456",
    "artifact_id": "art-789"
  }
}
```

### Estimate Response

```json
{
  "estimated_cost": {
    "amount": 1000,
    "currency": "sats"
  },
  "breakdown": {
    "base_cost": 500,
    "compute_minutes": 10,
    "compute_cost": 300,
    "storage_mb": 100,
    "storage_cost": 200
  },
  "worker": {
    "pubkey": "npub1worker...",
    "pricing_model": "standard"
  }
}
```

## Viewing Costs

### Run Cost

After deployment, use the web UI or the `bahia_get_run_cost` MCP tool. The current CLI does not register `bahia payments cost`.

### Web UI

1. Go to deployment run detail
2. View **Cost** section
3. See breakdown and payment status

### MCP Tool

```json
{
  "tool": "bahia_get_run_cost",
  "arguments": {
    "run_id": "run-789"
  }
}
```

## Payment History

### Web UI

Navigate to **Payments** in the sidebar:
- View transaction history
- Filter by worker, date, status
- See totals and trends

### Web transport and MCP tool

The browser requires encrypted-operation capability and uses the encrypted `payments.history` operation (the backend also accepts the `payments/history` alias). The server caps the requested limit at 250.

The MCP tool is a separate authenticated per-tool call; MCP transport does not make this tool an encrypted ContextVM request:

```json
{
  "tool": "bahia_get_payment_history",
  "arguments": {
    "worker_pubkey": "npub1worker...",
    "limit": 50
  }
}
```

## Worker Pricing

### Viewing Worker Pricing

Worker pricing is currently exposed through the web UI and payment/read-model surfaces; the current CLI does not register `bahia workers pricing`.

### Pricing Models

Workers can define pricing:

```yaml
pricing:
  model: "standard"
  base_cost: 100        # sats per run
  per_minute: 10        # sats per minute
  per_mb_storage: 1     # sats per MB
  gpu_multiplier: 2.0   # multiplier for GPU tasks
  currency: "sats"
```

### Pricing Tiers

| Tier | Description | Typical Rate |
|------|-------------|--------------|
| `free` | No charge | 0 sats |
| `standard` | Normal pricing | 100-500 sats |
| `premium` | Priority/GPU | 500-2000 sats |
| `enterprise` | Custom | Negotiated |

## Cost Breakdown

### Compute Costs

Based on execution time:
- CPU minutes
- GPU minutes (if applicable)
- Memory hours

### Storage Costs

Based on data transferred:
- Image pull size
- Artifact storage
- Log storage

### Base Costs

Fixed per-run costs:
- Job scheduling
- Infrastructure overhead

## Payment Status

| Status | Description |
|--------|-------------|
| `pending` | Awaiting payment |
| `processing` | Payment in progress |
| `completed` | Successfully paid |
| `failed` | Payment failed |
| `refunded` | Payment reversed |

## Currency

Bahia primarily uses **sats** (satoshis):
- 1 sat = 0.00000001 BTC
- Lightning-oriented pricing/history surfaces are documented here
- Cashu mint-backed token flows are not currently implemented and enabling them fails configuration validation

## Encrypted Operations

Payment data is sensitive:
- History is accessed via encrypted Nostr (`5980` requests / `7980` terminal results)
- Requires a NIP-44 capable signer
- Requires Bahia discovery to advertise `features.encrypted_nostr_requests` so browser-safe relays and the backend encrypted transport are available
- Not published to public relays

## Best Practices

1. **Estimate before deploying** — Know costs upfront
2. **Monitor spending** — Track trends over time
3. **Choose workers wisely** — Balance cost and capability
4. **Set budgets** — Alert on spending thresholds
5. **Review regularly** — Audit payment history

## Troubleshooting

### Cost Higher Than Estimated

- Check actual runtime vs estimated
- Review storage usage
- Check for retries/failures

### Payment Failed

- Verify payment method
- Check worker connectivity
- Review error details

### Missing History

- Check date range
- Verify worker filter
- Ensure encrypted access configured

## Related

- [Workers](workers.md) — Worker pricing
- [Deployments](deployments.md) — Cost sources
- [Organizations](organizations.md) — Billing scope
