# Managed DNS and HTTPS Routes

This guide documents the signer-first operator flow for taking an existing Bahia-managed Docker or Compose service from artifact deployment to internal DNS and a managed external HTTPS hostname. The service must expose an HTTP health endpoint.

The worked example uses **Astillero** in `edge-01-production`:

- public hostname: `astillero.sharegap.net`
- existing origin: `http://127.0.0.1:18088`
- health endpoint: `/health`
- final user URL: `https://astillero.sharegap.net/` (no port)

Replace the example UUIDs and image coordinate with values from your environment. Every UUID below is a placeholder; no real credential is shown.

## 1. Configure managed DNS and edge routing

Bahia's internal service projection requires a writable DNS backend. This example uses dnsmasq, the deployable internal-LAN backend for mapping `edge-01-production` services into `sharegap.net`; it writes a managed configuration file and runs the configured reload command after an atomic update. The filesystem backend is not deployable because Bahia does not wire the operational activator its snapshots require. Choose dnsmasq, CoreDNS, PowerDNS, or FIPS instead.

```yaml
direct_runtime_actions:
  enabled: true

dns:
  enabled: true
  default_ttl: 300
  reconcile_interval: 1m
  zones:
    - name: sharegap.net
      visibility: internal
      backend: lan-dnsmasq
      ttl: 300
  backends:
    lan-dnsmasq:
      type: dnsmasq
      dnsmasq_config_dir: /etc/dnsmasq.d
      dnsmasq_reload_command: systemctl reload dnsmasq
      dnsmasq_file_prefix: bahia-
  projection:
    services: true
    environment_zones:
      edge-01-production: sharegap.net

edge_routing:
  enabled: true
  provider: cloudflare_tunnel
  backend_ref: cloudflare-production
  api_token_ref: 00000000-0000-4000-8000-000000000001
  account_id: YOUR_CLOUDFLARE_ACCOUNT_ID
  tunnel_id: YOUR_CLOUDFLARE_TUNNEL_ID
  verify_timeout: 30s
  zones:
    - name: sharegap.net
      zone_id: YOUR_CLOUDFLARE_ZONE_ID
      allowed_org_ids:
        - 00000000-0000-4000-8000-000000000002
      protected: true
      ttl: 300
  origins:
    - deployment_unit_id: 00000000-0000-4000-8000-000000000003
      host: 127.0.0.1
      allowed_ports:
        - 18088
```

`api_token_ref` is an opaque SecretRef UUID. Store the Cloudflare API token through Bahia's secret-management path; never put the token itself in this file. `allowed_org_ids` limits which organizations may claim the zone. Each origin allowlists one deployment unit, host, and set of ports; `route-attach` is rejected when a request falls outside those boundaries. A protected zone also requires a protected environment.

The `dns/zone-create`, `dns/policy-apply`, `dns/record-set`, and `dns/drift-remediate` ContextVM methods are registered even before DNS is configured. If `dns.enabled` is false or no DNS runtime is configured, each returns JSON-RPC `-32000` with `DNS orchestration is not enabled; set dns.enabled and configure a backend`. This fail-closed response distinguishes missing configuration from an unavailable method.

After editing the server configuration, send `SIGHUP` to the Bahia server process. The server reloads and validates the candidate configuration, constructs a replacement application, stops the current application only after replacement initialization succeeds, and then starts the replacement. DNS and edge-routing changes therefore become active through **whole-application reconstruction**, not in-place mutation. An invalid candidate leaves the current application running.

## 2. Create or update the service

Configure the CLI with an operator signer. Prefer the NIP-46 remote signer: point the CLI at a file containing the bunker URI with `--nostr-bunker-file` (or `BAHIA_NOSTR_BUNKER_FILE`; `BAHIA_NOSTR_BUNKER_RELAYS` when the signer relay is stored separately). Keep the bunker URI in a file — it can carry a connect secret, so never pass it as a literal flag value or environment dump. A file-backed local key (`--nostr-key-file` / `BAHIA_NOSTR_KEY_FILE`) remains available as a compatibility path. Mutations publish signed ContextVM requests; REST is not the mutation authority.

Create Astillero if it is not registered:

```bash
bahia services create \
  --name astillero \
  --artifact-repo registry.example/astillero \
  --runtime-type compose \
  --managed-runtime-config-file astillero-runtime.json \
  --idempotency-key service:create:astillero
```

Or update the existing service:

```bash
bahia services update \
  --service 00000000-0000-4000-8000-000000000004 \
  --managed-runtime-config-file astillero-runtime.json \
  --idempotency-key service:update:astillero
```

The environment must already contain the Bahia-managed Docker or Compose deployment unit represented by `deployment_unit_id` in `edge_routing.origins`. Its runtime configuration must expose the application on the allowlisted origin port (`18088` here).

## 3. Deploy the artifact

Use the existing signer-first deployment flow with the service, environment, deployment-unit, and artifact IDs returned by Bahia:

```bash
bahia deployments deploy \
  --service 00000000-0000-4000-8000-000000000004 \
  --environment 00000000-0000-4000-8000-000000000005 \
  --deployment-unit 00000000-0000-4000-8000-000000000003 \
  --artifact 00000000-0000-4000-8000-000000000006 \
  --idempotency-key deploy:astillero:artifact
```

If policy returns a pending intent, approve it with the same signer-first control plane:

```bash
bahia deployments approve \
  --intent 00000000-0000-4000-8000-000000000007 \
  --idempotency-key approve:astillero:deploy
```

Wait for the canonical deployment run/state events to report success; the initial ContextVM response is only an acknowledgment.

## 4. Let Bahia project internal DNS

No DNS mutation command is required for the normal service path. With `dns.projection.services: true`, the `dns-reconciler` reacts to completed deployment runs, environment service-state changes, and runtime observations. A healthy in-sync Astillero observation in `edge-01-production` is mapped by `environment_zones` to `sharegap.net`, producing an A or AAAA record such as:

```text
astillero.sharegap.net. 300 IN A 10.20.0.88
```

The exact address comes from the runtime observation; do not hardcode it in the operator flow. Confirm that `/health` lists the `dns-reconciler` runner and that `/ready` succeeds before relying on the projection:

```bash
curl -fsS http://127.0.0.1:8080/health
curl -fsS http://127.0.0.1:8080/ready
dig @LAN_DNS_SERVER astillero.sharegap.net
```

## 5. Attach the external HTTPS route

Attach a public route to the **existing deployed service** without redeploying its artifact:

```bash
bahia deployments route-attach \
  --service 00000000-0000-4000-8000-000000000004 \
  --environment 00000000-0000-4000-8000-000000000005 \
  --deployment-unit 00000000-0000-4000-8000-000000000003 \
  --hostname astillero.sharegap.net \
  --upstream-scheme http \
  --upstream-port 18088 \
  --health-path /health \
  --tls managed \
  --idempotency-key route:astillero:sharegap
```

The route is part of the signed desired-state hash. Bahia executes it as a route-only deployment run, so the current artifact is preserved. If the route policy requires approval, approve the returned intent:

```bash
bahia deployments approve \
  --intent 00000000-0000-4000-8000-000000000008 \
  --idempotency-key approve:astillero:route
```

Bahia updates the managed Cloudflare Tunnel ingress and DNS record and verifies the HTTPS health path. If verification fails, the routing backend compensates by restoring the previous public route state and the route-only run fails.

## 6. Verify the result

```bash
curl -fsS http://127.0.0.1:8080/ready
curl -fsS https://astillero.sharegap.net/health
curl -fsS https://astillero.sharegap.net/
```

The final user experience is `https://astillero.sharegap.net/` without an exposed port. Apply the same flow to any Bahia-managed Docker or Compose service by changing the service/environment/unit IDs, mapped zone, allowed origin, hostname, and health path.

## Operational boundary

For this workflow:

- no direct SQL changes;
- no manual Docker, Compose, nginx, or cloudflared edits;
- no manual Cloudflare dashboard changes;
- no real credentials in configuration or command history.

Use signed Bahia commands and configured SecretRefs so desired state, policy decisions, deployment runs, DNS projection, routing compensation, and audit events remain consistent.

## Related guides

- [Services](../features/services.md)
- [Deployments](../features/deployments.md)
- [DNS](../features/dns.md)
- [CLI reference](../cli-reference.md)
