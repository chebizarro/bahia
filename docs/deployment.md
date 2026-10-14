# Deployment guide

Bahia ships three primary services:

- `bahia-server`: daemon, HTTP probes, MCP and OCI/API routes;
- `bahia-relay`: relay sidecar with bbolt storage;
- `web`: static SvelteKit application served by nginx.

PostgreSQL is optional derived-index storage. Blossom, OCI, Harbor, Hive-CI,
DNS, Soul Factory and runtime integrations are enabled independently.

## Local Docker Compose

Requirements: Docker Engine with Compose, a 64-hex or `nsec` service key, the
host Docker socket group ID, and a nostrc checkout with Signet >= 0.7.0 for
the disposable dev Signet that holds the daemon's system keys
([runbook](runbooks/signet-secret-store.md#dev-compose-stack)).

```bash
export NOSTRC_DIR=<nostrc checkout>
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
| Signet (dev) | `ws://signet:7777` inside the stack | healthy once seeded |

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
| `nostr.publish_enabled`, `publish_quorum` | `true`, `1` (at most the number of service write relays; `-1` = all) |
| `nostr.control_plane_publish_quorum` | unset (inherits service quorum, capped to the control-plane pool with a startup warning and WARN health; explicit value must fit that pool) |
| `nostr.controlled_relays` | empty = the sidecar alone; required when publishing with a service identity and any control-plane relay is not the sidecar; the sidecar URLs are always controlled |
| `nostr.allowed_public_relays` | empty (public relays are rejected in every relay list) |
| `nostr.closed_retry_budget` | `5` |
| `nostr.signer.method` | empty = `local` when `nostr.private_key` is set; `nip46` or `nip55l` keep the service key in a remote signer, see the [service signer runbook](runbooks/service-signer.md) |
| `nostr.outbound.kill_switch_file` | empty (also `BAHIA_NOSTR_OUTBOUND_KILL_SWITCH_FILE`) |
| `nostr.outbound` budgets | aggregate `45`/min burst `15`; per-relay wire `40`/min burst `13` + priority share `15`/`5`; see the [outbound admission runbook](runbooks/nostr-outbound-admission.md) |
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
These lists are the bootstrap topology: once the daemon receives the
canonical relay settings, those govern. Every control-plane relay must be in
`nostr.controlled_relays` and none may be a public relay; see the
[relay configuration runbook](runbooks/relay-configuration.md).
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
inputs. Remote Docker endpoints should use mutual TLS. Set
`execution_mode: sdk` on a Compose target to validate, apply, observe, deploy,
and remove services through the embedded Compose SDK without a Compose binary.
`execution_mode: cli` retains the Compose CLI compatibility path. SDK deploy
accepts image and environment interpolation overrides; other legacy deploy
options are rejected with an error rather than silently ignored. For Podman, set
`runtime.default.podman_host` (or an environment's `podman_host`) to a custom
socket such as `unix:///run/podman/podman.sock`; otherwise Bahia uses the
daemon user's rootless socket. A Podman target with `endpoint_ref` uses the
endpoint's `docker_host` as its Docker-compatible API socket instead.

Virtual-machine runtimes are configured separately under `virtualization`;
see [VM runtimes](vm-runtimes.md).

## Built-in OCI registry

Set `oci.enabled: true` only with PostgreSQL and Blossom available. The daemon
mounts the distribution API at `/v2`, stores manifests/tags in PostgreSQL and
blobs in Blossom, and authenticates with NIP-98, configured service accounts,
or anonymous pull CIDRs. See the exact route behavior in the
[HTTP reference](api.md).

## Edge deployment workflow

**Prerequisite.** A daemon that publishes (`nostr.publish_enabled`) needs the
confidential-dedupe system key, and one with `assistant.enabled` needs the
assistant-transcript key: production configuration must carry
`secret_store` (backend `signet`, a reachable Signet, the requester key file,
the owner) and `secret_store.system_keys` before publishing or the assistant
is enabled. Without the dedupe key every confidential write (orgs, members,
secrets, payments, security, operator allowlists) fails and `/ready` fails
`system_keys`; `assistant.enabled` without the transcript key is refused at
config load. Create the credentials first
([runbook](runbooks/signet-secret-store.md#system-credentials)).

`.github/workflows/deploy-edge.yml` is a manually dispatched deployment on a
runner labelled `self-hosted`, `edge-01`, `docker`. Required input
`release_revision` selects the full commit. Optional inputs set release-tree
retention, image retention and the operations-widget publisher allowlist.

The workflow:

1. checks out the exact revision;
2. validates the runner, Compose file, runtime bootstrap seed and image policy;
3. builds SHA-tagged daemon and web images locally;
4. stages the release tree under `/srv/data/bahia-controlplane/releases`;
5. resolves the host base Compose file through compose-go, applies immutable image pins, the read-only release docs mount and missing web bootstrap values as typed project mutations;
6. writes a private canonical JSON applied-project receipt and converges Bahia/web through the Compose v5 SDK with wait semantics;
7. verifies daemon readiness, relay-policy continuity, relay HTTP and web HTTP;
8. re-applies an admission-sanitized prior project receipt through the same SDK on failure;
9. prunes only release trees, receipts and Bahia-tagged images outside the configured retention windows.

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
`canonical_delivery` checks, plus `system_keys` and every enabled subsystem
check. Validate one
REQ/EOSE subscription, one signed intent and its relay `OK`, requester-scoped
`30315` status, and canonical record before considering the deployment
operational.
