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
| `BahiaNostrOutboxFailed` | One or more outbound Nostr events were abandoned (`bahia_nostr_outbox_failed > 0`) | Relay operator | Tier 1 | Read `last_publish_error` of the failed rows; fix the relay policy or event before re-producing it; do not reset rows to pending |
| `BahiaAudit4903Anomaly` | A rejected or contradictory kind-4903 event increments the anomaly counter | Security/operator pair | Tier 3 | Preserve the event chain and pause correlated mutations pending signature/correlation review |
| `BahiaAuthorizationRejectionSpike` | More than ten bounded authorization rejections occur within five minutes | Security operator | Tier 2 | Inspect identity, policy, replay, and signature reason counts; do not loosen policy |
| `BahiaTierRejectionSpike` | More than five insufficient-tier rejections occur within five minutes | Bahia operator | Tier 1 | Compare requested and active tier and restore the failed dependency instead of bypassing the gate |
| `BahiaSoulFactoryRelayReadRejected` | A fail-closed SoulFactory relay read keeps ending without EOSE from enough relays (rejected partial reads for ten minutes) | SoulFactory operator | Tier 1, Tier 2 if lifecycle or fleet work is blocked | Identify the silent or CLOSED relays for the named caller and restore them; do not relax the read policy |
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

## BahiaNostrOutboxFailed

The publish outbox gave up on at least one signed event: every write relay
rejected it permanently (`blocked:`, `invalid:`, `pow:`) or its attempt budget
ran out without reaching the publish quorum. The gauge counts
`publish_state = 'failed'` rows in `nostr_events`, which stay until archived,
so the alert keeps firing after the cause is fixed. Acknowledge it once each row
is accounted for. It reads `-1`, and does not fire, until `bahia-event-archive
ensure-indexes` has built `idx_nostr_events_publish_failed`.

```sql
SELECT id, kind, entity_type, publish_target, last_publish_error, received_at
FROM nostr_events WHERE publish_state = 'failed' ORDER BY received_at DESC LIMIT 50;
```

Callers already recorded the terminal outcome: Security publications and their
runs are `failed_terminal`, SBOM manifests are `failed`, config-fabric versions
are excluded from desired state, and projector coordinates are re-signed on the
next repair. Fix the relay policy or the event, then re-produce the content (a
new signed event). Never set a failed row back to `pending`: relays that
rejected it permanently will reject the same event again. See
`docs/runbooks/nostr-event-store-lifecycle.md#publish-outbox-health`.

## BahiaAudit4903Anomaly

Preserve the conflicting audit events and verify their signatures and
correlation chain before accepting further mutations.

## BahiaAuthorizationRejectionSpike

Check caller identity, policy changes, and replay protection. Do not weaken the
authorization boundary merely to clear the alert.

## BahiaTierRejectionSpike

Inspect dependency health and Bahia's requested versus active tier. Restore the
dependency instead of bypassing tier gates.

## BahiaSoulFactoryRelayReadRejected

`bahia_soulfactory_relay_read_partial{outcome="rejected"}` counts SoulFactory
reads whose `RelayReadPolicy` refused a partial answer (a relay never sent
EOSE, or CLOSED the REQ). Fail-closed callers such as `reactor.get_soul`,
`reactor.fleet_reconcile_souls` and `communikeys.profile_list` return an error
instead of acting on possibly stale state, so lifecycle actions, fleet
reconciliation or provisioning grants stall while this fires. Use the `caller`
label and the SoulFactory logs ("relay stored events are incomplete") to
find the relays that did not answer, then restore or remove them from
`soul_factory.relays` / `additional_relays`. Do not switch a caller to a
partial-read policy to clear the alert: the policy table in
`internal/soulfactory/relay_read_policy.go` records why each caller fails
closed.

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
