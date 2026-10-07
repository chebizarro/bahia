# Deployment guide

Bahia ships three primary services:

- `bahia-server`: daemon, HTTP probes, MCP and OCI/API routes;
- `bahia-relay`: relay sidecar with bbolt storage;
- `web`: static SvelteKit application served by nginx.

PostgreSQL is optional derived-index storage. Blossom, OCI, Harbor, Hive-CI,
DNS, Soul Factory and runtime integrations are enabled independently.

## Local Docker Compose

Requirements: Docker Engine with Compose, a 64-hex or `nsec` service key, and
the host Docker socket group ID.

```bash
export BAHIA_NOSTR_PRIVATE_KEY=<service-private-key>
export BAHIA_NOSTR_AUTHORIZED_PUBKEYS=<comma-separated-operator-pubkeys>
export PUBLIC_BAHIA_SERVICE_PUBKEYS=<service-pubkey-hex>
export DOCKER_SOCKET_GID=$(stat -c '%g' /var/run/docker.sock)
docker compose up --build
```

The stack exposes:

| Service | Address | Check |
|---|---|---|
| Web | `http://localhost:3000` | loads the runtime bootstrap seed |
| Daemon | `http://localhost:8080` | `/health` liveness, `/ready` readiness |
| Relay | `ws://localhost:3334/relay` | NIP-11 at the same HTTP URL |
| PostgreSQL | `localhost:5432` | `pg_isready` |

Both Go services mount `config.compose.yaml`. The daemon also mounts the host
Docker socket and joins `DOCKER_SOCKET_GID`; using a group name would resolve
against the image's group database instead of the host socket owner.

Container health probes liveness only. Use `/ready` for traffic admission and
operator gates so an unavailable signer or relay degrades readiness without
restarting a live process.

## Configuration loading

`config.Load` starts with code defaults, reads an optional YAML file, then
loads `BAHIA_` environment variables. A single underscore separates the
section from its field (`BAHIA_DB_SSLMODE` → `db.sslmode`); use double
underscores for nested maps (`BAHIA_RUNTIME__ENDPOINTS__prod__DOCKER_HOST`).

Important defaults:

| Key | Default |
|---|---|
| `mode` | `full` |
| `server.host`, `server.port` | `127.0.0.1`, `8080` |
| `db.host`, `db.port`, `db.user`, `db.name` | `localhost`, `5432`, `bahia`, `bahia` |
| `db.password`, `db.sslmode` | empty, `require` |
| `nostr.publish_enabled`, `publish_quorum` | `true`, `1` |
| `nostr.closed_retry_budget` | `5` |
| `nostr.relay_auth_unavailable` | `exclude_and_fail` |
| `nostr.relay_quorum` | full `2`, degraded `1`, emergency `1` |
| `nostr.sidecar.enabled` | `false` |
| `nostr.sidecar.read_auth_mode` | `enforce` (also for empty/unknown values) |
| `nostr.sidecar.event_retention` | `0` (durable regular events) |
| `nostr.sidecar.request_retention` | `24h` |
| `reconcile.enabled`, `reconcile.interval` | `true`, `60s` |
| `runtime.type`, `runtime.docker_host` | `docker`, `unix:///var/run/docker.sock` |
| `auth.enabled`, `adoption.enabled`, `direct_runtime_actions.enabled` | `false` |
| `cors.allowed_origins` | empty |
| `oci.enabled`, `telemetry.enabled`, `soul_factory.enabled` | `false` |

The full schema and validation rules are in `internal/config/config.go`.
`config.yaml` is a compact example and `.env.example` contains local Compose
overrides.

Outside `dev_mode`, a wildcard HTTP bind requires `auth.enabled`, the bundled
`bahia` database password is rejected, database TLS must be `require`,
`verify-ca` or `verify-full`, and non-loopback WebSocket endpoints must use
`wss`. Keep private keys and tokens in the deployment secret system rather
than YAML.

## Running binaries

```bash
make build
./bin/bahia-relay --config /etc/bahia/config.yaml
./bin/bahia-server -config /etc/bahia/config.yaml
./bin/bahia --help
```

