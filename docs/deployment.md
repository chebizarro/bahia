# Deployment Guide

This guide covers how to run Bahia in different environments.

## Prerequisites

- Go 1.24+
- PostgreSQL 16+
- Docker, Podman, or Kubernetes (for runtime observation)

## Quick Start with Docker Compose

```bash
# Clone and start
cd bahia
docker compose up --build

# The API is available at http://localhost:8080
curl http://localhost:8080/health
```

## Manual Setup

### 1. Database

```bash
# Create the database
createdb bahia

# Migrations run automatically on server start
```

### 2. Configuration

Copy and edit the config file:

```bash
cp config.yaml config.local.yaml
# Edit config.local.yaml with your settings
```

Or use environment variables:

```bash
export BAHIA_DB_HOST=localhost
export BAHIA_DB_PORT=5432
export BAHIA_DB_USER=bahia
export BAHIA_DB_PASSWORD=bahia
export BAHIA_DB_NAME=bahia
```

### 3. Build and Run

```bash
# Build
make build

# Run server
./bin/bahia-server -config config.local.yaml

# Or with make
make run-dev
```

### 4. CLI

```bash
# Build CLI
make build-cli

# List services
./bin/bahia services list

# Deployments are signer-first Nostr operations.
# Publish a ContextVM service/deploy request (kind 25910, wrapped with 1059/21059 when encrypted) and subscribe for canonical observables.
# Legacy REST-backed deploy CLI paths are deprecated until they publish signed events directly.
```

## Production Deployment

### Docker

```bash
# Build image
make docker

# Run
docker run -p 8080:8080 \
  -e BAHIA_DB_HOST=db.example.com \
  -e BAHIA_DB_PASSWORD=secret \
  bahia:latest
```

Backend, relay, CLI, bridge, sidecar, and web artifacts are stamped with SemVer component versions. The backend Dockerfile accepts `VERSION_BASE` (default `0.1.0`), `GIT_COMMIT` (default `dev`), and optional full `VERSION` build args; Compose passes the same defaults to the backend and relay images. The web Dockerfile accepts `PUBLIC_BAHIA_WEB_BASE_VERSION`, `PUBLIC_BAHIA_GIT_COMMIT`, and optional `PUBLIC_BAHIA_WEB_VERSION`. Release automation should pass the same commit hash to both backend and web builds so Settings displays matching `0.1.0-<commit-hash>` provenance.

### Environment Variables

See `.env.example` for all available configuration options.

Nested runtime target settings use double underscores in environment variables:

```bash
export BAHIA_RUNTIME__DEFAULT__TYPE=compose
export BAHIA_RUNTIME__DEFAULT__EXECUTION_MODE=cli
export BAHIA_RUNTIME__DEFAULT__COMPOSE_DIR=/srv/bahia/compose/default
export BAHIA_RUNTIME__DEFAULT__BAHIA_OWNED=true
export BAHIA_RUNTIME__ENVIRONMENTS__production__COMPOSE_DIR=/srv/bahia/compose/production
export BAHIA_RUNTIME__ENVIRONMENTS__production__BAHIA_OWNED=false
export BAHIA_RUNTIME__ENVIRONMENTS__production__ENDPOINT_REF=prod-docker
export BAHIA_RUNTIME__ENDPOINTS__prod-docker__DOCKER_HOST=tcp://docker-prod.example.com:2376
export BAHIA_RUNTIME__ENDPOINTS__prod-docker__CA_CERT_FILE=/etc/bahia/docker/ca.pem
export BAHIA_RUNTIME__ENDPOINTS__prod-docker__CLIENT_CERT_FILE=/etc/bahia/docker/cert.pem
export BAHIA_RUNTIME__ENDPOINTS__prod-docker__CLIENT_KEY_FILE=/etc/bahia/docker/key.pem
```

## Runtime Targeting

Bahia supports a process-wide runtime default plus per-environment runtime targets:

```yaml
runtime:
  # Legacy flat keys are still accepted as fallback defaults.
  type: docker
  docker_host: unix:///var/run/docker.sock

  default:
    type: compose
    # Compose control mode: "cli" (compose CLI compatibility) or "sdk"
    # (embedded Compose v5 Go SDK, in-process over the Engine API).
    execution_mode: cli
    docker_host: unix:///var/run/docker.sock
    compose_dir: /srv/bahia/compose/default
    bahia_owned: true

  endpoints:
    prod-docker:
      docker_host: tcp://docker-prod.example.com:2376
      ca_cert_file: /etc/bahia/docker/ca.pem
      client_cert_file: /etc/bahia/docker/cert.pem
      client_key_file: /etc/bahia/docker/key.pem
      # Compatibility only; prefer verified TLS for live endpoints.
      insecure_skip_verify: false

  environments:
    staging:
      compose_dir: /srv/bahia/compose/staging
      # false records that the operator has not approved authoritative generation.
      # A valid .bahia/render-state.json marker can still prove Bahia ownership.
      bahia_owned: false
    production:
      endpoint_ref: prod-docker
      compose_dir: /srv/bahia/compose/production
      bahia_owned: false
```

Resolution order is: legacy flat `runtime.*`, then `runtime.default.*`, then `runtime.environments.<environment-name>.*`, then the persisted `Environment.runtime_config` keys (`type`, `endpoint_ref`, `docker_host`, `podman_host`, `compose_dir`, `bahia_owned`, `kube_context`, `kube_namespace`, `kube_config`). When `endpoint_ref` is present, Bahia resolves the concrete Docker host and TLS material from server-managed `runtime.endpoints` and does not need callers or imported environments to carry raw Docker credentials. A service's `runtime_type` remains authoritative for whether Bahia uses Docker, Compose, Kubernetes, or Podman; environment-specific `type` overrides are rejected if they conflict with the service.

Docker API access accepts individual CA/client certificate file paths. Docker Compose uses the Docker CLI `DOCKER_CERT_PATH` convention, so configured Compose endpoint certificates must live in one directory with Docker's standard names (`ca.pem`, `cert.pem`, `key.pem`).

### Runtime execution mode

Runtime apply results report `execution_mode`:

- `engine_api`: Docker and Podman control clients mutate runtime resources directly through the Docker-compatible Engine API.
- `cli`: Compose control executes through the configured `docker compose`/`docker-compose` CLI compatibility path.
- `sdk`: Compose desired-state apply executes in-process through the embedded Docker Compose v5 Go SDK (`github.com/docker/compose/v5`), talking to the Docker Engine API directly with the same semantics as the CLI path (`config -q`, `up -d --remove-orphans`, `--pull`, `--no-deps`). Scope: `sdk` currently governs desired-state apply (validate/up). Observation (`compose ps`) and the legacy per-service deploy path still execute through the compose CLI, so the compose CLI must remain available on the host for those operations.

Compose control mode is not implicit. Any Docker Compose runtime target must set `execution_mode: cli` or `execution_mode: sdk` (or `BAHIA_RUNTIME__...__EXECUTION_MODE=cli|sdk`). Docker and Podman use `engine_api`. Podman Compose targets support only `execution_mode: cli`, because `podman-compose` is a separate implementation the embedded SDK does not drive.

The `sdk` mode uses the endpoint's Docker host and TLS material directly (individual `ca_cert_file`/`client_cert_file`/`client_key_file` paths); it does not depend on the `DOCKER_CERT_PATH` directory convention required by the CLI path.

In desired-state apply, Compose business logic remains above the execution transport (CLI or SDK). Bahia selects the target deployment unit, renders the unit-owned full Compose project into that unit's `compose_dir`, enforces the Compose ownership gate for that directory before writing, validates the staged render through the Compose executor control seam, and then applies the full project with `up -d --remove-orphans`. The desired-state path does not use per-service image environment substitution, service-scoped `up`, or unconditional `--force-recreate`.

Compose ownership is recordable per runtime target with `bahia_owned`. Set `bahia_owned: true` only after an operator has confirmed the directory is dedicated to Bahia authoritative generation. `bahia_owned: false` or an unset value does not grant ownership; Bahia will still allow the target if the directory contains a valid `.bahia/render-state.json` marker written by prior Bahia rendering. Unknown, missing, malformed, or operator-authored directories are blocked before staging or file writes. Checked-in staging/production examples record `bahia_owned: false`, so rollout remains blocked until an operator confirms ownership or Bahia has written a valid marker.

Generated Compose env files live under `.bahia/env/` inside the Bahia-owned project. They may contain resolved secret values required by Docker Compose, so operators must protect the directory with deployment-appropriate ownership and permissions and treat it as runtime secret material. Nostr events, apply metadata summaries, logs, desired-state snapshots, and normalized observations must use redacted secret refs or key-presence metadata instead of those values.

