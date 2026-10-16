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

Outbound relay traffic uses bounded `nostr.outbound` admission. Its priority
share reserves capacity for assistant checkpoints, session projections and
statuses while startup docs sync is paced through bulk admission. See the
[outbound admission runbook](../runbooks/nostr-outbound-admission.md) for the
default budgets and configuration overrides.

Podman targets accept `runtime.default.podman_host` (environment variable
`BAHIA_RUNTIME__DEFAULT__PODMAN_HOST`) or an environment-specific
`runtime.environments.<name>.podman_host`. Set it to a custom socket such as
`unix:///run/podman/podman.sock`; an explicit `endpoint_ref` instead uses that
endpoint's `docker_host`.

Compose runtime targets require an explicit `execution_mode`: `sdk` uses the
embedded Compose SDK for project apply, observation, deployment, and removal
without a Compose binary; `cli` uses the Compose CLI. SDK mode accepts endpoint
TLS certificate paths directly. Legacy deploy options other than image and
environment overrides require CLI mode.

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
  service_id: "bahia-deployment-1"                  # stable across signer rotation
  operator_root_pubkey: "<independent-fleetops-root-hex>"
  # trust_epoch_inventory_path: "/var/lib/bahia/accepted-inventory.json"
  service_relays: ["wss://relay.example.com"]   # Bahia-controlled relays only; never a public relay
  browser_relays: ["wss://relay.example.com"]
  publish_quorum: 1                               # at most the number of service relays
  # control_plane_publish_quorum: 1                 # optional; explicit value must fit the control-plane pool
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

`nostr.service_id` and `nostr.operator_root_pubkey` enable trust-epoch
verification. Both must be configured together; the root must be independent
of the service signer. An activated operator-root-signed epoch supersedes the
static service pubkey. An optional full accepted-inventory export permits only
its exact-digest, pre-barrier old-key events to be replayed.

For managed routes, `route_canaries.overrides.<hostname>` tunes per-route probe expectations, thresholds, catch-all policy, and post-deploy gate timing without changing signed route state. See [Route Canaries](features/route-canaries.md#configuration) for the keys, defaults, and unmatched-hostname warning.

`edge_routing.provider` selects the default public route provider
(`cloudflare_tunnel` or `caddy`); `edge_routing.zones[].provider` can
override it per zone. Cloudflare requires `backend_ref`, `api_token_ref`,
`account_id`, `tunnel_id`, and each selected zone's `zone_id`. Caddy
requires `caddy_backend_ref`, `caddy_admin_url`, and `caddy_server_id`;
`caddy_credential_ref` is an optional SecretStore bearer credential UUID.
Its admin URL must use HTTPS or loopback HTTP. Caddy owns the public route
and TLS policy, not DNS; configure a Bahia DNS backend separately. See
[Managed DNS and HTTPS Routes](guides/managed-dns-and-https-routes.md).

Secret values belong in Signet's secret store. Point the daemon at it with a dedicated requester key (never the service key) and the Signet agent that owns Bahia's credentials; see [Secret store](../architecture/secret-store.md) and the [runbook](../runbooks/signet-secret-store.md):

```yaml
secret_store:
  backend: signet                                        # empty: no secret store
  signet:
    pubkey: "<signet-management-hex-pubkey>"
    relays: ["wss://signet-relay.example.com"]
    client_key_file: /run/secrets/bahia-secret-store.key # 64-hex requester key, mounted file only
    owner: bahia-vault
    request_timeout: 30s                                 # default
  system_keys:                                           # the daemon's own 32-byte data keys, held in Signet
    assistant_transcript:                                # required with assistant.enabled; first entry seals
      - {key_id: "2026-10", credential_id: cred_<64 hex>, version: 1}
    confidential_dedupe:                                 # without it confidential records are not published
      - {key_id: "2026-10", credential_id: cred_<64 hex>, version: 1}
```

Confidential state (organizations, members, secrets, payments, security
findings) is published only once the `confidential_dedupe` key is configured
and resolved; until then the `system_keys` check fails readiness (`/ready`). Create both keys as
described in the [runbook](../runbooks/signet-secret-store.md#system-credentials).

Serialized configuration (JSON, YAML, log fields) is a diagnostic view: credentials, private keys, bunker URIs, and auth headers are masked. Never save such a view back as the operational configuration.

For OTLP/HTTP telemetry, set `telemetry.enabled: true`, `telemetry.otlp_protocol: http`, and `telemetry.otlp_endpoint` to the collector's HTTP endpoint (for example, `http://otel-collector:4318`). The default protocol remains gRPC when `otlp_protocol` is omitted. The collector sends material runtime-transition logs to VictoriaLogs and unit-state/transition metrics to VictoriaMetrics; configure 30-day retention in those backends. Telemetry uses the OTLP exporter, never the Nostr service key or relay transport.

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

Hive-CI pipeline policies are authored with operator-signed
`bahia hiveci-policy` intents. The `hiveci.policies` configuration only authorizes build-dependency dispatch:
`repo_coordinate`, `workflow_path`, `service_name`, `enabled`, and
`build_dependencies`. It does not seed canonical policies. The former
`environment_name`, `branch_pattern`, and `metadata` config keys are rejected;
put canonical pipeline-policy content in operator-signed `hiveci-policy` intents.

## Daemon config-fabric signer

Daemon config publish and rollback require an independent operator signer even
when the assistant is disabled. For a NIP-46 bunker, add:

```yaml
config_fabric:
  public_key: <64-hex operator pubkey>
  signer:
    method: nip46
    bunker_uri: bunker://<bunker-pubkey>?relay=<url-encoded-relay>
    client_secret_key_file: /run/secrets/bahia-config-fabric-client
```

`method: nip55l` with `signer.nip55l.bus_address` and `app_id` is also
supported. `local` and a raw operator private key are not. The service key
under `nostr` remains separate. Without `config_fabric`, startup succeeds but
`config_fabric_signer` health warns that daemon publish and rollback are
unavailable.

For the **service** identity, `nostr.signer.method: frostr` is a contained
2-of-3 group-key option, not a production rotation. It requires mounted
owner-only share and transport-key files, two pinned peer identities/public
shares, relay endpoints, and injected round coordination plus a cross-process
single-writer fence. Current daemon startup rejects it without those runtime
dependencies; `local`, `nip46` and `nip55l` remain the deployment methods.
See the [service signer runbook](../runbooks/service-signer.md#frostr-threshold-custody-contained-not-a-production-switch).
