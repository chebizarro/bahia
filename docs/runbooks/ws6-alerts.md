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
| `BahiaOpenClawProvisioningStageStuck` | A non-terminal provisioning stage remains older than 900 seconds for five minutes | Soul Factory operator | Tier 1 | Inspect the correlated run and dependency readiness; dry-run reconcile before mutation |
| `BahiaOpenClawProgressWithoutTerminal` | Progress reaches 100% without a terminal projection for five minutes | Soul Factory operator | Tier 2 | Treat the run as incomplete and trace the terminal publish plus relay acknowledgement |
| `BahiaOpenClawFalseRunning` | A run claims `running` without DM or terminal evidence for one minute | Soul Factory operator | Tier 2 | Disable new admission and inspect the DM gate and terminal lineage |
| `BahiaOpenClawOrphanCandidates` | Run-owned resources remain after compensation for five minutes | Soul Factory operator | Tier 2 | Freeze cleanup, verify ownership, and dry-run safe-abort |
| `BahiaOpenClawRepeatedSignetUnauthorized` | A run records at least three unauthorized Signet responses for five minutes | Signet operator | Tier 1 | Freeze policy changes and compare the exact client pubkey with the active policy revision |
| `BahiaOpenClawCorrelationMismatch` | A provisioning correlation mismatch persists for one minute | Soul Factory operator | Tier 2 | Stop the affected run, preserve evidence, and inspect request/run/resource lineage |
| `NodeExporterDown` | An expected-up node scrape fails for five minutes | Host owner | Tier 1 | Check the exporter service and monitoring-interface route; do not infer host failure from exporter failure alone |
| `HostMemoryPressure` | Available host memory remains below 10% for ten minutes | Host owner | Tier 1 | Inspect workload pressure and preserve continuity capacity before evicting work |
| `HostFilesystemPressure` | A writable filesystem remains below 10% free for ten minutes | Host owner | Tier 1 | Identify reclaimable data and use approved cleanup policy; do not delete manually |
| `LemmyGPUExporterDown` | Lemmy's GPU scrape fails for five minutes | Lemmy operator | Tier 1 | Check the exporter and `nvidia-smi`; keep exporter failure distinct from GPU failure |
| `LemmyGPUMemoryPressure` | A Lemmy GPU remains above 90% allocated VRAM for ten minutes | Lemmy operator | Tier 1 | Stop new GPU placement and inspect the owning inference workload |

The `BahiaOpenClaw*` rules link to
[`openclaw-provisioning-operations.md#incident-and-alert-response`](openclaw-provisioning-operations.md#incident-and-alert-response)
as their `runbook_url`; the sections below are a summary.

> **NOTE (2026-09-11, updated):** The `bahia_openclaw_provisioning_*`
> series consumed by the `BahiaOpenClaw*` rules is appended to Bahia's
> `/metrics` when `soul_factory.openclaw_saga_store_dir` is configured
> (`internal/soulfactory/saga.Monitor` via the telemetry provider). When the
> key is unset the series is absent, so these six alerts are covered by rule
> unit tests only. Additionally, no binary currently drives the saga engine, so
> a configured store stays empty until bahia-lf0s4 is resolved; treat alert
> silence as unverified until a provisioning run has been observed.

## Non-mutating detection simulations

These commands validate rule evaluation only. They do not contact production,
restart a service, publish a Nostr event, or require credentials:

```sh
# Covers worker-stale/down, drift-stuck, resource-pressure, relay, audit,
# authorization, tier-rejection, and OpenClaw provisioning samples.
docker run --rm \
  -v "$PWD/deploy/observability:/rules" \
  -w /rules \
  --entrypoint /bin/promtool \
  prom/prometheus:latest \
  test rules bahia-alerts.test.yml

# Syntax-check the production rule file.
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

## BahiaOpenClawProvisioningStageStuck

Inspect the correlated request/run and the current stage's dependency. Run the
saga reconciliation path in dry-run mode before approving a mutation.

## BahiaOpenClawProgressWithoutTerminal

Treat 100% progress as incomplete until the correlated terminal projection is
present. Inspect the terminal publish attempt and relay `OK` evidence.

## BahiaOpenClawFalseRunning

Disable new provisioning admission for the affected scope and trace the DM
verification gate and terminal event lineage.

## BahiaOpenClawOrphanCandidates

Do not delete candidates merely to clear the alert. Verify run ownership and
use a dry-run safe-abort report before approving compensation.

## BahiaOpenClawRepeatedSignetUnauthorized

Freeze Signet policy changes. Compare the exact client pubkey and active policy
revision, then verify an expected denial and an authorized signing operation.

## BahiaOpenClawCorrelationMismatch

Stop mutation for the affected run, preserve public IDs and sanitized evidence,
and compare request, run, agent, and resource correlation before retrying.

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