### Max direct-runtime Compose target

Model a host such as `max` as a normal Compose deployment unit, not as a new runtime type:

```json
{
  "key": "max",
  "display_name": "Max Compose",
  "runtime_type": "compose",
  "endpoint_ref": "max",
  "compose_dir": "/srv/bahia/compose/gastown",
  "ownership_mode": "bahia_managed",
  "reconcile_mode": "approval_required",
  "runtime_config": {
    "execution_mode": "sdk"
  }
}
```

`endpoint_ref` must name a server-managed `runtime.endpoints` alias, and `compose_dir` must be a directory dedicated to Bahia's full-project rendering. Set `ownership_mode` to `bahia_managed`; the runtime ownership gate must also be satisfied by operator-approved `bahia_owned: true` configuration or a valid Bahia render marker. Choose the unit's reconcile policy explicitly (`observe_only`, `auto_apply`, `approval_required`, or `disabled`) and set `runtime_config.execution_mode` explicitly to `sdk` or `cli`.

The workload desired state supplies the operational details that are not part of the deployment-unit record:

- Use named-volume mounts such as `gastown-data:/var/lib/gastown` for durable state. Reapplying or restarting the Compose project must reuse the named volume rather than container-local storage.
- Keep NIP-46 bunker URIs, persistent client keys, and other signer material out of command arguments and signed payloads. Materialize them as permission-restricted files, mount them read-only through desired-state volume entries, and configure the application with non-secret file-path environment variables. When a value must be injected as an environment variable, use a Bahia secret reference so apply writes it only to the protected generated env file under `.bahia/env/`; plaintext is excluded from desired state, events, metadata, and logs.
- Set an explicit restart policy (for example, `unless-stopped`) and a real healthcheck in each critical service's desired state. A successful apply renders these into the Bahia-owned Compose project; subsequent observation and reconciliation use the persisted desired state.

Compose routing is fail closed. Once the resolved deployment unit has `runtime_type: compose`, Bahia requires `ownership_mode: bahia_managed`, a non-empty managed `endpoint_ref`, a non-empty `compose_dir`, and an available runtime lifecycle. Missing or invalid configuration, render/apply errors, and unhealthy results fail the deployment; Bahia does not fall back to a Loom job or the obsolete bare `docker run` path.

## Deployment Units and Targeting

Environment targeting is typed and additive. Each environment owns a `targeting` object with `default_unit_key`, `failure_domain_labels`, `secret_scope_mode`, and `default_reconcile_mode`. `default_reconcile_mode` accepts `observe_only`, `auto_apply`, `approval_required`, or `disabled`; `secret_scope_mode` accepts `service`, `environment`, or `unit`.

A deployment unit is the runtime ownership boundary inside an environment. Each unit records its environment reference, runtime type (`docker`, `compose`, `kubernetes`, or `podman`), endpoint reference, Compose directory, namespace, network profile, reconcile mode override, ownership mode (`bahia_managed`, `adopted`, or `external`), and unit-local runtime configuration.

Reconcile policy is persisted on `environments.targeting.default_reconcile_mode` and, for explicit units, `deployment_units.reconcile_mode`. Explicit unit policy overrides the environment default; implicit default-unit rows use the environment default.

Scheduled reconciliation always observes through the resolved runtime and compares desired state with normalized observations when desired-state hashes are present, falling back to desired artifact digest comparison for legacy rows. `observe_only` records observations and drift only. `approval_required` records drift as `remediation_needed` and waits for an authorized operator-authored ContextVM `service/drift-remediate` request; Bahia does not synthesize a public remediation request internally. `auto_apply` invokes the same runtime lifecycle desired-state deploy helper used by deploy/apply operations, using the persisted desired artifact and desired-state snapshot.

Runtime mutation is serialized by the same environment apply lock as user deploy/apply. User-initiated deploys acquire the lock in blocking mode and therefore preempt scheduled auto-remediation. Scheduled `auto_apply` attempts the lock without blocking; if another operation holds it, Bahia keeps the desired state, records failure metadata on `environment_service_state.reconcile_failure_metadata`, sets `reconcile_backoff_until`, increments `reconcile_consecutive_failures`, and retries only after backoff. Apply failures follow the same rule: desired state remains authoritative, failure details are stored, and future scheduled attempts back off. `disabled` excludes the environment or unit from scheduled observation.

