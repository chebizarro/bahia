# Bahia Platform Maturity: Unified Roadmap

## Goal
Mature Bahia from a pragmatic bootstrap platform into a production-hardened system by addressing 10 identified architectural shortcuts: Compose-as-source-of-truth, single-project assumptions, control-plane/runtime state separation, CLI-coupled runtime adapters, weak observation/enforcement, shallow environment targeting, pragmatic secrets handling, rough service adoption, operational knowledge in runbooks, and mixed-era control-plane interfaces.

## Background

### Prior Art: Desired-State Runtime Architecture Plan
The existing `docs/plans/desired-state-runtime-architecture-2026-05-26.md` plan already covers significant ground:
- **Compose rendering**: Full-project synthesis via `ComposeRenderer` and `ComposeDesiredStateApplier` (`internal/adapters/runtime/compose_renderer.go:61-129`, `internal/adapters/runtime/compose_desired_state.go:91-156`)
- **Deployment path unification**: Migration from legacy `Runtime.Deploy` to `DesiredStateApplier.ApplyDesiredState` (`internal/adapters/runtime/desired_state_capability.go:14-35`)
- **Desired state domain**: `DesiredServiceSpec` with deterministic hashing (`internal/domain/runtime_desired_state.go:144-228`)
- **Observation normalization**: Normalized observed state with hash comparison (`internal/domain/runtime_desired_state.go:391-458`)
- **Environment locking**: DB-backed advisory locking for environment-level serialization

This plan builds on that foundation to complete the remaining maturity areas.

### Runtime Adapter Architecture
- **Two deployment paths exist**: Legacy `Runtime.Deploy` (per-service, imperative) at `internal/adapters/runtime/docker.go:294-404` and newer `DesiredStateApplier` (full-project, declarative) at `internal/adapters/runtime/desired_state_capability.go:14-35`
- **CLI coupling**: Compose adapter shells out to `docker compose` CLI (`internal/adapters/runtime/compose.go:52-63`, `70-83`, `143-158`); Docker uses Engine API directly
- **Runtime resolution**: Config overlay path via `RuntimeResolver` (`internal/adapters/runtime/resolver.go:83-205`) supports `type`, `docker_host`, `endpoint_ref`, `compose_dir`, `kube_context`, `kube_namespace`

### Control-Plane State Model
- **Intent-based deployment**: `DeploymentIntent` created via control plane, projects desired state into `EnvironmentServiceState` (`internal/service/registry.go:540-579`)
- **Observation-only reconciliation**: `Reconciler` calls only `Observe`, does not invoke `DesiredStateApplier` or remediate drift (`internal/reconcile/reconciler.go:94-119`)
- **Drift detection**: Currently artifact-digest based, not full desired-state-hash based (`internal/service/registry.go:1002-1017`)

### Secrets Handling
- **Adapter interface**: `secrets.Resolver` decrypts via `Encryptor` (NIP-44/AES-256) (`internal/adapters/secrets/resolver.go:22-41`)
- **Scoping**: Service-wide or environment-specific; effective merge via `ListEffective` (`internal/repository/pg_secret.go:95-109`)
- **Desired-state separation**: `DesiredStateBuilder` splits literal env from `SecretRefs` (`internal/service/runtime_desired_state_builder.go:77-121`)
- **Plaintext surfaces**: API ingress, runtime lifecycle injection, Docker env blocks, Compose env files
- **Missing**: Rotation history, decrypt audit log, runtime-native mounted secret abstraction

### Service Adoption/Import
- **Weak identity matching**: `sameAdoptedTarget` only matches on `TargetName` + `HostAlias` (`internal/service/adoption.go:943-949`)
- **Missing org ownership**: Adoption-created services/environments don't set `OrgID` (`internal/service/adoption.go:656-661`, `714-721`)
- **Compose takeover policy**: Scan adds warning for Compose-origin workloads (`internal/service/adoption.go:325-332`)

### Control-Plane Interfaces
- **Four surfaces**: REST, Nostr reactor, MCP, CLI
- **Canonical event kinds**: `internal/controlplane/reactor.go:26-158` — request `596x/597x`, status `696x`, result `796x`, read-models `3196x`
- **Overlapping deploy semantics**: REST intents vs direct runtime routes, Nostr deploy requests, CLI intent-style vs direct actions

