# Managed DNS and HTTPS Routes

This guide connects a Bahia-managed service to one hostname across runtime deployment, internal DNS, and managed HTTPS.

## Prerequisites

- A service, immutable artifact, and environment with an explicit deployment unit.
- A configured DNS backend and zone policy.
- `edge_routing.enabled` with its provider credential stored as a secret reference.
- `internal_routing.enabled` when Bahia serves a LAN vhost.
- A real application health endpoint.
- A signer authorized for the service's organization and fleet-scoped DNS actions.

## 1. Configure DNS and routing

Map the environment or service to the managed zone. For a local dnsmasq process, configure Bahia's owned include directory and reload command. For a resolver on another host, configure the dnsmasq agent with its pubkey and relays; it accepts signed requests, updates only Bahia-owned include files, and rolls back a failed validation or reload.

For internal HTTPS, use absolute include and certificate paths. Bahia refuses to overwrite a foreign file at its managed vhost path.

See [DNS](../features/dns.md) for backend choices and drift behavior.

## 2. Create the service and environment

```bash
bahia services create --org "$ORG" --name api \
  --artifact-repo ghcr.io/acme/api --runtime-type compose

bahia environments create --org "$ORG" --name production \
  --unit-runtime-type compose \
  --unit-endpoint-ref production-docker \
  --unit-compose-dir /srv/bahia/api
```

Use returned UUIDs in later commands. Keep the Compose directory dedicated to Bahia-generated content.

## 3. Preview and deploy

```bash
bahia deployments preview --service "$SERVICE_ID" --environment "$ENV_ID" \
  --artifact "$ARTIFACT_ID"

bahia deploy --org "$ORG" --service "$SERVICE_ID" --environment "$ENV_ID" \
  --deployment-unit "$UNIT_ID" --artifact "$ARTIFACT_ID" \
  --expected-desired-state-hash "$DESIRED_HASH" \
  --idempotency-key "$INTENT_UUID"
```

Wait for the deployment run and service-state observation to confirm the digest is running.

## 4. Project DNS

Create the zone and policy if they do not exist:

```bash
bahia dns zone-create --name example.com --visibility external \
  --backend-ref edge-dns --ttl 300
bahia dns policy-apply --file dns-policy.json
```

Runtime observation drives endpoint projection. Confirm the endpoint has the intended hostname and address. If the runtime reports an unresolvable endpoint alias, configure a projection host override.

## 5. Attach HTTPS

```bash
bahia deployments route-attach \
  --service "$SERVICE_ID" --environment "$ENV_ID" \
  --deployment-unit "$UNIT_ID" \
  --hostname api.example.com --upstream-port 8080 \
  --health-path /healthz
```

The command plans and applies the route for the current artifact. When internal routing is configured, Bahia also manages the LAN vhost.

## 6. Verify

Check each layer independently:

```bash
dig api.example.com
curl --fail --show-error https://api.example.com/healthz
```

From the LAN, resolve the hostname through the managed internal resolver. From outside, confirm public DNS and the edge path. Route Canaries should report `route_ok` for every configured perspective and the service-state record should remain `in_sync`.

If the service is healthy but the route is not, inspect DNS projection, certificate selection, provider apply status, and proxy upstream resolution before redeploying the application.

## Recovery

- Retire a manual DNS override when projection becomes authoritative.
- Roll back a failed route through the route operation's compensation result.
- Roll back the deployment with an explicit successful artifact when application state is at fault.
- Preserve the same idempotency key when retrying an uncertain request.

## Related

- [Services](../features/services.md)
- [Deployments](../features/deployments.md)
- [DNS](../features/dns.md)
- [Route Canaries](../features/route-canaries.md)
- [CLI Reference](../cli-reference.md)
