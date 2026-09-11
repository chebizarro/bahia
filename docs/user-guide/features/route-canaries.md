# Route Canaries and Outage Detection

Route canaries verify that a Bahia-managed route actually serves traffic, from the perspectives that matter to its users.

## Why this exists

Converging a routing provider only proves that configuration was *accepted*. It does not prove the route *works*.

A live outage on `git.sharegap.net` made the gap concrete. Gitea and grasp-bridge were both healthy at the container level. Nginx accepted its configuration and reloaded successfully. Every health signal Bahia had was green. But `nginx-git` had cached a stale Docker upstream IP after grasp-bridge was recreated, so every user request returned HTTP 502 until nginx was reloaded by hand.

Nothing in the control plane was watching the thing the user actually touches. Route canaries are that missing signal.

## What a canary checks

Each managed route is probed from one or two perspectives:

| Perspective | What it proves |
|---|---|
| `public_edge` | The route resolves in public DNS and serves valid, trusted HTTPS through the edge provider - what an internet user experiences. |
| `internal_lan` | The route serves correctly from inside the LAN under split DNS, dialing the configured internal address while keeping the canonical hostname for TLS and `Host`. |

The internal perspective is what catches a stale nginx upstream: the public edge can look fine, or be serving a cached response, while the internal vhost is broken.

Every probe asserts:

- **DNS resolution** from the configured resolver
- **TLS validity** - the certificate chain must verify for the requested hostname. Verification is never disabled.
- **Response status** within the configured expected range
- **Response body** contains the configured marker, when one is set
- **Certificate expiry** warning when the leaf expires within the configured window

## Classifications

Failures are classified by the outermost layer that broke, so you see the most actionable cause rather than a downstream symptom:

| Classification | Meaning |
|---|---|
| `route_ok` | Every configured assertion held. |
| `dns_unresolved` | The hostname did not resolve from this perspective. |
| `tls_invalid` | The certificate chain does not verify for this hostname. |
| `connect_failed` | The address resolved but no HTTP response came back. |
| `upstream_error` | **The route is published and the edge is reachable, but the origin returned 502, 503, or 504.** This is the stale-or-unreachable-upstream signature. |
| `status_mismatch` | The response status fell outside the expected range. |
| `body_mismatch` | Status was acceptable but the expected body marker was absent. |
| `tls_expiring` | The route works, but the certificate expires soon. This is a **warning**: it never opens an outage and never blocks a deployment. |
| `health_path_not_discriminating` | The health path returned the same response as a deliberately bogus control path, so the check proves only that *something* answered. A **warning** by default. |

### Catch-all routes and why the health path may prove nothing

A single-page application typically serves its shell with HTTP 200 for **every**
path, including paths that do not exist. Against such a service a health-path
check is close to vacuous: a wrong path, a stale upstream still serving the
shell, and a perfectly healthy service all look identical.

This was found live on 2026-09-07. `astillero.sharegap.net/health` returned a
byte-identical body to a randomly generated garbage path, meaning the configured
health path was not a health endpoint at all — it was the SPA catch-all.

Set `detect_catch_all: true` and each probe additionally requests a random
control path that nothing should serve. If the health response is
indistinguishable from it — same status *and* same body — the canary reports
`health_path_not_discriminating` instead of implying a confidence it does not
have.

Any difference at all counts as discriminating, so applications that return the
same status for everything but vary their content are not flagged.

This is a **warning** by default, because serving a catch-all is legitimate and
promoting it to an outage would page operators about working routes and roll
back healthy deployments. Two ways to make the check meaningful:

- point the route at a real health endpoint, or
- set `expected_body_contains` to something only the intended application
  returns — an explicit body assertion is real evidence and suppresses the
  warning.

Set `require_discriminating_health_path: true` to promote it to a failure, which
also makes it **block deployments**. Note this will block a route whose health
path is a catch-all even when that route is serving correctly — which is the
point, but it is disruptive, so stage it.

### Service healthy, route broken

Route canary state is keyed by the same service/environment/deployment-unit coordinate as managed instance health. The API reports both together and sets `service_healthy_route_broken` when a route outage is open while the containers behind it are healthy or running.

That flag is the exact condition that had no signal before: it means the problem is in the routing layer, not the application.

## Post-deploy gate

When `gate_enabled` is set, a deployment that attaches or changes a route must prove the route serves traffic before the run may succeed.

1. The route plan is applied to the providers.
2. The route is probed from every configured perspective, retrying until it passes or the gate deadline expires. Routes legitimately take time to propagate, so the gate retries - but it never concludes success merely because time elapsed.
3. If the route still does not serve, **the route is rolled back** through the routing backend's own compensation and the deployment fails with the classification named.

Rollback runs on a context that outlives a cancelled deployment, so cancelling a run cannot strand a route that was already proven broken.