### User Decisions (from Phase 1)
- **Scope**: Unified roadmap covering all 10 areas with explicit ordering and dependencies
- **Deployment path**: Migrate fully to desired-state — deprecate legacy `Runtime.Deploy` path
- **Reconciliation**: Configurable per environment — some auto-remediate, others require approval
- **Runtime abstraction**: Direct API eventually — long-term move to Docker Engine API / containerd

## Approach

Bahia should treat the 10 maturity items as one staged runtime/control-plane refactor, not as 10 disconnected features. The correct anchor is the existing desired-state runtime work: finish that slice first, then extend it with a first-class `DeploymentUnit` model for multi-unit runtime boundaries, explicit desired-vs-observed state ownership, runtime executor/API seams, configurable reconciliation, typed environment targeting, versioned secrets, hardened adoption identity, machine-readable operational contracts, and interface convergence on canonical Nostr request/status/result/read-model flows.

### Dependency-Ordered Workstreams

| ID | Roadmap area | Primary dependency |
|---|---|---|
| R1 | Finish desired-state runtime baseline (areas 1, 3, 4, 5) | existing desired-state plan |
| R2 | Introduce `DeploymentUnit` and multi-unit runtime boundaries (area 2) | R1 |
| R3 | Harden control-plane/runtime state separation (area 3) | R1, R2 |
| R4 | Introduce runtime control client seam and migrate executor ownership (area 4) | R1 |
| R5 | Add configurable reconciliation and observation enforcement (area 5) | R3, R4 |
| R6 | Deepen environment targeting and placement (area 6) | R2, R3 |
| R7 | Mature secrets storage, audit, and materialization (area 7) | R1 |
| R8 | Harden adoption/import identity and org ownership (area 8) | R2, R6, R7 |
| R9 | Codify operational contracts in docs, schemas, PSTF, metrics (area 9) | R5, R8, R10 |
| R10 | Unify control-plane write interfaces on canonical Nostr flows (area 10) | R3, R4, R7, R8 |

## Work Items

### Item 1 — Land and Validate Desired-State Baseline

**Goal:** Confirm current landing state of desired-state runtime files and land any missing persistence/hash normalization.

**Done when:**
- Audit of `runtime_desired_state.go`, `compose_renderer.go`, `desired_state_capability.go`, `runtime_environment_plan.go` completed
- `5961` execution path from reactor to `RuntimeLifecycleService` traced and validated end-to-end
- Desired-state snapshots and hashes persisted on intent/run/state/observation records
- Golden-hash test fixtures lock deterministic behavior
- Migration adds all required columns additively
- Normative docs updated with any field additions

**Key files:** `internal/domain/runtime_desired_state.go`, `internal/adapters/runtime/`, `internal/db/migrations/`, `internal/repository/pg_*.go`, `internal/controlplane/reactor.go`

**Dependencies:** None (prerequisite: verify 5961 call chain is wired before proceeding)

**Size:** M

---

### Item 2 — Introduce DeploymentUnit Domain Types and Persistence

**Goal:** Add first-class `DeploymentUnit` model with implicit default-unit resolution.

**Done when:**
- `DeploymentUnit` domain type defined (exact struct shape determined during implementation)
- Required fields: environment reference, runtime type, reconcile mode, ownership mode
- `deployment_units` table created
- `deployment_unit_id` foreign keys added to state/intent/run/observation tables
- Implicit default-unit synthesized in memory when no explicit unit exists
- **Transition trigger defined**: Unit persisted on explicit operator API call (`POST /environments/{id}/units`) or on first multi-unit config change — not on every write/apply/observe
- Normative docs updated with unit model

**Key files:** `internal/domain/deployment_unit.go` (new), `internal/db/migrations/`, `internal/repository/`, `docs/deployment.md`

**Dependencies:** Item 1

**Size:** L

---

### Item 3 — Make Desired-State Planning Unit-Aware

**Goal:** Update builder/plan code to compute unit revision hashes and environment aggregate hashes.

