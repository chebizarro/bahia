# Core Concepts

This guide explains the fundamental concepts in Bahia and how they work together.

## The Deployment Model

Bahia manages deployments through a **desired state** model:

1. You declare what **should** be running (desired state)
2. Workers **apply** the desired state
3. Observers **report** what's actually running (observed state)
4. Bahia **detects drift** between desired and observed
5. Remediation **corrects** drift when configured

## Primary Entities

### Service

A **Service** is an application you deploy — a web API, worker process, or any containerized workload.

```yaml
# Example service (fields from the service read model)
name: "payment-api"
org_id: "<org-uuid>"
artifact_repo: "ghcr.io/company/payment-api"
repo_url: "https://github.com/company/payment-api"
default_branch: "main"
runtime_type: "compose"
```

Key attributes:
- **name**: Unique identifier
- **artifact_repo**: Registry repository that CI publishes images to (required)
- **repo_url** / **repository**: Source code location, including structured Git or NIP-34 repository metadata
- **runtime_type**: Runtime used for deployments (`compose` by default in the CLI)

### Environment

An **Environment** is a deployment target — staging, production, edge, etc.

```yaml
# Example environment
name: "production"
org_id: "<org-uuid>"
deploy_strategy: "replace"   # replace, blue_green, or canary
protected: true
targeting:
  default_unit_key: "default"
deployment_units:
  - key: "default"
    runtime_type: "compose"     # docker, compose, kubernetes, podman, vm-firecracker, vm-qemu
    endpoint_ref: "prod-docker"
```

Environments carry:
- **Deployment units** — explicit runtime targets, or an implicit default when none are declared
- **Protection and approval** — `protected` environments require approval for every deployment; [policies](features/policies.md) can block or warn
- **Reconcile modes** — `observe_only`, `auto_apply`, `approval_required`, or `disabled`
- **Secret scope** — `service`, `environment`, or `unit`

### Build

A **Build** represents a CI workflow execution that produces deployable output.

```yaml
# Build metadata from CI
service_id: "svc-123"
ci_system: "hiveci"
ci_run_id: "run-123"
git_sha: "abc123def"
git_ref: "main"
status: "succeeded"   # queued, running, succeeded, failed, cancelled
```

Bahia integrates with CI systems through:
- **Hive-CI** — trusted signed results (kind `5402` carrying `BAHIA_ARTIFACT`) register builds and digest-pinned artifacts automatically
- **Manual recovery registration** — `bahia artifacts register`, only when `hiveci.allow_manual_artifact_registration` is enabled
- **Observed-image import** — `bahia artifacts import-observed`, only when `hiveci.allow_live_artifact_import` is enabled

> **NOTE (2026-09-11):** there is no generic CI webhook receiver in the current HTTP router; non-Hive-CI systems should publish through the signer-first flows above.

### Artifact

An **Artifact** is an immutable container image with metadata.

```yaml
# Example artifact
service_id: "svc-123"
build_id: "build-456"
image_repo: "registry.example.com/payment-api"
image_tag: "v2.1.0"
image_digest: "sha256:abc123..."
scan_status: "unknown"
metadata:
  git_commit: "abc123"
```

Artifacts are immutable — once registered, their digest never changes.

### Deployment Intent

A **Deployment Intent** is a request to deploy an artifact to an environment.

```yaml
# Deployment intent
service_id: "svc-123"
environment_id: "env-456"
deployment_unit_id: "unit-1"
artifact_id: "art-789"
requested_by: "npub1..."
approval_status: "pending"   # not_required, pending, approved, rejected
status: "pending"
```

Intents go through a lifecycle (`status`):
1. **pending** → Intent submitted; may be waiting on approval (`approval_status: pending`)
2. **approved** → Ready to execute
3. **deploying** → Run in progress
4. **deployed** / **failed** / **rejected** → Terminal outcomes
5. **superseded** / **rolled_back** → Replaced by a newer intent or a rollback

### Deployment Run

A **Deployment Run** is a concrete execution of a deployment intent.

```yaml
# Deployment run
deployment_intent_id: "intent-123"
worker_pubkey: "npub1worker..."
status: "running"
started_at: "2024-01-15T10:35:00Z"
```

Runs track:
- Execution status and progress
- Worker assignment
- Logs and output
- Runtime observations

### Runtime Observation

An **Observation** is a snapshot of what's actually running.

```yaml
# Runtime observation
service_id: "svc-123"
environment_id: "env-456"
deployment_unit_id: "unit-1"
observed_image_repo: "registry.example.com/payment-api"
observed_image_digest: "sha256:abc123..."
health_status: "healthy"   # unknown, starting, healthy, unhealthy, stopped
observed_at: "2024-01-15T10:40:00Z"
```

Observations enable drift detection by comparing:
- **Desired state** (from the latest deployed intent)
- **Observed state** (image digest and normalized runtime state from inspection)

### Drift

**Drift** occurs when observed state doesn't match desired state.

Causes of drift:
- Manual container restarts
- Out-of-band deployments
- Container crashes and restarts
- Configuration changes

