# Bahia Backup Control Plane Implementation Plan

_Date: 2026-05-23_

## Purpose

Define, strictly in Bahia terms, what still must be implemented for Bahia to become the fleet backup control plane.

This document is intentionally **not** a storage-01 operations guide and **not** a migration runbook for current shell jobs. It is a product and architecture gap analysis for Bahia itself.

---

## Scope

This plan covers only Bahia-owned concerns:

- control-plane command surface
- durable state model
- backend capability model
- read models and operator UX
- executor model
- MCP and web interfaces
- policy and approval semantics
- observability and provenance

This plan does **not** define:

- current fleet backup host procedures
- rsync shell details
- ad hoc cron migration steps
- host-by-host data movement choreography

---

## Current Code Reality

Bahia already has a meaningful backup foundation.

### Implemented now

#### 1. Durable backup domain + database schema

Bahia already has durable records for:

- backup repositories
- backup policies
- backup recipes
- backup runs
- backup verifications
- backup restores
- backup retention runs

Primary code:

- `internal/domain/backup.go`
- `internal/db/migrations/000027_backup_control_plane.up.sql`
- `internal/db/migrations/000028_backup_restore_retention.up.sql`
- `internal/repository/pg_backup_controlplane.go`

#### 2. Nostr-native backup request handling

Bahia already accepts and processes these backup command families:

- backup run request
- backup restore request
- backup restore approval
- backup retention enforcement request

Primary code:

- `internal/controlplane/backup_handlers.go`
- `internal/controlplane/backup_restore_handlers.go`
- `internal/controlplane/backup_retention_handlers.go`
- `internal/controlplane/reactor.go`

#### 3. Durable coordinators and recovery loops

Bahia already has queue-driven coordinators with stale-run recovery for:

- backup runs
- backup restores
- backup retention runs

Primary code:

- `internal/service/backup_run_coordinator.go`
- `internal/service/backup_restore_coordinator.go`
- `internal/service/backup_retention_coordinator.go`

#### 4. Nostr read-model projection

Bahia already projects backup state into replaceable read models.

Primary code:

- `internal/adapters/nostr/projector.go`
- `internal/adapters/nostr/backup_kinds.go`
- `docs/control-planes.md`

#### 5. Real backend adapters

Bahia already has concrete backup backend adapters for:

- **Kopia**
  - snapshot creation
  - snapshot verification
  - restore
  - retention
- **Velero**
  - restore
  - retention
  - health

Primary code:

- `internal/adapters/backup/kopia_backend.go`
- `internal/adapters/backup/velero_backend.go`
- `internal/service/backup_backend_resolver.go`

---

## What Prevents Bahia From Being the Fleet Backup Control Plane Today

The missing work is no longer the basic backup data model. The gaps are in **operator surface**, **registry mutation flow**, **executor architecture**, and **backend scope**.

### 1. Backup registry mutation is incomplete

`docs/control-planes.md` reserves command kinds for:

- repository register
- policy apply
- recipe apply
- definition apply
- repository probe
- verification request

But the current reactor wiring only implements:

- run
- restore
- restore approval
- retention

That means operators cannot yet manage the backup registry through the same first-class Nostr command flow they use elsewhere.

#### Missing Bahia work

Implement handlers, validation, responders, and tests for:

- `BackupRepositoryRegisterRequest` / result
- `BackupPolicyApplyRequest` / result
- `BackupRecipeApplyRequest` / result
- `BackupDefinitionApplyRequest` / result
- `BackupRepositoryProbeRequest` / result
- `BackupVerificationRequest` / result

#### Required outcome

Everything needed to define and manage backup configuration must be commandable through Bahia itself, not only via direct database writes or internal service calls.

---

### 2. There is no first-class backup MCP surface

Current Bahia MCP tools do not expose backup operations.

This blocks agents and operator tooling from using Bahia as the authoritative backup control plane through the native Bahia tool layer.

#### Missing Bahia work

Add MCP tools for at least:

- apply backup repository
- apply backup policy
- apply backup recipe
- apply backup definition
- probe backup repository
- request backup run
- request backup verification
- request backup restore
- approve/reject backup restore
- request retention enforcement
- list backup repositories
- list backup policies
- list backup recipes
- list backup runs
- list backup restores
- list backup retention runs
- inspect one run/restore/repository/policy/recipe

#### Required outcome

Agents should be able to manage the backup control plane entirely through Bahia MCP without bypassing Bahia’s own command and result model.

---

### 3. There is no backup web UI

The backend has real backup capability, but there is no meaningful operator UI for it in `web/src`.

Without this, Bahia cannot credibly serve as the human-facing fleet backup console.

#### Missing Bahia work

Create operator UX for:

- backup repositories
- backup policies
- backup recipes
- backup definitions
- backup run history
- verification history
- restore requests
- restore approval queue
- retention runs
- repository health/probe status

#### Required read-model UX elements

Operators need to see:

