# DNS

Bahia manages DNS zones, endpoints, backends, policies, record overrides, projection, drift, and FIPS mesh visibility. The feature is enabled with the `dns` configuration section.

## Web and CLI

Open **DNS** (`/dns`) for zones, endpoints, policies, backends, overrides, drift, and mesh status.

```bash
bahia dns zone-create --name prod.example --visibility external --backend-ref powerdns-prod --ttl 300
bahia dns endpoint-create --file endpoint.json
bahia dns policy-apply --file dns-policy.json
bahia dns record-set --zone prod.example --name api --type A --value 192.0.2.10 \
  --reason "incident pin"
bahia dns override-retire --override-id <uuid> --reason "projection is authoritative"
bahia dns drift-remediate --zone prod.example
```

DNS commands are fleet-scoped and require a fleet operator. Updates and deletes use `expected_updated_at` where the command exposes it.

## MCP and resources

MCP reads use `bahia_dns_list_endpoints`, `bahia_dns_list_drift`, and their assistant aliases. The `bahia_assistant_dns_*` intent tools cover zone, endpoint, backend, policy, record, and override changes. Drift remediation is available through the CLI.

`resources/list` also exposes DNS endpoint resources as `bahia://dns/…`.

## Projection

A DNS policy maps an environment or service scope to a zone. Runtime observations produce endpoint records only when the selected service, environment, and unit have enough address evidence. Host overrides can translate an endpoint alias to an IP address or fully qualified hostname.

Record overrides take precedence until retired or expired. They require a reason, are retained for audit, and do not silently disappear after a successful projection.

Supported zone visibility values are `internal`, `external`, `edge`, and `mesh`. Record types are `A`, `AAAA`, `CNAME`, and `SRV`.

## Backends

Configure a backend that Bahia can actively apply: dnsmasq, dnsmasq agent, CoreDNS, PowerDNS, or FIPS. The filesystem renderer alone does not activate snapshots.

For a remote dnsmasq host, the dnsmasq agent accepts signed requests from the configured Bahia identity, writes only Bahia-owned include files, applies a monotonic serial, and restores the prior file when validation or reload fails. Give every agent or bridge its own writable local event-store path.

## Drift and safety

The drift view compares desired endpoint records with backend observations. A remediation intent is zone-scoped and reports bounded status data. Do not mark a zone authoritative until backend delegation is in place. Protect manual overrides with expiry and ownership review.

For an end-to-end service hostname flow, see [Managed DNS and HTTPS Routes](../guides/managed-dns-and-https-routes.md).

## Related

- [Services](services.md)
- [Deployments](deployments.md)
- [Route Canaries](route-canaries.md)