The server listens even when PostgreSQL is unavailable. Relay-backed
services, ContextVM handling, discovery, health/readiness and DB-less payment
reads can operate; `/mcp`, `/v2/*` and repository-backed HTTP routes are
dependency-gated. The `database-recovery` runner reconnects and installs
repository-backed services when PostgreSQL returns.

## Relay topology

Configure each purpose explicitly:

```yaml
nostr:
  private_key: <secret>
  authorized_pubkeys: [<operator-pubkey>]
  service_relays: [wss://relay.example]
  browser_relays: [wss://relay.example]
  contextvm_relays: [wss://relay.example]
  sidecar:
    enabled: true
    listen_addr: 127.0.0.1:3334
    public_url: wss://bahia.example/relay
    backend_url: ws://127.0.0.1:3334/relay
    read_auth_mode: enforce
```

`service_relays` is the daemon publication/backfill set. When it is empty,
`nostr.relays` supplies that list. Browser and ContextVM sets are advertised
by discovery; the sidecar URL is added to the ContextVM policy when enabled.
See [control planes](control-planes.md) and the
[relay sidecar reference](relay-sidecar.md).

## Runtime targets

Use named endpoints so signed intents carry an opaque `endpoint_ref`, not a
Docker URL or TLS material:

```yaml
runtime:
  endpoints:
    prod-docker:
      docker_host: tcp://docker.example:2376
      ca_cert_file: /run/secrets/docker-ca.pem
      client_cert_file: /run/secrets/docker-cert.pem
      client_key_file: /run/secrets/docker-key.pem
  environments:
    production:
      compose_dir: /srv/bahia/compose/production
      bahia_owned: true
      docker_host: unix:///var/run/docker.sock
```

Docker, Compose, Kubernetes and Podman runtimes are selected from the service
and environment desired state. Bahia-owned Compose projects render under the
configured `compose_dir`; generated environment files can contain resolved
secrets and must inherit the same storage protection as the daemon's secret
inputs. Remote Docker endpoints should use mutual TLS.

Virtual-machine runtimes are configured separately under `virtualization`;
see [VM runtimes](vm-runtimes.md).

## Built-in OCI registry

Set `oci.enabled: true` only with PostgreSQL and Blossom available. The daemon
mounts the distribution API at `/v2`, stores manifests/tags in PostgreSQL and
blobs in Blossom, and authenticates with NIP-98, configured service accounts,
or anonymous pull CIDRs. See the exact route behavior in the
[HTTP reference](api.md).

## Edge deployment workflow

`.github/workflows/deploy-edge.yml` is a manually dispatched deployment on a
runner labelled `self-hosted`, `edge-01`, `docker`. Required input
`release_revision` selects the full commit. Optional inputs set release-tree
retention, image retention and the operations-widget publisher allowlist.

The workflow:

1. checks out the exact revision;
2. validates the runner, Compose file, runtime bootstrap seed and image policy;
3. builds SHA-tagged daemon and web images locally;
4. stages the release tree under `/srv/data/bahia-controlplane/releases`;
5. updates the host Compose file through `scripts/deploy_edge_compose_update.py`;
6. applies the stack under the relay-policy gate;
7. waits for the daemon readiness URL;
8. restores the saved Compose file and safe images if the gated apply fails;
9. prunes only release trees, backups and Bahia-tagged images outside the
   configured retention windows.

The workflow never runs a blanket `docker image prune -a`. Operational detail
and Hive-CI artifact publication are in
[push-to-deploy and Hive-CI](push-to-deploy-and-hiveci-runbook.md).

## Production checks

Before directing traffic to a release:

```bash
curl -fsS https://bahia.example/health
curl -fsS https://bahia.example/ready
curl -fsS -H 'Accept: application/nostr+json' https://bahia.example/relay
```

Confirm that `/ready` reports passing `relay_quorum`, `bootstrap_ready`,
`background_runners`, `intent_readiness`, `ock_rotation` and
`canonical_delivery` checks, plus every enabled subsystem check. Validate one
REQ/EOSE subscription, one signed intent and its relay `OK`, requester-scoped
`30315` status, and canonical record before considering the deployment
operational.
