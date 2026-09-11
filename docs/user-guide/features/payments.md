# Payments

**Payments** in Bahia record worker payments for deployment runs and estimate run costs from worker pricing.

## Overview

Payments documentation covers the web and MCP surfaces. Bahia does not currently register a top-level `bahia payments` CLI command.

Payment features include:
- **Cost estimation** — Estimate a run's cost from the assigned worker's advertised pricing
- **Payment history** — View payment and change records per run and worker
- **Worker pricing** — Per-worker pricing tiers from Loom worker advertisements

> Cashu mint-backed token flows are not implemented. Setting `cashu.enabled: true` fails configuration validation.

## Key Concepts

### Worker pricing

Workers advertise pricing tiers in their kind `10100` advertisement:

```yaml
pricing:
  - mint_url: "https://mint.example.com"
    price_per_second: 1
    unit: "sat"
```

See [Workers](workers.md) (**Pricing Tiers** on the worker detail page).

### Cost Estimate

A **Cost Estimate** is `price_per_second × estimated_secs` for the assigned worker:

```yaml
worker_pubkey: "<worker-pubkey-hex>"
worker_name: "prod-worker-1"
mint_url: "https://mint.example.com"
price_per_second: 1
estimated_secs: 600
estimated_cost_sats: 600
unit: "sat"
```

### Payment Record

A **Payment Record** tracks one payment or change transfer for a deployment run:

```yaml
id: "<uuid>"
deployment_run_id: "run-789"
worker_pubkey: "<worker-pubkey-hex>"
mint_url: "https://mint.example.com"
amount_sats: 600
direction: "payment"   # payment or change
status: "sent"         # pending, sent, redeemed, failed, refunded
token_hash: "..."      # idempotency hash, when present
created_at: "2024-01-15T10:30:00Z"
```

## Estimating Costs

### Web UI

The service **Deploy** wizard shows a **Cost Estimate** step before you sign the deployment.

### MCP Tool

`bahia_estimate_cost` estimates the cost for a deployment run based on its assigned worker's pricing. `run_id` is required; `estimated_duration_secs` is optional.

```json
{
  "tool": "bahia_estimate_cost",
  "arguments": {
    "run_id": "run-789",
    "estimated_duration_secs": 600
  }
}
```

`bahia_get_run_cost` returns the recorded cost for a run:

```json
{
  "tool": "bahia_get_run_cost",
  "arguments": {
    "run_id": "run-789"
  }
}
```

The REST compatibility reads are `GET /api/v1/payments/estimate`, `GET /api/v1/payments/history`, and `GET /api/v1/deployments/runs/{id}/cost`.

## Payment History

### Web UI

Navigate to **Payments** in the sidebar. The table shows **Created**, **Amount**, **Status**, **Direction**, **Worker**, **Run**, **Mint**, and **Token Hash**, with status and direction filters and a page size of up to 250.

### Web transport and MCP tool

The browser requires encrypted-operation capability and uses the encrypted `payments.history` operation (the backend also accepts the `payments/history` alias). The server caps the requested limit at 250.

The MCP tool is a separate authenticated per-tool call; MCP transport does not make this tool an encrypted ContextVM request:

```json
{
  "tool": "bahia_get_payment_history",
  "arguments": {
    "worker_pubkey": "<worker-pubkey-hex>",
    "limit": 50
  }
}
```

## Payment Status

| Status | Description |
|--------|-------------|
| `pending` | Awaiting payment |
| `sent` | Payment sent to the worker |
| `redeemed` | Worker redeemed the payment |
| `failed` | Payment failed (see `error_message`) |
| `refunded` | Payment reversed |

## Encrypted Operations

Payment data is sensitive:
- Browser history requests use encrypted ContextVM messages (kind `25910` wrapped in NIP-59 `1059`/`21059`); the legacy `5980`/`7980` kinds are migration inputs only
- Requires a NIP-44 capable signer
- Requires Bahia discovery to advertise encrypted request support so browser-safe relays and the backend encrypted transport are available
- Not published to public relays

## Troubleshooting

### Missing History

- Check the status and direction filters
- Verify the worker filter
- Ensure encrypted access is configured

### No Cost Estimate

- The run must have an assigned worker that advertises pricing
- Check the worker's **Pricing Tiers** on its detail page

## Related

- [Workers](workers.md) — Worker pricing
- [Deployments](deployments.md) — Cost sources
- [Organizations](organizations.md) — Billing scope
