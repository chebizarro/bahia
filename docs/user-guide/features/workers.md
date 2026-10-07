# Workers

Workers are execution nodes that advertise capabilities, pricing, and availability and receive governed work. Bahia separates worker-authored capability evidence from service-authored scheduling and lifecycle state.

## Discovery and state

Loom workers publish advertisements as kind `10100`. Bahia accepts advertisements only from known worker pubkeys and verifies their signatures and freshness. Service-authored `30900` records hold worker state, assignments, drain progress, eligibility decisions, and operator labels.

Open **Workers** (`/workers`) or use:

```bash
bahia workers list
bahia workers show <worker-pubkey>
```

Without relay EOSE, the CLI displays its cached snapshot with a stale-data warning.

## Operator controls

```bash
bahia workers cordon <worker-pubkey> --reason "maintenance"
bahia workers drain <worker-pubkey> --reason "kernel update"
bahia workers maintenance-enter <worker-pubkey> --reason "host work"
bahia workers labels-update <worker-pubkey> --labels '{"gpu":"a100"}'
bahia workers cleanup <worker-pubkey> --mode reclaimable_only
```

Matching uncordon, undrain, and maintenance-exit commands restore eligibility when current capability evidence permits it. Signed worker intents remain pending until correlated status or newer canonical state arrives.

MCP exposes worker list/get, lifecycle controls, label update, eligibility preview, assignment reads, drain-status reads, pricing, and cost estimation. See [MCP Tools](../mcp-tools.md).

## Scheduling

The scheduler filters by required capabilities, labels, runtime, maintenance, cordon/drain state, capacity, and freshness. Desired capability labels are not proof of capability; authenticated worker evidence is required. An offline or stale worker is ineligible.

Assignments are individually addressable and idempotent. Draining stops new assignments and tracks outstanding work; it does not claim that running work disappeared.

## Pricing

Worker pricing is signed advertised data. `bahia_get_worker_pricing` and `bahia_estimate_cost` expose it. Review the returned currency, unit, and resource basis instead of assuming a fixed price model.

## Health

Worker advertisements and health evidence have bounded freshness. Investigate relay connectivity, signer identity, and capability filters when a worker disappears. For resource pressure and cleanup, see [Fleet Health](fleet-health.md).

## Related

- [Deployments](deployments.md)
- [ML Models](ml-models.md)
- [Payments](payments.md)
- [Fleet Health](fleet-health.md)