**Done when:**
- `DesiredServiceSpec` carries unit-capable identity fields
- Environment plan grouped by deployment unit
- Cross-unit Compose dependencies rejected
- Unit revision hash and aggregate environment revision hash computed
- Normative docs updated with planning changes

**Key files:** `internal/service/runtime_environment_plan.go`, `internal/service/runtime_desired_state_builder.go`, `internal/domain/runtime_desired_state.go`

**Dependencies:** Item 2

**Size:** M

---

### Item 4 — Switch Service State/Drift to Desired-vs-Observed Hashes

**Goal:** Make desired-state-managed workloads use hash comparison instead of artifact-digest drift.

**Done when:**
- `registry.go` drift calculation uses desired hash vs normalized observed hash
- Drift statuses: `in_sync` (hashes match + health OK), `drifted` (hashes differ), `unknown` (observation unavailable)
- Normalized observation persistence includes hash column
- Tests prove old artifact-digest path still works for non-desired-state workloads
- Projections enriched with desired/observed hash metadata
- Normative docs updated with drift semantics

**Key files:** `internal/service/registry.go`, `internal/repository/pg_observation.go`, `internal/adapters/nostr/projector.go`, `docs/event-spec.md`

**Dependencies:** Items 1, 3

**Size:** M

---

### Item 5 — Deepen Environment Targeting and Placement

**Goal:** Add typed targeting data to environments and units, preserving backward compatibility.

**Done when:**
- Environment-level targeting owns: default unit key, failure-domain labels, secret scope mode, default reconcile mode
- Unit-level targeting owns: runtime type, endpoint ref / compose dir / namespace, network profile, ownership mode, unit-local reconcile override
- Service/environment placement owns: `deployment_unit_id`
- **Backward compatibility rule**: moved fields read from typed fields first, then `runtime_config`; writes project back into typed fields, not only raw JSON
- API DTOs add additive fields for deployment_units, targeting, reconcile mode
- Projections add unit tags on `31961`, `31963`, `31967`, `31968`
- Normative docs updated with targeting changes

**Key files:** `internal/domain/models.go`, `internal/domain/deployment_unit.go`, `internal/api/dto/requests.go`, `internal/adapters/nostr/projector.go`, `docs/deployment.md`

**Dependencies:** Item 2

**Size:** M

---

### Item 6 — Add Reconcile Policy Fields and Observation Scheduler (Observe-Only)

**Goal:** Land policy persistence and scheduler wiring without auto-remediation first.

**Done when:**
- `ReconcileMode` enum (`observe_only`, `auto_apply`, `approval_required`, `disabled`) defined
- Environment and unit tables store reconcile policy
- Scheduler selects due services/units and runs observation
- No remediation triggered (observe-only mode)
- Normative docs updated with reconcile policy

**Key files:** `internal/reconcile/reconciler.go`, `internal/domain/deployment_unit.go`, `internal/db/migrations/`, `docs/deployment.md`

**Dependencies:** Item 4

---

### Item 7 — Introduce Secret Versioning and Access Audit

**Goal:** Model secret rotation and access explicitly with audit trail.

**Done when:**
- `SecretVersion` domain type defined
- `secret_versions` and `secret_access_audit` tables created
- Existing secrets migrated to version 1
- Resolver returns audit manifest alongside resolved values
- Audit row written after resolve/apply attempt
- No plaintext in DB, logs, read models, or status/result payloads

**Key files:** `internal/domain/secret_version.go` (new), `internal/db/migrations/`, `internal/repository/pg_secret.go`, `internal/adapters/secrets/resolver.go`

**Dependencies:** Item 2

**Size:** L

---

### Item 8 — Add Adoption Identity Persistence and OrgID Migration

**Goal:** Tighten adoption identity matching and safely migrate org ownership.

**Done when:**
- `adopted_runtime_identity` table persists unique fingerprints (container ID, digest, Compose coordinates, endpoint ref)
- Scan surfaces stronger identity inputs from Docker discovery
- Import requires org_id unless environment has single resolved org
- Ambiguous OrgID rows marked for operator repair
- Signer-first mutate flows fail closed for unresolved ownership

