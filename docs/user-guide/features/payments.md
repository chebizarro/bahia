# Payments

**Payments** (`/payments`) shows worker pricing, cost estimates, run cost, and payment history.

## Estimate cost

Use `bahia_estimate_cost` with the worker or capability selection and requested resources. The estimate reports the applicable pricing model and a cost breakdown. It is planning evidence, not a charge guarantee; actual duration and resources determine final run cost.

`bahia_get_worker_pricing` exposes the worker's advertised pricing data. Do not assume a fixed currency, tier, or unit unless it appears in the signed response.

## Run cost and history

- `bahia_get_run_cost` returns the canonical cost record for a deployment run.
- `bahia_get_payment_history` returns the visible payment records.
- Authenticated HTTP reads are available at `/api/v1/deployments/runs/{id}/cost` and `/api/v1/payments/history` when payment projection is configured. These reads come from the local canonical event store and do not require PostgreSQL.

Payment records are service-authored `30900` records with topic `payment-record` and are encrypted with the fleet OCK. The browser decrypts them in memory through the active signer. A signer without the fleet key sees **not readable with this key**.

## Reading results

Keep estimate, accrued cost, settlement, and payment status distinct. A completed run can have delayed cost or payment evidence. Missing history is unknown until relays reach EOSE.

If a payment method is unavailable or a settlement fails, the record reports the failure; Bahia does not mark it paid because the workload completed.

## Related

- [Workers](workers.md)
- [Deployments](deployments.md)
- [Organizations](organizations.md)
