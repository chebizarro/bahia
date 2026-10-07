# Route Canaries

**Route Canaries** (`/route-canaries`) verify that a managed hostname works from the perspectives that users depend on.

## Checks

A route can have public-edge and internal-LAN probes. Each probe can verify DNS resolution, trusted TLS for the hostname, response status, a bounded body marker or anchored RE2 expression, and certificate lifetime.

Classifications include:

| Result | Meaning |
|---|---|
| `route_ok` | All configured assertions passed |
| `dns_unresolved` | The hostname did not resolve |
| `tls_invalid` | TLS verification failed |
| `connect_failed` | No HTTP response arrived |
| `upstream_error` | The route returned 502, 503, or 504 |
| `status_mismatch` | Status fell outside the expected range |
| `body_mismatch` | A configured body assertion failed |
| `tls_expiring` | The route works but the certificate is near expiry |
| `health_path_not_discriminating` | The configured path behaves like a catch-all path |

`tls_expiring` is a warning. Catch-all detection is also a warning unless `require_discriminating_health_path` is enabled.

## Deployment gate

When `gate_enabled`, a deployment that attaches or changes a route must pass its configured probes before the run succeeds. The gate retries until its deadline. Failure compensates the applied route through the routing backend and fails the deployment with the probe classification.

A gate with no derived targets reports that it verified nothing. It does not silently claim route health.

## Periodic probing

Periodic probes enumerate desired routes, not provider configuration. Outages open after the configured failure threshold and clear after the success threshold. A route removed from desired state stops probing; a desired route missing from the provider still fails.

The route record pairs canary status with managed instance health and marks `service_healthy_route_broken` when the service is healthy but its route is not.

If the database-backed secret store is unavailable, provider convergence that needs an API token is suspended, while route probing and relay-backed observables continue.

## Configuration

```yaml
route_canaries:
  enabled: true
  interval: 60s
  probe_timeout: 15s
  failure_threshold: 3
  success_threshold: 2
  gate_enabled: true
  gate_timeout: 90s
  gate_retry_interval: 3s
  expected_status_min: 200
  expected_status_max: 299
  expected_body_contains: ""
  expected_body_regex: ""
  tls_min_days_remaining: 14
  detect_catch_all: true
  require_discriminating_health_path: false
  public_resolver: "1.1.1.1:53"
  internal_dial_addresses:
    example.com: "192.0.2.10"
  overrides:
    api.example.com:
      expected_body_contains: '"status":"ok"'
```

`route_canaries.enabled` requires `edge_routing.enabled`. Internal dial addresses require `internal_routing.enabled`. Per-route overrides can change interval, timeout, expected status/body, TLS warning, and perspectives.

## Evidence and alerts

Canonical route state is a service-authored `30900` record at the route coordinate. `30315` status and `4903` audit facts carry bounded transitions and sanitized evidence. Secret values and response bodies are not published.

Use notification channels for outage open, change, and clear events. Treat stale probe evidence as unknown.

## Related

- [Managed DNS and HTTPS Routes](../guides/managed-dns-and-https-routes.md)
- [Instance Health](instance-health.md)
- [Notifications](notifications.md)
