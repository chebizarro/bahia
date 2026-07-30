# Bahia WS6 observability rollout

This is the production inventory and rollout checklist for Bahia's WS6
Prometheus, Alertmanager, and Grafana stack. It deliberately uses internal
service discovery; monitoring ports must not be published to the public
interface.

## Scrape inventory

| Target | Internal endpoint | Metrics | Required |
|---|---|---|---|
| Bahia control plane | `bahia:8080/metrics` | Fleet health, drift, workers, relays, audit, authorization, runtime operations | Yes |
| Routstr gateway | `fleet-routstr-gateway:<port>/metrics` | Requests, spend, wallet, routing | When `fp-397` is deployed |
| Loom | deployment-specific `/metrics` | Job lifecycle | When an authenticated internal endpoint exists |

The initial production target is Bahia only. On `edge-01`, Bahia is currently
published on host port 8080, but Prometheus must use `bahia:8080` on the
internal Compose network. Host-published ports are diagnostic facts, not
stable scrape addresses. Add optional targets only after their service name,
port, ownership, and label cardinality have been verified.

Do not place agent IDs, event IDs, pubkeys, free-form errors, repository names,
URLs, or secrets in target labels. Use bounded labels such as `environment`
and `service`.

## Pre-deployment

- [ ] Build and pin a Bahia image containing the `fp-obs-1` metrics.
- [ ] Validate `deploy/observability/prometheus.yml` and
      `deploy/observability/bahia-alerts.yml` with `promtool`.
- [ ] Run `deploy/observability/bahia-alerts.test.yml`.
- [ ] Confirm `/metrics` returns all zero-valued fleet-health series before
      relying on absence-based alerts.
- [ ] Create persistent volumes for Prometheus, Alertmanager, and Grafana.
- [ ] Attach all four services to a private monitoring network.
- [ ] Keep Prometheus and Alertmanager unexposed; expose Grafana only through
      the authenticated reverse proxy.
- [ ] Configure Grafana's `PROMETHEUS_URL` with the internal Prometheus URL.
- [ ] Configure the Alertmanager receiver to the Signet-backed NIP-29 adapter;
      never load an nsec into Alertmanager.
- [ ] Verify the adapter is authorized for the authenticated `incidents`
      group and validates Alertmanager webhook authentication.

## Rollout

1. Deploy Prometheus with the checked-in scrape configuration and rules.
2. Verify target `bahia:8080` is `UP` and inspect label cardinality.
3. Deploy Alertmanager and confirm Prometheus reports it as reachable.
4. Provision Grafana with the checked-in datasource and dashboard bundle.
5. Confirm dashboard UID `bahia-fleet-health-v1` loads without missing-series
   or unknown-metric errors.
6. Send a uniquely labelled synthetic test alert through Alertmanager.
7. Verify exactly one signed NIP-29 kind-9 message arrives in `incidents`,
   including the alert name, environment, severity, start time, and runbook
   reference, without secrets or raw event content.
8. Resolve the synthetic alert and verify the resolution path does not
   duplicate the firing notification.

## Acceptance evidence

Capture:

- pinned image digests and deployed configuration hashes;
- Prometheus configuration/rule validation output;
- the Bahia target health record and scrape timestamp;
- Grafana dashboard UID and datasource health;
- Alertmanager receiver status;
- synthetic alert fingerprint;
- signed NIP-29 event ID and signature verification;
- proof that the message is visible only to authenticated `incidents` members.

Do not use screenshots as the only evidence. Record machine-verifiable hashes,
event IDs, and command output with secrets redacted.

## Rollback

Stop the monitoring containers and restore the previous Compose/configuration
bundle. Monitoring rollback must not restart Bahia or mutate its desired state.
Preserve Prometheus and Alertmanager volumes until acceptance evidence and any
failed-delivery investigation are complete.

