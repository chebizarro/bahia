# Core Concepts

Bahia is a desired-state control plane for services, their environments, and the artifacts that run in them. This page names the entities, the lifecycle they move through, and the model that ties every client to the same signed truth.

## The deployment model

1. An operator **declares** desired state by signing an intent.
2. The daemon **validates, authorizes, and applies** it, and publishes the canonical record.
3. Runtimes and workers **execute** deployment runs.
4. Runtime observations **report** what is actually running.
5. The service state record **compares** desired and observed and reports drift; reconciliation corrects it according to the environment's reconcile mode.

## Entities

### Organization

The tenancy boundary. Services, environments, deployment intents, secrets, and notification channels belong to exactly one organization; membership records on the relay give operators roles in it. Confidential records of an organization are encrypted under its own content key (OCK), which the daemon wraps to each member. See [Organizations](features/organizations.md).

### Service

A deployable application: a name, an artifact repository (the image repository CI pushes to), structured repository metadata (`repo_source`, `repo_coordinate`, `clone_url`, `ci_provider`, `ci_workflow`), a default branch, a runtime type, and optional managed runtime configuration. Secrets are owned by a service and optionally scoped to one environment. See [Services](features/services.md).

### Environment

A deployment target. An environment carries one or more **deployment units** — the concrete place a service instance runs (`runtime_type`, `endpoint_ref`, `compose_dir`, `namespace`, ownership mode) — a deploy strategy (`replace`, `blue_green`, `canary`), a reconcile mode (`observe_only`, `auto_apply`, `approval_required`, `disabled`), a `protected` flag that forces approval, and a secret scope mode. See [Environments](features/environments.md).

### Build and artifact

A **build** is a CI run the daemon learned about from signed Hive-CI evidence (`5401` workflow run, `5402` result) or a `build/request` intent. A successful build yields an **artifact**: an immutable, digest-pinned image with provenance (build, commit, signatures, SBOM, scan status). Artifacts are what deployments reference; names and tags are never trusted on their own. See [Builds](features/builds.md) and [Artifacts](features/artifacts.md).

### Deployment intent and run

A **deployment intent** asks for an artifact to run in an environment (optionally in one deployment unit) and carries the reviewed desired-state hash. Its status moves through `pending` → `approved` or `rejected` → `deploying` → `deployed` or `failed`, and may become `superseded` (a newer intent for the same target) or `rolled_back`. Each approved intent produces one or more **deployment runs** (`queued`, `running`, `succeeded`, `failed`, `cancelled`, `timeout`) with logs. See [Deployments](features/deployments.md).

### Runtime observation and drift

A **runtime observation** is what the runtime adapter saw: the running image digest, container status, and labels. The **service state** record for a service in an environment holds the desired artifact, the observed artifact, and a `drift_status` of `unknown`, `in_sync`, `drifted`, `deploying`, or `remediation_needed`. Manual restarts, out-of-band deployments, and crashes all surface as drift. See [Environment States](features/environment-states.md).

### Policy

A fleet-level rule set evaluated against an artifact before it may be deployed into an environment — for example `require_sbom` or a signature requirement — with `enforcement: warn` or `block`. See [Policies](features/policies.md).

### Worker

A Loom worker is an execution node that advertises itself on the relays and runs jobs (deployments, builds, inference). Operators cordon, drain, label, and clean up workers; the scheduler ranks eligible workers per job. See [Workers](features/workers.md).

## The event model

Every entity above exists as a service-signed Nostr record, and every change starts as an operator-signed event:

| Role | Kind | Who signs |
|------|------|-----------|
| **Intent** — a desired-state document for one entity coordinate | `30900` with `t=bahia-intent` | operator |
| **Intent status** — accepted, rejected, or conflict, addressed to the requester | `30315` | service |
| **Canonical record** — current state of one entity, one per coordinate | `30900` with `schema=bahia.cp-state.v1` | service |
| **Audit fact** — an immutable event about an entity | `4903` | service |
| **Confidential request** — secret reveal, run logs, assistant | `25910` in a `1059` gift wrap | operator |

Properties that follow from this:

- **One truth, many clients.** The web app, CLI, MCP tools, and the operator assistant publish the same intents and read the same records.
- **Idempotent writes.** Every intent carries a UUIDv7 `intent_id`; a retry with the same ID is replayed, not re-applied.
- **Optimistic concurrency.** Updates carry the record's `updated_at`; a stale revision is a conflict.
- **Offline-tolerant reads.** Clients keep a local event store and render it first; relay catch-up is explicit.
- **Non-repudiation.** Intents and records are signed; relay delivery (`OK`) is never mistaken for completion.

Details, including coordinates, topics, and encryption, are in [Nostr Integration](nostr-integration.md).

## Authorization

Authorization is by verified pubkey.

**Fleet operators** are listed in `nostr.authorized_pubkeys`. They act outside any organization: policies, workers, DNS, ML, tool decisions, security scans, relay policy, fleet-scope rekeys, and MCP over HTTP.

**Organization roles** gate everything that belongs to an organization:

| Role | Grants |
|------|--------|
| `viewer` | read services, environments, deployments, logs, policies |
| `deployer` | viewer + create and approve deployments |
| `admin` | deployer + write services, environments, secrets, policies, LLM routes; manage backups; read secrets |
| `owner` | admin + manage members and settings |

Two scoped lists add further gates: `adoption.allowed_pubkeys` for runtime adoption and `auth.bootstrap_owner_pubkeys` / `nostr.bootstrap_owners` for creating an organization before it has members.

## Surfaces

| Surface | Use |
|---------|-----|
| **Web app** | Store-first dashboard over the relays; signs with NIP-07 or NIP-46 |
| **CLI** (`bahia`) | Scripts and operators; local key or NIP-46 bunker — [CLI Reference](cli-reference.md) |
| **MCP** | Agents and the operator assistant — [MCP Tools](mcp-tools.md) |
| **Relays** | Any Nostr client that can sign and subscribe — [Nostr Integration](nostr-integration.md) |
| **HTTP** | Health, metrics, run logs, blob proxy, a few reads — [HTTP surface](nostr-integration.md#http-surface) |