**Key files:** `internal/repository/pg_adopted_runtime_identity.go` (new), `internal/service/adoption.go`, `internal/adapters/runtime/docker_discovery.go`, `internal/db/migrations/`

**Dependencies:** Items 3, 6, 7

**Size:** L

---

### Item 9 — Introduce Runtime Control Client Seam

**Goal:** Move execution ownership into a narrow runtime control seam.

**Done when:**
- Runtime control interface defined (exact method set determined during adapter extraction)
- **Constraint**: narrow seam, no business logic in adapters, execution-mode metadata returned
- Docker/Podman implementations use direct Engine API
- Compose implementation uses CLI-backed executor (compatibility)
- Apply result reports execution mode (`cli` or `engine_api`)
- No silent fallback; compatibility mode explicit in config
- Normative docs updated with execution mode semantics

**Key files:** `internal/adapters/runtime/control_client.go` (new), `internal/adapters/runtime/compose_executor.go` (new), `internal/adapters/runtime/docker.go`, `internal/adapters/runtime/factory.go`, `docs/deployment.md`

**Dependencies:** Items 1, 3

**Size:** L

---

### Item 10 — Migrate Docker/Podman to API-Owned Control Clients

**Goal:** Docker/Podman desired-state execution through explicit API ownership.

**Done when:**
- Docker/Podman appliers delegate to control client
- Engine API used for all create/start/stop/inspect operations
- Execution mode surfaced in apply result
- Business logic removed from `docker.go` adapter

**Key files:** `internal/adapters/runtime/docker.go`, `internal/adapters/runtime/docker_renderer.go`, `internal/adapters/runtime/control_client.go`

**Dependencies:** Item 9

**Size:** M

---

### Item 11 — Convert Compose Execution to Unit-Owned Renderer/Executor Flow

**Goal:** Compose business logic lives above CLI executor; CLI becomes compatibility transport.

**Done when:**
- Full-project rendering uses unit-owned Compose dir
- Compose ownership gate enforced per unit
- CLI executor handles validation/apply under control client interface
- Service-image substitution and service-scoped `up` removed from desired-state path

**Key files:** `internal/adapters/runtime/compose.go`, `internal/adapters/runtime/compose_renderer.go`, `internal/adapters/runtime/compose_executor.go`

**Dependencies:** Items 9, 10

**Size:** L

---

### Item 12 — Enable Policy-Driven Reconciliation

**Goal:** Turn on `approval_required` and `auto_apply` modes through shared desired-state helper.

**Done when:**
- Reconciler uses same lock implementation as deploy/apply
- **Lock contention rule**: user deploys preempt scheduled auto-remediation; auto-remediation queues behind active user operations and applies backoff on contention
- `auto_apply` mode invokes shared desired-state deploy helper
- `approval_required` creates remediation-needed state and waits for `5968`
- Failure behavior: keep desired state, store failure metadata, apply backoff
- Internal auto-remediation doesn't synthesize fake external events
- Normative docs updated with reconciliation behavior

**Key files:** `internal/reconcile/reconciler.go`, `internal/service/runtime_lifecycle.go`, `internal/service/runtime_apply_lock.go`, `docs/deployment.md`

**Dependencies:** Items 4, 6, 9

**Size:** L

---

### Item 13 — Unify Control-Plane Write Interfaces

**Goal:** REST, MCP, CLI use canonical Nostr flows or normalized command handler.

**Done when:**
- `CommandReceipt` DTO defined with request_event_id, status/result kinds, idempotency_key
- **Publish-and-wait contract**: timeout configurable (default 30s), partial-failure returns receipt with error status, relay-unreachable returns immediate error with retry hint
- Idempotency-key support in reactor, publishers, and persistence
- Long-running REST routes return `202` + receipt (optional publish-and-wait compatibility)
- MCP returns uniform receipts for runtime/adoption/remediation
- CLI generates deterministic idempotency keys, defaults to receipt output
- No distinct business logic paths between surfaces
- **Pre-work gate**: Search `web/src/lib/api` and `web/src/routes` before changing REST defaults; split into "add receipts additively" and "switch defaults" if clients depend on sync responses
- Normative docs updated with receipt/idempotency semantics