Existing single-runtime environments do not need a persisted unit row. When no explicit `deployment_units` row exists, Bahia resolves an in-memory default unit from typed environment targeting first, then falls back to legacy `Environment.runtime_config` keys. The implicit default is used for planning and placement resolution, but its ID remains absent from state, intent, run, and observation rows until an operator creates a real unit boundary.

Backward compatibility is read-tolerant and write-forward. Runtime fields moved into typed targeting are read from typed fields first and then from `runtime_config` (`default_unit_key`, `failure_domain_labels`, `secret_scope_mode`, `default_reconcile_mode`, `reconcile_mode`, `type`, `endpoint_ref`, `compose_dir`, `namespace`, `kube_namespace`, and `network_profile`). Environment and unit writes normalize those values into typed targeting columns/JSON so new reads do not depend on raw runtime JSON alone.

Environment and deployment-unit writes are signer-first. An authorized signer publishes ContextVM `environment/create` or `environment/update` with this payload shape (create requires `name`; update requires `id`):

```json
{
  "org_id": "organization-uuid",
  "name": "production",
  "id": "environment-uuid",
  "expected_updated_at": "2026-08-02T08:00:00Z",
  "loom_worker_selector": {},
  "runtime_config": {},
  "targeting": {
    "default_unit_key": "max",
    "failure_domain_labels": {},
    "secret_scope_mode": "service",
    "default_reconcile_mode": "approval_required"
  },
  "reconcile_mode": "approval_required",
  "deployment_units": [
    {
      "key": "max",
      "display_name": "Max Compose",
      "runtime_type": "compose",
      "endpoint_ref": "max",
      "compose_dir": "/srv/bahia/compose/gastown",
      "namespace": "",
      "network_profile": {},
      "ownership_mode": "bahia_managed",
      "reconcile_mode": "approval_required",
      "runtime_config": {"execution_mode": "sdk"}
    }
  ],
  "deploy_strategy": "replace",
  "protected": true
}
```

`deployment_units` has complete-set semantics. Omitting it leaves the current unit set unchanged; providing it atomically replaces the complete explicit set; providing `[]` returns the environment to its implicit default. Every update that supplies `deployment_units` must also supply `expected_updated_at` from the latest environment read. Bahia locks the environment row and rejects a stale revision with ContextVM error code `-32009` before publishing canonical registry state or writing the environment/unit transaction. Bahia also rejects removal of units referenced by state, runs, intents, or observations, and requires `targeting.default_unit_key` to identify a member of any non-empty explicit set. Deploy, apply, and observe do not materialize the implicit unit.

Environment GET and list responses embed `deployment_units`. They contain the persisted explicit units when present; otherwise they contain the resolved default unit marked with `"implicit": true`.

The CLI uses the same contract and never revives REST mutations:

```bash
bahia environments create --name production --units-file units.json
bahia environments update <environment-id> --units-file units.json
bahia environments units list <environment-id>
bahia environments units create <environment-id> --file unit.json --default-unit-key max
bahia environments units update <environment-id> max --file unit.json --default-unit-key max
```

The unit `create` and `update` helpers read the environment, merge the requested unit locally, and publish a signed `environment/update` carrying the complete explicit unit set plus its `expected_updated_at` revision. On revision conflict the CLI rereads, deliberately remerges, and resigns at most three attempts so unrelated concurrent units are preserved; the final conflict is surfaced to the operator. `--default-unit-key` changes targeting in the same atomic update, including implicit-to-explicit transitions to a non-`default` key. Use JSON files for unit specifications and secret-bearing configuration; do not place secrets in arguments.

The core control-plane tables include nullable `deployment_unit_id` foreign keys on `deployment_intents`, `deployment_runs`, `runtime_observations`, and `environment_service_state`. A `NULL` value means the record belongs to the implicit default unit for the environment. API request DTOs accept additive `deployment_unit_id`, `deployment_units`, `targeting`, and `reconcile_mode` fields. Canonical Nostr projections (`30900` state and `30078` app data) include additive `unit` tags; `NULL` placement is tagged as `default`. Historical `31961`, `31963`, `31967`, and `31968` projections are migration inventory only.

For adoption, prefer endpoint aliases:

```bash
bahia adopt scan --target prod-docker
bahia adopt import --target prod-docker --all
```

Raw Docker hosts are a compatibility path only. They require the server to set `adoption.allow_raw_docker_hosts: true` and the CLI to use `--raw-target alias=dockerHost`.

For production rollout, follow the signer-first operator runbook in [`adoption-production-rollout.md`](adoption-production-rollout.md). In short: configure signer/operator pubkeys and relay discovery, keep raw-host mode off, run a signer-first scan-only dry run, import a single low-risk workload first, then monitor correlated adoption/runtime events, logs, and `/metrics` before expanding. Use `bahia adopt import --org <organization-uuid>` whenever the organization cannot be inferred unambiguously.

A successful import creates or reuses a deployment unit for the imported service and binds both `environment_service_state` and the initial runtime observation to it. Cross-organization service/environment reuse is rejected. Explicit import may take over a same-name legacy service that has no adopted-runtime identity, but an already-adopted same-name service on a different target remains a conflict.

Signer-first adoption/import and direct-runtime events remain subject to relay, authorization, and reactor-side operator controls; the legacy per-IP REST mutation limiters are no longer mounted.

## Rollout and Runtime Failure Semantics

Canary and blue/green plans require a runtime implementing verifiable traffic transitions. Bahia checks the runtime-reported target slot and weight after a shift/switch; unsupported runtimes, rejected changes, or mismatched reported state fail the step instead of logging success.

Before rollout, Bahia captures the prior primary artifact. Automatic rollback restores progressive traffic, restores or removes the primary as appropriate, verifies artifact identity and healthy runtime state, and cleans up canary/green slots. Only a fully persisted verified restoration emits `rollout.rolled_back`; any restoration, cleanup, observation, or persistence error produces terminal `rollback_failed` state and a `rollout.rollback_failed` event.

Health-observer errors count toward the configured consecutive failure threshold just like unhealthy observations, so an unavailable observer fails fast instead of waiting for the full gate timeout.

Native encrypted `service/deploy` accepts UUID service/environment/artifact IDs, evaluates deployment policy, creates an intent with a desired-state snapshot, and executes the runtime lifecycle immediately only when approval policy marks the intent approved. The run is completed as failed when runtime apply fails.

Loom-backed non-terminal runs are monitored for missing kind-`30100` status. After `nostr.stale_run_after` (default `5m`), Bahia publishes replaceable NIP-38 `30315` health state with schema `bahia.deployment-run-health.v1`; it publishes a `recovered` transition when Loom status resumes, the job changes, or the run becomes terminal.

## Desired-State Persistence

Bahia persists desired-state metadata additively so deploy, observe, and projection paths can compare deterministic state without relying on image digest alone:

- `deployment_intents.desired_state` / `deployment_intents.desired_hash` store the canonical `DesiredServiceSpec` snapshot and hash accepted for a deploy intent.
- `deployment_runs.apply_metadata` stores runtime apply metadata such as renderer, revision, resources, and warnings.
- `environment_service_state.desired_runtime_state` / `environment_service_state.desired_hash` store the current desired runtime snapshot for the service/environment row.
- `runtime_observations.normalized_state` / `runtime_observations.normalized_hash` store normalized observed runtime state for drift comparison.

For desired-state-managed workloads, service drift is evaluated from deterministic hashes: `environment_service_state.desired_hash` is compared with the latest observation's normalized hash (`normalized_state.observation_hash`, falling back to `normalized_hash`). The resulting `drift_status` is `in_sync` only when hashes match and runtime health is acceptable, `drifted` when hashes differ or matching state is unhealthy, and `unknown` when the desired or observed hash is unavailable. Workloads without desired-state metadata continue to use the legacy desired artifact digest versus observed image digest comparison.

`DesiredServiceSpec` includes deployment-unit identity: `deployment_unit_id` when a persisted unit exists, `deployment_unit_key` for implicit or explicit unit grouping, and `unit_runtime_type` for renderer/runtime ownership. Older snapshots without those fields are normalized into the implicit `default` unit during planning.

`DesiredEnvironmentPlan` is both environment-scoped and unit-scoped. The flat `services` list remains available for existing renderers, and `unit_plans` groups the same services by deployment unit. Each unit plan computes a unit `revision_hash` from its unit identity, runtime type, and sorted service desired hashes. The environment `revision_hash` is an aggregate over sorted unit revision hashes, so moving a service between units or changing one unit's desired state changes the aggregate deterministically.

