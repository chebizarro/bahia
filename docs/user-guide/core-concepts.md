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
# Example service
name: "payment-api"
repository: "https://github.com/company/payment-api"
description: "Handles payment processing"
tags:
  team: "payments"
  criticality: "high"
```

Key attributes:
- **name**: Human-readable identifier
- **repository**: Source code location
- **tags**: Metadata for filtering and organization

### Environment

An **Environment** is a deployment target — staging, production, edge, etc.

```yaml
# Example environment
name: "production"
slug: "prod"
deployment_target:
  type: "kubernetes"
  cluster: "prod-us-east"
```

Environments can require:
- **Approval policies** (manual or automated)
- **Runtime targets** (Kubernetes, Docker, Compose)
- **Notification channels**

### Build

A **Build** represents a CI workflow execution that produces deployable output.

```yaml
# Build metadata from CI
workflow_id: "ci-123"
commit_sha: "abc123def"
branch: "main"
status: "completed"
```

Bahia integrates with CI systems through:
- **Hive-CI Bridge** for Hive-CI workflows
- **Webhook receivers** for other CI systems
- **Manual registration** via API/CLI

### Artifact

An **Artifact** is an immutable container image with metadata.

```yaml
# Example artifact
image: "registry.example.com/payment-api:v2.1.0"
digest: "sha256:abc123..."
build_id: "build-456"
metadata:
  git_commit: "abc123"
  build_timestamp: "2024-01-15T10:30:00Z"
```

Artifacts are immutable — once registered, their digest never changes.

### Deployment Intent

A **Deployment Intent** is a request to deploy an artifact to an environment.

```yaml
# Deployment intent
service_id: "svc-123"
environment_id: "env-456"
artifact_id: "art-789"
requested_by: "npub1..."
status: "pending_approval"
```

Intents go through a lifecycle:
1. **Created** → Intent submitted
2. **Pending Approval** → Waiting for policy/manual approval
3. **Approved** → Ready to execute
4. **Executing** → Run in progress
5. **Completed** / **Failed** → Terminal state

### Deployment Run

A **Deployment Run** is a concrete execution of a deployment intent.

```yaml
# Deployment run
intent_id: "intent-123"
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
observed_artifact: "art-789"
container_status: "running"
observed_at: "2024-01-15T10:40:00Z"
```

Observations enable drift detection by comparing:
- **Desired artifact** (from latest successful deployment)
- **Observed artifact** (from runtime inspection)

### Drift

**Drift** occurs when observed state doesn't match desired state.

Causes of drift:
- Manual container restarts
- Out-of-band deployments
- Container crashes and restarts
- Configuration changes

Bahia can:
- **Alert** on drift via notifications
- **Auto-remediate** drift (when configured)
- **Track** historical drift events

## Nostr Event Model

Bahia is **Nostr-native** — it uses Nostr events as the primary control plane.

### Event Categories

| Category | Kind Range | Purpose |
|----------|------------|---------|
| **Requests** | 5961-5989 | Signed operator commands |
| **Status** | 6961-6984 | Progress updates |
| **Results** | 7961-7979 | Terminal outcomes |
| **Read Models** | 31961-31999 | Current state projections |

### Read Models

**Read models** are Nostr replaceable events that reflect current state.

```json
{
  "kind": 31961,
  "content": {
    "service_id": "svc-123",
    "environment_id": "env-456",
    "desired_artifact": "art-789",
    "observed_artifact": "art-789",
    "status": "healthy"
  },
  "tags": [
    ["d", "svc-123:env-456"],
    ["service", "svc-123"],
    ["environment", "env-456"]
  ]
}
```

Benefits of read models:
- **Real-time updates** via subscription
- **Offline resilience** (cached locally)
- **Multi-client sync** (all clients see same state)
- **Audit trail** (events are signed and timestamped)

### Signer-First Operations

Critical operations require **signed Nostr events**:

```bash
# Signer-first deployment via CLI
bahia deploy --service payment-api --environment prod --artifact v2.1.0
# Signs a 5961 DeployRequest event
```

This ensures:
- **Non-repudiation** — actions are cryptographically signed
- **Auditability** — all operations are on the relay
- **Authorization** — pubkeys are checked against allowlists

## Control Planes

Bahia exposes three control-plane surfaces:

### 1. Nostr Relay Sidecar (Primary)

The **primary** control plane for:
- Real-time state updates
- Signed operator commands
- Read model subscriptions

### 2. MCP (Model Context Protocol)

For **AI agent** interactions:
- Tool discovery at `/mcp`
- Synchronous tool invocation
- Nostr correlation metadata for async follow-up

### 3. REST API

A **compatibility** surface for:
- CRUD operations on registry entities
- Query and list operations
- Legacy client support

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

Within organizations:
- **Owner** — Full access, can delete org
- **Admin** — Manage members and settings
- **Editor** — Create/modify resources
- **Viewer** — Read-only access

## Encrypted Operations

Sensitive operations use **encrypted Nostr events** (kind 5980/7980):

- Notification channel configurations
- Service secrets
- Payment history
- Deployment run logs

These events are:
- NIP-44 encrypted to the service pubkey
- Sent to separate encrypted-request relays
- Never published to public relays

## Next Steps

- Learn about [Services](features/services.md) in detail
- Understand [Nostr Integration](nostr-integration.md)
- Explore [MCP Tools](mcp-tools.md) for agent integration
