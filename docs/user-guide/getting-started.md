# Getting Started with Bahia

This guide takes you from a fresh checkout to a first deployment.

## Prerequisites

- **Docker** and **Docker Compose** for the quick start, or **Go 1.26+** for a local build
- A **Nostr signer**: a NIP-07 browser extension or a NIP-46 bunker for the web app, and a key file or bunker for the CLI
- **PostgreSQL 16+** is optional. The daemon keeps its truth on the relays and in its local event store; PostgreSQL is a derived index that enables a few HTTP reads and the OCI proxy.

## Quick start with Docker Compose

```bash
git clone https://github.com/openagentsinc/bahia.git
cd bahia
docker compose up --build

curl http://localhost:8080/health      # {"status":"ok", ...}
open http://localhost:3000
```

Compose starts the daemon (`:8080`), the relay sidecar (`:3334`), PostgreSQL (`:5432`), and the web app (`:3000`). The web container receives its bootstrap seed — the sidecar URL and the daemon's service pubkey — as `PUBLIC_BAHIA_BOOTSTRAP_RELAYS` and `PUBLIC_BAHIA_SERVICE_PUBKEYS` at container start.

## Local build

```bash
make deps
make build            # bin/bahia-server, bin/bahia, …
make run-dev          # go run ./cmd/server -config config.yaml
```

If you run with PostgreSQL, create the database and apply the schema before the first start: `make migrate MIGRATE_CONFIG=config.yaml` (`MIGRATE_ACTION=status` reports pending migrations without writing).

## Configuration

The daemon reads `-config <file>` (YAML) and then environment variables prefixed `BAHIA_`. `BAHIA_<SECTION>_<FIELD>` maps to `section.field` (`BAHIA_DB_HOST` → `db.host`); use `__` for deeper nesting (`BAHIA_NOSTR__SIDECAR__ENABLED`).

```yaml
server:
  host: "127.0.0.1"          # default
  port: 8080                 # default

db:                          # optional derived index
  host: "localhost"
  port: 5432
  user: "bahia"
  password: ""
  name: "bahia"
  sslmode: "require"         # default
  startup_probe_timeout: 2s  # optional SQL connect/migration startup budget; max 5s (0 uses default)

auth:
  enabled: true              # NIP-98 on the HTTP surface; default false
  bootstrap_owner_pubkeys: ["<your-hex-pubkey>"]

nostr:
  private_key: "<service-hex-key>"
  relays: ["wss://relay.example.com"]
  browser_relays: ["wss://relay.example.com"]
  authorized_pubkeys: ["<fleet-operator-hex-pubkey>"]
  bootstrap_owners:
    "11111111-1111-1111-1111-111111111111": "<org-owner-hex-pubkey>"
  sidecar:
    enabled: true
    listen_addr: "0.0.0.0:3334"
    public_url: "wss://relay.example.com"
    data_dir: "/var/lib/bahia/relay-sidecar"
```

Everything a client can read or change goes through the relays, so the two values that matter most are `nostr.private_key` (the service identity) and the relay list. `nostr.authorized_pubkeys` names the fleet operators who may act outside any organization (policies, DNS, workers, ML, tool decisions, fleet rekeys, MCP over HTTP). See [Nostr Integration](nostr-integration.md#relays) for the full relay and sidecar reference, and each feature page for its own section (`adoption`, `direct_runtime_actions`, `dns`, `hiveci`, `llm`, `soul_factory`, `assistant`, `supervision`, `route_canaries`, `virtualization`).

Serialized configuration (JSON, YAML, log fields) is a diagnostic view: credentials, private keys, bunker URIs, and auth headers are masked. Never save such a view back as the operational configuration.

## Your first deployment

### 1. Sign in

Open `http://localhost:3000` and click **Sign in with Nostr**. Choose your NIP-07 extension or paste a NIP-46 bunker URI. The session is verified by signature; your roles come from the organization membership records on the relay. **Edit Profile** (`/settings/profile`) publishes your kind-0 metadata through the same signer.

### 2. Create an organization

Every service belongs to an organization. The configured bootstrap owner (or a fleet operator) creates it:

```bash
bahia --relay wss://relay.example.com --service-pubkey <service-pubkey> \
  orgs create acme --display-name "ACME"
bahia orgs members add <org-id> <teammate-pubkey> --role deployer
```

See [Organizations](features/organizations.md).

### 3. Create a service

In **Services**, use **Create Service**; or:

```bash
bahia services create --org "$ORG" --name my-api --artifact-repo ghcr.io/acme/my-api \
  --repo-source gitea --repo-coordinate acme/my-api --clone-url https://git.example/acme/my-api.git
```

```json
{"name": "bahia_create_service", "arguments": {"org_id": "<org-uuid>", "name": "my-api", "artifact_repo": "ghcr.io/acme/my-api"}}
```

All three publish the same signed `service` intent; the web app shows a **pending** badge until the daemon's status and canonical record arrive.

### 4. Create an environment

In **Environments**, use **Create Environment**; or `bahia environments create --org "$ORG" --name staging --unit-runtime-type compose --unit-endpoint-ref <endpoint-ref> --unit-compose-dir /srv/my-api`. An environment names where the service runs (its deployment units) and how changes are reconciled (`observe_only`, `auto_apply`, `approval_required`). See [Environments](features/environments.md).

### 5. Get an artifact

Artifacts are digest-pinned images with provenance. The normal path is a governed build: `bahia builds request --service <id> --git-ref main --credential-ref <secret-id> --artifact-repo ghcr.io/acme/my-api`, after which the daemon registers the artifact from signed Hive-CI evidence. See [Builds](features/builds.md) and [Artifacts](features/artifacts.md).

### 6. Deploy

From the service page choose **Deploy**, pick the environment and artifact, review the desired-state preview, and submit. From the CLI:

```bash
bahia deployments preview --service <service-id> --environment <env-id> --artifact <artifact-id>
bahia deploy --org "$ORG" --service <service-id> --environment <env-id> --artifact <artifact-id> \
  --expected-desired-state-hash <hash-from-preview>
```

A protected environment, or a policy with `enforcement: block`, holds the intent for approval (**Deployments → Pending Approvals**, `bahia deployments approve`).

### 7. Follow progress

**Deployments** shows the intent, its run, and run logs. On the relays the same progress is the `30315` status addressed to your pubkey, the `deployment-intent` and `deployment-run` records, and `4903` audit facts; the service's `service-state` record reports the observed artifact and drift once the runtime converges.

## The flow at a glance

```
operator signs intent ──▶ relay ──▶ daemon validates, authorizes, applies
                                      │
                                      ├─▶ 30315 status to the requester
                                      ├─▶ 30900 canonical records (intent, run, state)
                                      └─▶ 4903 audit facts
runtime reconciles ──▶ runtime observation ──▶ service-state (observed vs desired, drift)
```

## Next steps

- [Core Concepts](core-concepts.md) — the data model
- [CLI Reference](cli-reference.md) — every command and flag
- [Notifications](features/notifications.md) — alerts for failed deployments and policy breaches
- [Troubleshooting](troubleshooting.md) — relay, signer, and authorization problems
