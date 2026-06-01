# DNS

**DNS** in Bahia provides service discovery through DNS zone and endpoint management.

## Overview

DNS features include:
- **Zone management** — Define DNS zones
- **Endpoint projection** — Auto-discover service endpoints
- **Policy routing** — Split-horizon, weighted routing
- **FIPS mesh integration** — Federated identity endpoints

## Key Concepts

### DNS Zone

A **Zone** defines a DNS namespace:

```yaml
name: "services.example.com"
backend_id: "backend-123"
soa:
  ttl: 3600
  refresh: 7200
```

### DNS Endpoint

An **Endpoint** is a discoverable service address:

```yaml
family: "service"
name: "payment-api"
environment: "prod"
fqdn: "payment-api.prod.services.example.com"
address: "10.0.1.100"
port: 8080
protocol: "https"
health: "healthy"
```

### DNS Policy

A **Policy** controls routing behavior:

```yaml
name: "geo-routing"
type: "weighted"
rules:
  - weight: 80
    endpoint: "payment-api-us"
  - weight: 20
    endpoint: "payment-api-eu"
```

## Viewing DNS State

### Web UI

Navigate to **DNS** in the sidebar:
- **Zones**: DNS zone definitions
- **Endpoints**: Service endpoint catalog
- **Policies**: Routing policies
- **FIPS Mesh**: Federated identity endpoints

### CLI

```bash
# List zones
bahia dns zones list

# List endpoints
bahia dns endpoints list
bahia dns endpoints list --family service

# List policies
bahia dns policies list
```

### MCP Tools

```json
{
  "tool": "bahia_dns_list_zones",
  "arguments": {}
}
```

```json
{
  "tool": "bahia_dns_list_endpoints",
  "arguments": {
    "family": "service"
  }
}
```

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

### Zones

```bash
bahia dns zones create \
  --name "services.example.com" \
  --backend-id backend-123
```

Nostr (signer-first):
```json
{
  "kind": 5941,
  "content": {
    "name": "services.example.com"
  },
  "tags": [
    ["t", "dns-zone-create"]
  ]
}
```

### Policies

```bash
bahia dns policies apply \
  --name "geo-routing" \
  --type weighted \
  --config rules="..."
```

### Record Overrides

```bash
bahia dns records override \
  --zone "services.example.com" \
  --name "api" \
  --type A \
  --value "10.0.1.200"
```

## Endpoint Projection

Bahia automatically projects endpoints from:
- **Services** — Healthy service deployments
- **LLM routes** — Active LLM endpoints
- **ML endpoints** — Inference endpoints
- **Workers** — Available workers
- **FIPS mesh** — Federated identity nodes

### Endpoint Families

| Family | Source |
|--------|--------|
| `service` | Deployed services |
| `llm` | LLM route endpoints |
| `ml` | ML inference endpoints |
| `worker` | Loom workers |
| `fips` | FIPS mesh nodes |

### Health Status

| Status | Description |
|--------|-------------|
| `healthy` | Endpoint is responding |
| `unhealthy` | Endpoint is failing |
| `unknown` | Health not determined |

## FIPS Mesh

The FIPS mesh provides federated identity endpoints:

### Viewing FIPS Mesh

```bash
bahia fips mesh status
bahia fips nodes list
```

### Web UI

The DNS page includes a **FIPS Mesh Panel** showing:
- Mesh topology
- Node status
- Connection health

### MCP Resources

FIPS nodes are exposed as MCP resources:

```json
{
  "uri": "bahia://fips/node/node-123",
  "name": "fips-node-123",
  "metadata": {
    "status": "online",
    "capabilities": ["sign", "verify"]
  }
}
```

## Nostr Event Kinds

| Kind | Name | Description |
|------|------|-------------|
| 5941 | DNSZoneCreate | Create/reconcile zone |
| 5942 | DNSPolicyApply | Apply policy |
| 5943 | DNSRecordOverride | Override record |
| 5944 | DNSDriftRemediate | Fix drift |
| 5945 | DNSBackendRegister | Register backend |
| 6941 | DNSStatus | Progress updates |
| 7941-7945 | DNS Results | Terminal results |

## Read Models

| Kind | d-tag | Content |
|------|-------|---------|
| 31975 | `zone:<name>` | Zone state |
| 31976 | `endpoint:<family>:<name>:<env>` | Endpoint state |
| 31977 | `dnspolicy:<id>` | Policy state |
| 31978 | `dnsbackend:<id>` | Backend state |

Subscribe for updates:
```json
{
  "kinds": [31976],
  "#t": ["dns-endpoint"]
}
```

## Drift Detection

DNS drift is detected when:
- Expected records don't match actual
- Endpoints are missing or extra
- Health status changes

### Drift Remediation

```bash
bahia dns drift remediate --zone services.example.com
```

```json
{
  "kind": 5944,
  "content": {
    "zone": "services.example.com"
  }
}
```

## Best Practices

1. **Use policies** — Consistent routing behavior
2. **Monitor health** — Alert on unhealthy endpoints
3. **Document zones** — Clear naming conventions
4. **Test failover** — Verify routing under failure
5. **Secure backends** — Limit backend access

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

- [Services](services.md) — Endpoint sources
- [Workers](workers.md) — Worker endpoints
- [LLM Routes](llm-routes.md) — LLM endpoints