Compose dependencies are unit-local. During plan assembly Bahia rejects `depends_on` edges that reference a service in a different deployment unit; operators must colocate those services in one Compose-owned unit or express the relationship through runtime/network configuration instead of a cross-unit Compose graph.

Secret plaintext is never stored in desired-state or normalized-observation JSON. Desired-state secret entries use redacted references only.

## Podman Runtime

Bahia supports Podman as an alternative to Docker. Since Podman emulates Docker's API, Bahia communicates with Podman via its Docker-compatible socket.

### Configuration

```yaml
runtime:
  type: podman
  # Rootless Podman (default if omitted):
  podman_host: unix:///run/user/1000/podman/podman.sock

  # Or for rootful Podman:
  # podman_host: unix:///run/podman/podman.sock
```

### Socket Paths

| Mode | Socket Path |
|------|-------------|
| Rootless | `unix:///run/user/<UID>/podman/podman.sock` |
| Rootful | `unix:///run/podman/podman.sock` |

### Enabling the Podman Socket

Podman's API socket is not enabled by default. Enable it with:

```bash
# Rootless (user service)
systemctl --user enable --now podman.socket

# Rootful (system service)
sudo systemctl enable --now podman.socket
```

### Environment Variables

```bash
export BAHIA_RUNTIME__DEFAULT__TYPE=podman
export BAHIA_RUNTIME__DEFAULT__PODMAN_HOST=unix:///run/user/1000/podman/podman.sock
```

For Compose desired-state deploys, Bahia intentionally owns the generated Compose project for the environment or deployment unit. Multiple environments can point to different `compose_dir` values, and services in the same Compose-owned unit share that generated project. Bahia writes the service image directly into the rendered model from the desired-state snapshot; operators should not rely on the old service-name-derived `<SERVICE>_IMAGE` override pattern for desired-state-managed deploys.

When running Bahia inside a container with the Compose runtime, mount both `/var/run/docker.sock` and the configured Compose project directory at the same path used by `runtime.default.compose_dir` or the per-environment `compose_dir`. The mounted directory must be Bahia-owned or carry a valid `.bahia/render-state.json` marker before authoritative generation is allowed.

## OCI Registry Configuration

Bahia includes an internal OCI-compliant container registry. Configure it in your config file:

```yaml
oci:
  enabled: true
  public_host: registry.sharegap.net
  spool_dir: /srv/data/bahia/oci-uploads
  upload_expiry: 24h
  
  # Anonymous pull from internal network
  allow_anonymous_pull_cidrs:
    - 192.168.40.0/24
    - 10.0.0.0/8
  
  # Trusted proxies for X-Forwarded-For
  trusted_proxy_cidrs:
    - 127.0.0.1/32
    - 172.17.0.0/16
  
  # Service accounts for push access
  service_accounts:
    - username: hive-ci
      password_hash: "$2a$10$..."  # bcrypt hash
      permissions: [pull, push]
      repo_prefixes: [cascadia/]
```

### Blossom Backend

The registry uses Blossom for blob storage. Ensure Blossom is configured:

```yaml
blossom:
  base_url: https://blossom.sharegap.net
  auth_pubkey: <your-blossom-auth-pubkey>
```

### Spool Directory

Create the spool directory for upload chunks:

```bash
mkdir -p /srv/data/bahia/oci-uploads
chown bahia:bahia /srv/data/bahia/oci-uploads
```

## Hive-CI Integration

Enable automatic CI event ingestion:

```yaml
hiveci:
  enabled: true
  
  # Trusted CI dispatcher pubkeys for external Hive-CI kind-5401 workflow-run events
  trusted_ci_pubkeys:
    - <hive-ci-dispatcher-pubkey>
  
  # Auto-create builds from CI results
  auto_register_builds: true
  
  # Auto-deploy to staging environment
  auto_deploy_staging_environment: edge-01-staging
  
  # Retry configuration
  retry_interval: 30s
  max_retries: 10
```

## Monitoring

- Liveness/health snapshot: `GET /health`
- Active-tier readiness: `GET /ready` (`503` when required checks fail)
- Drift detection: `GET /api/v1/state/drifted`
- Registry API: `GET /v2/`
