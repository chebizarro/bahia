# Docker development stack

`docker-compose.yml` is a disposable local development topology, not a
production deployment recipe. It uses `dev_mode`, a throwaway local service
key, a seeded dev Signet and the host Docker socket. Do not expose these ports
or the socket to untrusted users. Production requires the signer, trust-root,
relay-policy, authentication and release gates in the
[deployment guide](docs/deployment.md) and [runbooks](docs/runbooks/).

## Required inputs

```sh
export NOSTRC_DIR=<nostrc-checkout-with-Signet-0.7.0-or-newer>
export BAHIA_NOSTR_PRIVATE_KEY=<throwaway-64-hex-service-key>
export BAHIA_NOSTR_AUTHORIZED_PUBKEYS=<comma-separated-operator-pubkeys>
export PUBLIC_BAHIA_SERVICE_PUBKEYS=<matching-service-pubkey-hex>
export DOCKER_SOCKET_GID=$(stat -c '%g' /var/run/docker.sock)
docker compose up --build
```

Use the numeric group of the host's socket; a group name inside the image
can refer to a different ID. `NOSTRC_DIR` supplies the Signet build context.
See [dev Signet setup](docs/runbooks/signet-secret-store.md#dev-compose-stack).
`.env.example` lists the local inputs; do not commit a populated `.env`.

## Services and data flow

| Service | Role | Address |
|---|---|---|
| `signet` | Seeded dev system-key store | `ws://signet:7777` inside Compose |
| `relay` | Durable sidecar; NIP-42 read authorization | `ws://localhost:3334/relay` |
| `bahia` | Intent processor, local state/outbox, HTTP-native tools | `http://localhost:8080` |
| `web` | Static app; store-first relay subscriptions and signed intents | `http://localhost:3000` |
| `postgres` | Optional derived index under `derived-sql` profile | `localhost:5432` |

The web receives its trusted signer and bootstrap relays from `PUBLIC_BAHIA_*`
at container start. Ordinary reads subscribe to canonical events and mutations
are signed intents, not REST CRUD. nginx proxies `/relay` and the remaining
HTTP-native daemon routes (logs, blob fetch, MCP and probes). The exact HTTP
surface is documented in [docs/api.md](docs/api.md); do not infer a CRUD API
from the presence of an `/api/v1` proxy location.

The daemon and relay depend on the dev Signet becoming healthy. Neither
requires PostgreSQL health to start. Container health checks use daemon
`/health` (liveness); traffic admission must use `/ready`. SQL-independent MCP
calls are available without the derived index; individual tools still enforce
authorization and their actual dependencies. A poisoned sidecar trust epoch
returns HTTP 503 from its `/ready`, while an acknowledgment-signer-only outage
is a structured warning.

## Persistent state

| Volume | Container path | Contents |
|---|---|---|
| `daemonstate` | `/var/lib/bahia/daemon` | Local events, outbox, receipt journals, effect fences, containment markers, trust-epoch high-water state |
| `relaydata` | `/var/lib/bahia/relay-sidecar` | Relay events and sidecar trust state |
| `nostrarchive` | `/var/lib/bahia/nostr-archive` | Event archive artifacts |
| `signetdata`, `signetshared` | Dev Signet `/data`, `/shared` | Throwaway dev credentials and public bootstrap data |
| `pgdata` | PostgreSQL data directory | Optional derived SQL index |

The image provisions Bahia's state directories for its non-root user. Use
`docker compose down` to stop the stack without deleting volumes. **Do not use
`down -v` or remove `daemonstate`/`relaydata` as a repair procedure.** Locally
accepted events and effects may not be recoverable from relays; the outbox and
its adjacent journals are not disposable caches. Existing installs using the
container writable layer need a stopped, operator-reviewed copy of their
local files into the new volume before recreation. Never copy a live bbolt
file or activate a second daemon with an independent receipt ledger.

## Optional derived SQL index

Only SQL-dependent integrations, including built-in OCI metadata, need this
profile. It does not grant SQL rows authority over canonical state.

```sh
docker compose --profile derived-sql up -d --wait postgres
export BAHIA_DB_HOST=postgres BAHIA_DB_PASSWORD=bahia BAHIA_DB_SSLMODE=disable
docker compose --profile derived-sql up -d --build bahia
```

The development database uses disposable credentials. The runtime still has a
bounded optional SQL startup probe. If that probe misses an available index,
the recovery runner probes/migrates it but does **not** install repositories
into a running daemon; restart/recreate the daemon after the index is healthy.
Do not promise hot attachment or use SQL recovery to re-enable paused effects.
See [PostgreSQL loss and recovery](docs/runbooks/postgres-loss-and-recovery.md).

## Diagnostics

```sh
docker compose logs -f bahia relay signet
curl http://localhost:8080/health
curl http://localhost:8080/ready
curl http://localhost:3334/ready
docker compose config --services
```

A successful build, rendered Compose file or health probe is not end-to-end
production acceptance. Verify exact-image signer/relay readiness, EOSE,
per-relay OK, canonical delivery and the relevant live runtime effects.
