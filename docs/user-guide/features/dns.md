# DNS

**DNS** in Bahia provides service discovery through DNS zone and endpoint management.

> DNS orchestration is feature-gated and disabled by default. Set `dns.enabled: true` and configure a backend.

## Overview

DNS features include:
- **Zone management** — Define DNS zones
- **Endpoint projection** — Auto-discover service endpoints
- **Policy routing** — Match/action rules that set visibility and TTL overrides
- **FIPS mesh integration** — Mesh node endpoints

## Key Concepts

### DNS Zone

A **Zone** defines a DNS namespace:

```yaml
name: "services.example.com"
visibility: "internal"     # internal, external, edge, or mesh
backend_ref: "dnsmasq-main"
ttl: 3600
authoritative: true
allow_empty_authoritative: false
```

### DNS Endpoint

An **Endpoint** is a discoverable service address:

```yaml
family: "service"
name: "payment-api"
environment: "prod"
zone: "services.example.com"
fqdn: "payment-api.prod.services.example.com"
address: "10.0.1.100"
port: 8080
protocol: "https"
health: "healthy"
drift_status: "in_sync"
```

### DNS Policy

A **Policy** is a named set of match/action rules, optionally scoped to a zone or environment:

```json
{
  "name": "edge-routing",
  "rules": [
    {
      "match": {"environment": "prod"},
      "action": {"visibility": "edge", "ttl_override": 60}
    }
  ],
  "enabled": true
}
```

Weighted or geo routing is not part of the DNS policy model.

## Viewing DNS State

### Web UI

Navigate to **DNS** in the sidebar:
- **Signed DNS control-plane commands**: zone, policy, record, and remediation forms
- **Zones**: DNS zone definitions
- **Endpoints**: Service endpoint catalog
- **Drift history**: Recent drift events
- **Policies**: Routing policies
- **FIPS/Mesh** tab: Mesh node status

### CLI and MCP

The signer-first `bahia dns` group provides zone creation, policy application, record overrides, and drift remediation. These commands use the configured operator signer to publish ContextVM kind `25910` requests and await correlated acknowledgments. The corresponding `dns/zone-create`, `dns/policy-apply`, `dns/record-set`, and `dns/drift-remediate` ContextVM methods are always registered.

> **NOTE (2026-09-11):** the relay projector's capability announcement lists `dns/record-override`, but the registered handler method is `dns/record-set` (`internal/controlplane/encrypted_transport.go`). Use `dns/record-set`. When DNS orchestration is disabled or has no configured runtime, they return JSON-RPC `-32000` with the exact message `DNS orchestration is not enabled; set dns.enabled and configure a backend` instead of method-not-found.

```bash
bahia dns zone-create --name prod.example --visibility internal --backend-ref dnsmasq-main --ttl 300 --authoritative
bahia dns policy-apply --file dns-policy.json
bahia dns record-set --zone prod.example --name api --type A --value 192.0.2.10 --ttl 60 --reason "incident pin"
bahia dns drift-remediate --zone prod.example
bahia dns drift-remediate
```

For dnsmasq and `dnsmasq_agent` zones, `authoritative: true` renders a managed `local=/<zone>/` guard so unanswered query types are not forwarded upstream. The default is `false`, preserving existing forwarding behavior.

Authoritative zones also refuse a destructive transition from a non-empty listed record set to an empty projected record set. The reconciler leaves the existing backend include unchanged, emits drift plus a `dns.zone_sync_refused` status event, and warns once until projection recovers. Set `allow_empty_authoritative: true` only when an intentional authoritative-zone teardown must be allowed; non-authoritative zones continue to permit empty syncs.

For external MCP embeddings with authorization configured, `bahia_dns_list_endpoints`, `bahia_dns_list_drift`, and the `bahia_assistant_dns_*` tools remain available as listed in the [MCP reference](../mcp-tools.md).

## MCP Resources

DNS endpoints are exposed as MCP resources:

```json
{
  "uri": "bahia://dns/endpoint/payment-api.prod.services.example.com",
  "name": "payment-api (prod)",
  "description": "Payment API production endpoint",
  "mimeType": "application/json",
  "metadata": {
    "protocol": "https",
    "address": "10.0.1.100",
    "port": 8080,
    "health": "healthy"
  }
}
```

Agents can query these resources for service discovery.

## Creating DNS Records

### Zones, policies, and overrides

Use the signer-first CLI commands above, the DNS web mutation flows, or the registered assistant tools:

- `bahia_assistant_dns_zone_create`
- `bahia_assistant_dns_policy_apply`
- `bahia_assistant_dns_record_override`

Each publishes the corresponding signed Nostr request and returns correlation metadata.

## Endpoint Projection