- repository backend kind and health
- what each recipe targets
- what policy governs verification and retention
- whether a backup run is restore-eligible
- which restores are waiting for approval
- evidence from verification and retention runs
- clear terminal success/failure reasons

#### Required outcome

Backup operations must become visible, reviewable, and actionable from the Bahia UI without requiring log spelunking or direct DB inspection.

---

### 4. Backup definitions are not yet a complete operator abstraction

The event namespace reserves both **recipe** and **definition** concepts, but the current code reality is still centered on repositories, policies, and recipes.

For Bahia to act as the fleet backup control plane, it needs one operator-facing abstraction that represents:

- what is being protected
- where it is stored
- under what policy it runs
- what executor/backend is expected to fulfill it

#### Missing Bahia work

Define and implement a clear `BackupDefinition` model that composes:

- repository
- policy
- recipe
- scheduling metadata
- ownership / tenant / environment scoping
- approval policy
- restore target rules
- executor targeting
- labels / grouping

If `BackupDefinition` is meant to remain only a projection/coordination wrapper, that contract must be made explicit in domain code and UI language.

#### Required outcome

Operators should manage “backup definitions” as the canonical fleet backup object, not stitch together low-level records mentally.

---

### 5. Executor targeting is underdefined

Today the backup backend execution model is coordinator-driven, but Bahia does not yet expose a robust operator-visible concept of **where backup work runs**.

For a real fleet backup control plane, Bahia must explicitly model backup execution placement.

#### Missing Bahia work

Add first-class backup execution targeting concepts such as:

- executor worker identity
- executor selection policy
- backend capability requirements
- locality constraints
- environment/site constraints
- repository reachability constraints
- credential profile compatibility

This should follow Bahia’s broader worker/execution model, not create a one-off side channel.

#### Required outcome

A backup definition must be able to say, in Bahia-native terms, which workers or worker classes are allowed to execute it, and Bahia must be able to explain why a run was or was not placeable.

---

### 6. Backend scope is still too narrow for fleet-wide control-plane claims

Current backend reality:

- **Kopia** is the only implemented snapshot-creating backend in the first slice
- **Velero** supports restore/retention but not snapshot creation in this implementation
- Kopia first-slice recipes are intentionally constrained

This is a good start, but not yet broad enough for Bahia to claim “fleet backup control plane” without qualification.

#### Current constraints visible in code

- backup recipe validation currently restricts runnable recipes to `kopia`
- Kopia adapter currently requires path-oriented semantics and defers include/exclude complexity
- restore and retention semantics are backend-specific and not yet normalized into a broader operator contract

#### Missing Bahia work

At minimum:

- make backend capability visibility explicit in UI and MCP
- expose per-backend capability flags:
  - snapshot_create
  - snapshot_verify
  - restore
  - retention
  - probe
- decide whether phase 1 fleet support means:
  - “Bahia is the control plane for Kopia-backed filesystem/data protection first”
  - or broader multi-backend guarantees

#### Required outcome

Bahia must present an honest capability contract instead of implying every registered backend supports the full lifecycle.

---

### 7. Verification must become a first-class operational concept

Verification is already present in the domain model and completion rules, which is good. But it still needs stronger operator-facing treatment.

#### Missing Bahia work

- implement explicit verification request flow if retained in the event taxonomy
- show verification mode, evidence, and restore eligibility in UI and MCP
- make “restore eligibility” a first-class displayed state, not an inferred hidden rule
- surface policy failures clearly when a backup snapshot exists but is not verified

#### Required outcome

Operators should be able to answer, from Bahia alone:

- was a snapshot created?
- was it verified?
- by what method?
- is it restore-eligible?
- if not, why not?

---

### 8. Restore approval flow needs dedicated operator ergonomics

The backend logic for restore approval exists, but becoming a real fleet control plane requires better approval workflow support.

#### Missing Bahia work

- approval queue UI
- approval/rejection MCP tools
- policy-driven approval requirements on definitions
- structured approval reason capture
- event/read-model representation for pending approvals
- audit-friendly provenance view of who approved what and why

#### Required outcome

Restore is not just a backend function. It must be an auditable operator workflow.

---

### 9. Observability and provenance need a stronger backup-specific product layer

The backup coordinators already carry evidence and publish summaries, but Bahia still needs a proper operator-facing observability contract.

#### Missing Bahia work

Standardize and expose:

- repository probe status
- last successful run per definition
- last verification success per definition
- last restore request and outcome
- retention outcomes
- backend health failures
- queued/running/stale counts
- structured failure categories
- signed provenance/attestation views

#### Required outcome

Bahia should become the place where an operator asks:

- “what is protected?”
- “what is unhealthy?”
- “what has not verified recently?”
- “what can be restored right now?”

---

### 10. Scheduling belongs in Bahia if Bahia is the control plane

A fleet backup control plane cannot stop at manual run requests.

If Bahia is truly the backup control plane, it must own the canonical scheduling model for backup definitions even if underlying executors remain external or host-local.