**Key files:** `internal/controlplane/reactor.go`, `internal/controlplane/service_command_publisher.go`, `internal/api/dto/`, `internal/api/handlers/`, `internal/mcp/server.go`, `cmd/cli/main.go`, `pkg/client/`, `docs/control-planes.md`, `docs/api.md`

**Dependencies:** Items 4, 8, 10

**Size:** XL (consider splitting into "add receipts" M + "switch defaults" M with soak period)

---

### Item 14 — Machine-Readable Schemas and PSTF Bundles

**Goal:** Add machine-readable schemas and PSTF acceptance evidence packages.

**Done when:**
- Machine-readable schemas added under `schemas/` for desired-state, unit, receipt, reconcile payloads
- PSTF bundles created for each maturity slice with acceptance criteria, rollout gates, metrics, rollback criteria

**Key files:** `schemas/` (new), `pstf/features/`

**Dependencies:** Items 2, 6, 7, 8, 9, 12, 13

**Size:** M

**Note:** Normative doc updates are included in each work item's "Done when" criteria rather than bundled here.

## Risks and Migration

### Mixed-Version Risk
All DB and Nostr changes must be additive first: nullable new columns, new tables, additive tags/content fields, tolerant decoders. Do not flip defaults until all runtime-serving nodes are on the new binary.

### Deployment Unit Migration Risk
Current runtime config is partly config-file-derived. Use lazy default-unit materialization; only require explicit units when operators want more than one boundary.

### Compose Ownership Risk
Keep the explicit ownership gate from the desired-state plan and extend it to units. No multi-unit Compose work should weaken this.

### OrgID Migration Risk
Ambiguous ownership must not be guessed. Backfill only safe cases; block signer-first mutate flows for unresolved rows until operator repair.

### Secret History Migration Risk
Migration should create version 1 rows from existing data, preserve old identity rows, switch resolver reads after backfill.

### REST Compatibility Risk
Add receipt fields first, then optional publish-and-wait compatibility, then switch default response mode.

## Open Questions (Order-Changing)

- **`5961` call chain — resolved 2026-05-30**: `internal/controlplane/reactor.go` handles kind `5961` in `handleDeployRequest`, validates authorization/policy, builds a desired-state snapshot through `RuntimeLifecycleService.BuildDesiredStateSnapshot`, persists it on `DeploymentIntent`, and invokes `RuntimeLifecycleService.DeployWithStatus` for approved intents. App wiring passes the service via `internal/app/app.go` with `controlplane.WithRuntimeLifecycleService(runtimeLifecycleSvc)`. Test coverage: `TestHandleDeployRequestInvokesRuntimeLifecycleAndPersistsDesiredState`.
- **Non-Bahia-owned Compose dirs in production**: If any exist, Item 11 is blocked on operator migration not in this plan, and Item 8 should move earlier to inventory them.
- **Web UI sync REST dependence**: Search `web/src/lib/api` and `web/src/routes` before Item 13. If clients depend on sync responses, Item 13 must split into additive + switch-defaults phases with soak period.

## Resolved Design Decisions

- **DeploymentUnit transition trigger**: Operator-initiated (`POST /environments/{id}/units`) or on first multi-unit config change, not every write/apply/observe
- **Lock contention priority**: User deploys preempt scheduled auto-remediation; auto-remediation queues and backs off
- **CommandReceipt timeout**: Default 30s, partial-failure returns receipt with error status, relay-unreachable returns immediate error with retry hint
- **Backward compatibility for targeting**: Read from typed fields first, then `runtime_config`; writes project back into typed fields

## References

- `docs/plans/desired-state-runtime-architecture-2026-05-26.md` — prior art baseline
- `internal/adapters/runtime/` — runtime adapter layer
- `internal/reconcile/reconciler.go` — observation-only reconciliation
- `internal/adapters/secrets/` — secrets adapter
- `internal/service/adoption.go` — adoption service
- `internal/controlplane/reactor.go` — Nostr reactor and event kinds
- `docs/control-planes.md` — canonical control-plane contracts
- `docs/deployment.md` — runtime targeting and endpoint governance
- `docs/adoption-production-rollout.md` — rollout safety defaults
- Docker Engine API: https://docs.docker.com/reference/api/engine/