Bahia can:
- **Alert** on drift via notifications
- **Auto-remediate** drift when the environment or unit reconcile mode is `auto_apply`
- **Surface** drift on the **Environment States** page and via `bahia state drifted`

## Nostr Event Model

Bahia is **Nostr-native** — it uses Nostr events as the primary control plane.

### Event Categories

| Category | Kind(s) | Purpose |
|----------|---------|---------|
| **ContextVM intents** | `25910`, optionally wrapped in `1059` or `21059` | Signed JSON-RPC mutation requests, immediate acknowledgments, and encrypted transport |
| **Canonical state** | `30900`, `30078` | Current control-plane state projections and app-specific data |
| **Canonical status/audit** | `30315`, `4903` | Operational progress, terminal facts, provenance, and audit |
| **Assistant transcript** | `30316` | Encrypted assistant transcript entries using a service-held symmetric-key AEAD envelope and key-reference/rotation tags |
| **Discovery and relays** | `11316`-`11320`, `30002` | ContextVM announcements and NIP-51 relay topology |

Legacy Bahia custom ranges (`5961`-`6006`, `6961`-`6997`, `7961`-`7997`, `31961`-`32003`, `38390`-`38431`, `5980`, `7980`) are startup migration inventory only.

### Canonical Observables

**Canonical observables** are signed Nostr events that reflect durable truth after a ContextVM intent is acknowledged.

```json
{
  "kind": 30900,
  "content": "{\"service_id\":\"svc-123\",\"environment_id\":\"env-456\",\"desired_artifact\":\"art-789\",\"observed_artifact\":\"art-789\",\"status\":\"healthy\"}",
  "tags": [
    ["d", "service:svc-123:env-456"],
    ["domain", "service"],
    ["schema", "bahia.service-state.v1"],
    ["service", "svc-123"],
    ["environment", "env-456"]
  ]
}
```

Benefits of canonical observables:
- **Real-time updates** via scoped subscriptions
- **Offline resilience** (cached locally)
- **Multi-client sync** (all clients see same state)
- **Audit trail** (events are signed and timestamped)

### Signer-First Operations

Critical operations require **signed ContextVM intents**:

```json
{
  "kind": 25910,
  "content": "{\"jsonrpc\":\"2.0\",\"id\":\"deploy-svc-123-env-456\",\"method\":\"service/deploy\",\"params\":{\"service_id\":\"svc-123\",\"environment_id\":\"env-456\",\"artifact_id\":\"art-789\"}}",
  "tags": [
    ["p", "<bahia-service-pubkey>"],
    ["method", "service/deploy"],
    ["service", "svc-123"],
    ["environment", "env-456"],
    ["artifact", "art-789"]
  ]
}
```

This ensures:
- **Non-repudiation** — actions are cryptographically signed
- **Auditability** — intents and observables are on relays
- **Authorization** — verified ContextVM pubkeys are checked against allowlists

## Control Planes

Bahia exposes three control-plane surfaces:

### 1. Nostr Relay Sidecar (Primary)

The **primary** control plane for:
- Real-time state updates
- ContextVM mutation intents
- Canonical observable subscriptions

### 2. MCP (Model Context Protocol)

For **AI agent** interactions:
- Tool discovery at `/mcp`
- Synchronous tool invocation
- Nostr correlation metadata for async follow-up

### 3. REST API

A **compatibility** surface under `/api/v1` for:
- Query and list operations
- A small set of remaining compatibility mutations
- Legacy client support

Most mutations (service, environment, deployment, artifact, adoption, direct runtime, and LLM operations) are signer-first only; the corresponding legacy REST mutations are not mounted.

## Authorization Model

### Pubkey-Based Authorization

Control-plane operations use **Nostr pubkey** authorization:

| Allowlist | Purpose |
|-----------|---------|
| `nostr.authorized_pubkeys` | General operator access |
| `adoption.allowed_pubkeys` | Runtime adoption operations |
| `direct_runtime_actions.allowed_pubkeys` | Direct deploy/restart/stop |
| `auth.bootstrap_owner_pubkeys` | Organization creation |

### Organization-Based Access

Within organizations (roles from `internal/domain/tenant.go`):
- **owner** — Full access, can delete org
- **admin** — Manage members and settings
- **deployer** — Create/modify resources and deploy
- **viewer** — Read-only access

## Encrypted Operations

Sensitive operations use **encrypted ContextVM events**: inner kind `25910` JSON-RPC messages wrapped with CEP-4/NIP-59 `1059` or `21059`. Legacy `5980`/`7980` encrypted request/result events are startup migration inputs only.

- Notification channel configurations
- Service secrets
- Payment history
- Deployment run logs

These events are:
- Encrypted to the Bahia service pubkey
- Routed through the relay sidecar/browser relay allowlist advertised by discovery
- Never published to non-allowlisted public relays

## Next Steps

- Learn about [Services](features/services.md) in detail
- Understand [Nostr Integration](nostr-integration.md)
- Explore [MCP Tools](mcp-tools.md) for agent integration
