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