A failure that occurs while *applying* to the provider is reported as a provider failure, not a canary failure - the gate does not probe or roll back a route whose apply never succeeded.

### What the gate can and cannot catch

The gate blocks a route that does not **serve**: bad status, upstream 502/503/504,
DNS failure, untrusted TLS, missing body marker.

It does not, by default, catch a route that serves the *wrong thing* with a 200.
Against a catch-all application a wrong health path still returns 200, so the
gate will correctly allow it. If you need a misconfigured health path to be
caught, enable `detect_catch_all` together with
`require_discriminating_health_path`, or set `expected_body_contains`.

If the policy derives no targets for a route, the gate has verified nothing. It
allows the deployment but logs a warning at `route-canary-gate`, so
"checked and healthy" is never silently confused with "checked nothing".

## Periodic probing

Independently of deployments, every managed route is re-probed on an interval. Routes are enumerated from **desired state**, not from live provider configuration:

- a route withdrawn from desired state stops being probed, so it cannot alarm forever;
- a route that the provider has lost is still probed, so its disappearance is detected.

Outages open and clear with hysteresis. A failure streak counts *any* failing classification, not a single class, so a route flapping between `dns_unresolved` and `upstream_error` cannot evade the threshold.

## Configuration

```yaml
route_canaries:
  enabled: true
  # Periodic probing
  interval: 60s
  probe_timeout: 15s
  failure_threshold: 3      # consecutive failures that open an outage
  success_threshold: 2      # consecutive successes that clear one
  # Post-deploy gate
  gate_enabled: true
  gate_timeout: 90s
  gate_retry_interval: 3s
  # Assertions
  expected_status_min: 200
  expected_status_max: 299
  expected_body_contains: ""
  tls_min_days_remaining: 14
  # Also request a random control path each probe. If the health path answers
  # identically, report health_path_not_discriminating instead of route_ok.
  detect_catch_all: true
  # Promote that warning to a failure, which also blocks deployments.
  # Requires detect_catch_all.
  require_discriminating_health_path: false
  # Public-edge checks use this resolver so they cannot be satisfied by
  # split-horizon LAN DNS. Use "system" for the host resolver.
  public_resolver: "1.1.1.1:53"
  # Zone -> LAN address. Enables the internal_lan perspective for that zone.
  internal_dial_addresses:
    sharegap.net: "192.168.40.10"
```

`route_canaries.enabled` requires `edge_routing.enabled`. `internal_dial_addresses` requires `internal_routing.enabled` - mapping a zone that internal routing does not serve would probe a host that never carries the vhost, so it is rejected at startup rather than silently probing the wrong thing.

Canary settings are control-plane policy, not signed desired state. Changing an expectation does not invalidate an already-deployed route plan hash.

## API

All endpoints are tier-2 gated.

| Method | Path | Returns |
|---|---|---|
| `GET` | `/api/v1/route-canaries` | All route canary state. Filters: `service_id`, `environment_id`, `open`. |
| `GET` | `/api/v1/services/{serviceId}/environments/{envId}/routes/{hostname}/canary` | State for one route. |
| `GET` | `/api/v1/services/{serviceId}/environments/{envId}/routes/{hostname}/canary/events` | Append-only failure lineage, newest first. `limit` up to 500. |

Add `?deployment_unit_id=<uuid>` when a service has more than one managed route per environment.

Example:

```bash
# When auth.enabled is true, send a NIP-98 signed Authorization header
curl -H "Authorization: Nostr <base64-signed-nip98-event>" \
  "https://bahia.example/api/v1/route-canaries?open=true"
```

```json
{
  "data": [
    {
      "service_id": "...",
      "environment_id": "...",
      "hostname": "git.sharegap.net",
      "open": true,
      "classification": "upstream_error",
      "perspective": "internal_lan",
      "consecutive_failures": 3,
      "failure_reason": "internal_lan GET https://git.sharegap.net/healthz: route published and edge reachable but upstream returned HTTP 502; origin is stale or unreachable",
      "opened_at": "2026-09-07T01:12:44Z",
      "observed_instance_status": "healthy",
      "service_healthy_route_broken": true
    }
  ]
}
```

## Events and alerting

| Event | Severity | When |
|---|---|---|
| `route.canary_outage_opened` | critical | A route outage is declared. |
| `route.canary_recovered` | info | An open outage clears. |
| `route.canary_classification_changed` | error / warning | The route is still failing but for a different reason, or a warning appeared or cleared (including `health_path_not_discriminating`). |

Payloads carry the container-level status observed at the same moment, so a notification about a broken route also tells you whether the service behind it was fine.

## Evidence and secrets

Response bodies are bounded and passed through evidence sanitization before being stored or published, so credentials echoed by a broken upstream never reach durable state.

Body assertions are matched against the raw bounded body rather than the sanitized one, so redaction can never silently change whether an assertion passes.
