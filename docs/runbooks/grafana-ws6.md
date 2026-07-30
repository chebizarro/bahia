# Bahia WS6 Grafana bundle

The dashboard has stable UID `bahia-fleet-health-v1` and covers service and
deployment health, drift, worker capacity and pressure, relay health, Loom
jobs, Cashu/Routstr spend, and adoption/runtime operations.

Mount or copy:

- `deploy/observability/grafana/provisioning` to Grafana's provisioning path;
- `deploy/observability/grafana/dashboards` to `/var/lib/grafana/dashboards`.

Set `PROMETHEUS_URL` in the deployment environment to the internal Prometheus
endpoint. The checked-in files contain no deployment hostname, bearer token, or
other secret. Prometheus owns scrape authentication; Grafana reads Prometheus
through the provisioned proxy datasource.

Validate the dashboard metric contract with:

```sh
go test ./internal/adapters/telemetry -run TestGrafanaFleetHealthDashboardReferencesCataloguedMetrics
```

OwnAuth/Grafana SSO remains under `fp-own`/`fp-46`; this bundle neither configures
nor bypasses authentication.