For an operational LAN configuration using dnsmasq, environment-to-zone mapping, and managed external HTTPS routing, follow [Managed DNS and HTTPS Routes](../guides/managed-dns-and-https-routes.md). The dnsmasq backend is the deployable internal-LAN exemplar for mapping `edge-01-production` services into `sharegap.net`. When the LAN resolver runs on a **different host** than Bahia (the edge-01/core-01 topology), use the `dnsmasq_agent` backend instead: Bahia publishes signed ContextVM kind `25910` requests (schema `bahia.dnsagent.v1`) over the configured relays to a `bahia-dns-agent` process on the resolver host, which manages only Bahia-owned dnsmasq include files with atomic writes, serial-guarded applies, and automatic rollback on failed reloads. It is configured with `agent_pubkey` (the agent's hex pubkey), optional `agent_relays`, `agent_encrypted`, `agent_timeout`, and `agent_retries`; see the [core-01 dnsmasq agent runbook](../../runbooks/core01-dnsmasq-agent.md) for deployment. The filesystem backend is not deployable because Bahia does not wire an operational activator for its snapshots; choose dnsmasq, dnsmasq_agent, CoreDNS, PowerDNS, or FIPS instead. DNS configuration changes take effect on `SIGHUP` through whole-application reconstruction, not in-place backend mutation.

Bahia automatically projects endpoints from:
- **Services** — Healthy service deployments
- **LLM routes** — Active LLM endpoints
- **ML endpoints** — Inference endpoints
- **Workers** — Available workers
- **FIPS mesh** — Mesh nodes

For service observations, `dns.projection.host_overrides` translates a runtime-observed host or deployment-unit endpoint alias into a concrete IP address or fully qualified hostname before Bahia selects the DNS record type. IP overrides produce `A` or `AAAA` records; fully qualified hostname overrides produce `CNAME` records. Configure an override for every Bahia-managed endpoint alias that is not itself resolvable:

```yaml
dns:
  projection:
    host_overrides:
      edge-01-docker: 192.168.40.104
```

Bahia never emits a `CNAME` to a bare single-label target such as `edge-01-docker`. Without a matching override, it skips that service record and logs a warning rather than publishing a record whose target will return `NXDOMAIN`.

### Endpoint Families

| Family | Source |
|--------|--------|
| `service` | Deployed services |
| `llm` | LLM route endpoints |
| `ml` | ML inference endpoints |
| `worker` | Loom workers |
| `mesh` | FIPS mesh nodes |

### Health Status

| Status | Description |
|--------|-------------|
| `healthy` | Endpoint is responding |
| `unhealthy` | Endpoint is failing |
| `unknown` | Health not determined |

## FIPS Mesh

The FIPS mesh exposes mesh-node endpoints:

### Viewing FIPS mesh

Use the web panel or `bahia_fips_mesh_status` and `bahia_fips_list_mesh_nodes` through MCP.

### Web UI

The DNS page includes a **FIPS/Mesh** tab showing:
- Mesh topology
- Node status
- Connection health

### MCP Resources

FIPS nodes are exposed as MCP resources:

```json
{
  "uri": "bahia://fips/mesh/node/node-a.mesh.example",
  "name": "fips-node-123",
  "metadata": {
    "status": "online",
    "capabilities": ["sign", "verify"]
  }
}
```

## Nostr Methods and Kinds

DNS mutations use ContextVM kind `25910` methods (`dns/zone-create`, `dns/policy-apply`, `dns/record-set`, `dns/drift-remediate`, `dns/backend-register`). The dedicated DNS request/status/result kinds (`5941`-`5945`, `6941`, `7941`-`7945`) remain defined in `internal/kinds`.

## Read Models

DNS state is published as canonical kind `30900` with `domain=dns` (entities `zone`, `endpoint`, `policy`, `backend`) and a `legacy_kind` tag. The historical `31975`-`31978` read-model kinds are migration inventory only.

Subscribe for updates:
```json
{
  "kinds": [30900],
  "authors": ["<bahia-service-pubkey>"],
  "#domain": ["dns"]
}
```

## Drift Detection

DNS drift is detected when:
- Expected records don't match actual
- Endpoints are missing or extra
- Health status changes

### Drift remediation

Use `bahia dns drift-remediate [--zone <zone>]` for the CLI path. MCP clients can use `bahia_assistant_dns_drift_remediate` and follow its correlation metadata to the status/result projection.

## Best Practices

1. **Use policies** — Consistent visibility and TTL behavior
2. **Monitor health** — Alert on unhealthy endpoints
3. **Configure host overrides** — Map every non-resolvable endpoint alias
4. **Secure backends** — Limit backend access

## Troubleshooting

### Endpoint Not Appearing

- Check service health
- Verify deployment succeeded
- Check DNS projection enabled

### Zone Sync Failed

- Check backend connectivity
- Verify credentials
- Review sync logs

### FIPS Node Offline

- Check network connectivity
- Verify node configuration
- Review node logs

## Related

- [Managed DNS and HTTPS Routes](../guides/managed-dns-and-https-routes.md) — Signer-first deployment, automatic internal DNS, and managed public HTTPS
- [Services](services.md) — Endpoint sources
- [Workers](workers.md) — Worker endpoints
- [LLM Routes](llm-routes.md) — LLM endpoints
