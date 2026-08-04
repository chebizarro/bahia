# Bahia WS6 alerts

The checked-in Prometheus rules live at
`deploy/observability/bahia-alerts.yml`. Validate them with:

```sh
promtool check rules deploy/observability/bahia-alerts.yml
promtool test rules deploy/observability/bahia-alerts.test.yml
```

Alertmanager-to-Nostr delivery terminates at the `alerting.Dispatcher` contract.
Production must supply a Signet-backed publisher that emits a NIP-29 kind-9
message to group `incidents`. Source tests use dry-run mode and never load a
signing key or publish an event. Deploying the publisher, configuring the
authenticated NIP-29 group, and routing Alertmanager webhooks are Track B
operations and are not implied by these source fixtures.

## Detection and response matrix

| Alert | Detection evidence | First responder | Escalation | Immediate safe action |
|---|---|---|---|---|
| `BahiaWorkerHeartbeatStale` | Worker heartbeat lag exceeds 300 seconds for five minutes | Fleet operator | Tier 1 | Inspect worker and relay health; stop assigning new work if freshness continues degrading |
| `BahiaWorkerDown` | Worker heartbeat lag exceeds 1,800 seconds for five minutes | Fleet operator | Tier 2 | Cordon the worker and identify restart-safe assignments; do not restart workloads blindly |
| `BahiaDriftStuck` | At least one drifted service state is older than the threshold or has reconciliation failures | Service owner | Tier 2 | Freeze additional promotion for the affected service and compare desired/observed state |
| `BahiaWorkerResourcePressure` | Bahia recommends operator intervention for worker pressure | Host owner | Tier 1, Tier 2 if continuity capacity is affected | Cordon new placements; inspect reclaimable disk, VRAM, memory, and thermal state |
| `BahiaRelayDegraded` | One or more configured relays are degraded or unhealthy | Relay operator | Tier 1 | Preserve multi-relay publishing and verify relay acknowledgements; do not infer delivery from a socket connection |
| `BahiaAudit4903Anomaly` | A rejected or contradictory kind-4903 event increments the anomaly counter | Security/operator pair | Tier 3 | Preserve the event chain and pause correlated mutations pending signature/correlation review |
| `BahiaAuthorizationRejectionSpike` | More than ten bounded authorization rejections occur within five minutes | Security operator | Tier 2 | Inspect identity, policy, replay, and signature reason counts; do not loosen policy |
| `BahiaTierRejectionSpike` | More than five insufficient-tier rejections occur within five minutes | Bahia operator | Tier 1 | Compare requested and active tier and restore the failed dependency instead of bypassing the gate |
| `NodeExporterDown` | An expected-up node scrape fails for five minutes | Host owner | Tier 1 | Check the exporter service and monitoring-interface route; do not infer host failure from exporter failure alone |
| `HostMemoryPressure` | Available host memory remains below 10% for ten minutes | Host owner | Tier 1 | Inspect workload pressure and preserve continuity capacity before evicting work |
| `HostFilesystemPressure` | A writable filesystem remains below 10% free for ten minutes | Host owner | Tier 1 | Identify reclaimable data and use approved cleanup policy; do not delete manually |
| `LemmyGPUExporterDown` | Lemmy's GPU scrape fails for five minutes | Lemmy operator | Tier 1 | Check the exporter and `nvidia-smi`; keep exporter failure distinct from GPU failure |
| `LemmyGPUMemoryPressure` | A Lemmy GPU remains above 90% allocated VRAM for ten minutes | Lemmy operator | Tier 1 | Stop new GPU placement and inspect the owning inference workload |

## Non-mutating detection simulations

These commands validate rule evaluation only. They do not contact production,
restart a service, publish a Nostr event, or require credentials:

```sh
# Covers worker-stale/down, drift-stuck, resource-pressure, relay, audit,
# authorization, and tier-rejection samples.
docker run --rm \
  -v "$PWD/deploy/observability:/rules" \
  -w /rules \
  --entrypoint /bin/promtool \
  prom/prometheus:latest \
  test rules bahia-alerts.test.yml

# Inspect exactly which production series the rules consume.
docker run --rm \
  -v "$PWD/deploy/observability:/rules" \
  --entrypoint /bin/promtool \
  prom/prometheus:latest \
  check rules /rules/bahia-alerts.yml

# Prove incident rendering does not publish in dry-run mode.
go test ./internal/adapters/alerting -run TestDispatcherDryRunRendersWithoutPublishing
```

The commands above are Track A source verification. Track B acceptance is
separate and requires Prometheus to scrape the deployed Bahia `/metrics`,
Alertmanager to load the checked-in rules, and a Signet-backed adapter to
deliver a test alert to the authenticated NIP-29 `incidents` group. A successful
Track A fixture is not evidence that production delivery is configured.

## BahiaWorkerHeartbeatStale

Confirm the worker process and its relay connection before rescheduling work.

## BahiaWorkerDown

Cordon the worker, inspect its runtime, then reassign only restart-safe work.

## BahiaDriftStuck

Inspect desired and observed state plus reconciliation failures before applying
or rolling back.

## BahiaWorkerResourcePressure

Inspect disk, VRAM, memory, thermal, and queue telemetry. Prefer cleanup or
cordoning before moving continuity-critical workloads.

## BahiaRelayDegraded

Verify relay connectivity and publish acknowledgements across the configured
relay set. Do not infer delivery from WebSocket connection state alone.

## BahiaAudit4903Anomaly

Preserve the conflicting audit events and verify their signatures and
correlation chain before accepting further mutations.

## BahiaAuthorizationRejectionSpike

Check caller identity, policy changes, and replay protection. Do not weaken the
authorization boundary merely to clear the alert.

## BahiaTierRejectionSpike

Inspect dependency health and Bahia's requested versus active tier. Restore the
dependency instead of bypassing tier gates.

## NodeExporterDown

Verify `prometheus-node-exporter` is active and listening only on the inventory address. Test the route from the Prometheus host. The alert describes observer failure and does not itself prove that the host is down.

## HostMemoryPressure

Inspect the host's workload and swap activity. Preserve fleet continuity reserve and cordon new placement before terminating workloads.

## HostFilesystemPressure

Identify the pressured mount and use Bahia's approved cleanup/quarantine workflow. Do not bypass signed maintenance approvals for destructive cleanup.

## LemmyGPUExporterDown

Verify `nvidia_gpu_exporter`, its bound listener, and `nvidia-smi`. Compare with Nostr presence before deciding whether Lemmy or merely the exporter is unavailable.

## LemmyGPUMemoryPressure

Inspect the GPU UUID and owning inference process. Stop new GPU placement and use the model lifecycle controls rather than killing arbitrary processes.