#### Missing Bahia work

Add scheduling/state concepts for backup definitions:

- disabled / enabled
- schedule expression or cadence policy
- jitter / maintenance window controls
- next scheduled run
- last scheduled dispatch
- schedule pause reason
- missed run accounting

This can be Bahia-native scheduling or a clearly modeled delegated scheduler contract, but the control plane must own the intended schedule state.

#### Required outcome

Operators should define backup intent once in Bahia and see the canonical schedule state there.

---

## Required Bahia Capability Model

To be credible as the fleet backup control plane, Bahia should expose the following layered model.

### Layer 1: Registry

Authoritative objects:

- backup repository
- backup policy
- backup recipe
- backup definition

### Layer 2: Execution intent

Commandable operations:

- run backup
- verify backup
- restore backup
- approve/reject restore
- enforce retention
- probe repository

### Layer 3: Durable workflow state

Tracked state objects:

- backup run
- verification record
- restore run
- retention run
- repository probe result/history
- schedule/dispatch state

### Layer 4: Read models

Operator-facing state:

- definition state
- repository state
- run state
- restore eligibility state
- approval queue state
- retention state
- health/observation state

### Layer 5: Interfaces

Bahia-native access surfaces:

- Nostr commands/results
- MCP tools
- web UI

---

## Phased Bahia Work

## Phase 1 — Complete the missing control-plane command surface

Implement the missing request/result flows for:

- repository register
- policy apply
- recipe apply
- definition apply
- repository probe
- verification request

Deliverables:

- reactor handlers
- validation
- responders
- tests
- docs updates

### Exit criteria

Every backup registry object and operational request in the reserved namespace is actually executable through Bahia.

---

## Phase 2 — Add MCP parity

Implement backup MCP tools covering:

- registry CRUD/apply
- probe
- run
- verify
- restore
- approval
- retention
- list/query/read-model inspection

### Exit criteria

An agent can fully operate the backup control plane through Bahia MCP alone.

---

## Phase 3 — Add web operator UX

Implement a backup section in the Bahia UI with:

- repositories
- policies
- recipes
- definitions
- runs
- verification
- restore queue
- retention
- health

### Exit criteria

A human operator can understand current backup posture and perform the common workflows from the UI.

---

## Phase 4 — Make executor targeting explicit

Implement backup execution placement semantics tied to Bahia’s worker model.

Deliverables:

- executor targeting fields in definitions
- placement rules and validation
- read-model explainability for placement decisions
- capability compatibility checks

### Exit criteria

Bahia can explain where a backup definition may run and why.

---

## Phase 5 — Normalize capability contracts and backend truthfulness

Implement explicit backend capability reporting and surface it everywhere.

Deliverables:

- backend capability descriptors
- repository probe output standardization
- UI/MCP exposure of supported lifecycle operations
- honest operator messaging for partial backend support

### Exit criteria

Operators are never misled about what a backend can or cannot do.

---

## Phase 6 — Add Bahia-owned schedule state

Implement canonical schedule semantics for backup definitions.

Deliverables:

- schedule fields and validation
- dispatch state model
- next/last run tracking
- pause/disable controls
- missed-run observability

### Exit criteria

Backup intent and schedule state live authoritatively in Bahia.

---

## Recommended Non-Goals for the First Complete Slice

To keep the first truly usable Bahia backup control plane tractable, avoid expanding scope into these immediately:

- generic shell-script execution as the primary backup abstraction
- repo mirroring as “backup” in the same domain model
- per-backend exotic features beyond the common lifecycle
- deep include/exclude orchestration in Bahia for Kopia phase 1
- multi-tenant policy complexity beyond what the existing fleet needs

These can come later. The immediate goal is a trustworthy, explainable, commandable backup control plane.

---

## Acceptance Criteria: When Bahia Can Truthfully Claim This Role

Bahia can reasonably call itself the fleet backup control plane when all of the following are true:

1. Backup registry objects are fully manageable through Bahia-native commands.
2. Backup operations are fully accessible through Bahia MCP.
3. Backup operations have first-class web UX.
4. Definitions, not ad hoc backend details, are the operator-facing abstraction.
5. Restore approval is an auditable operator workflow.
6. Executor placement is explicit and explainable.
7. Restore eligibility is visible and policy-derived.
8. Backend capability limits are surfaced honestly.
9. Schedule state is owned by Bahia.
10. Operators can determine fleet backup posture from Bahia alone.

---

## Short Version

Bahia no longer lacks the core backup domain. It lacks the **product completion layer** required to make that domain operationally authoritative.

The work now is to finish Bahia as a backup control plane across four fronts:

- complete the missing backup command surface
- expose backup through MCP
- build the backup UI and read-model UX
- make execution targeting, schedule state, and capability truthfulness explicit

Once those are in place, Bahia can become the real fleet backup control plane rather than just a backend slice with promising primitives.
