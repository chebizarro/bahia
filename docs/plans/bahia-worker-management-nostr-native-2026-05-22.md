# Bahia Worker Management Plan (Nostr-First)

_Date: 2026-05-22_

## Purpose

Rewrite Bahia's worker-management roadmap in native Bahia terms:

- **commands are Nostr events**
- **state is read-model driven**
- **web UI is an adapter over Nostr-backed read models**
- **workers are a shared execution substrate**, not an ML-only concept

This plan covers:

1. shared-worker semantic cleanup
2. operator actions: cordon, drain, maintenance, labels, pinning, rollout
3. read-model design for trustworthy UX
4. concrete codebase touchpoints
5. phased implementation order

---

## Current Code Reality

### Generic worker substrate already exists

- `internal/domain/worker.go`
- `internal/repository/pg_worker.go`
- `internal/api/handlers/workers.go`
- `web/src/routes/workers/+page.svelte`
- `web/src/routes/workers/[pubkey]/+page.svelte`

Bahia already has a generic worker entity, but the operator model is underpowered.

### Placement logic already exists in two layers

#### Generic worker policy selection
- `internal/service/worker_policy.go`
- environment selector inputs:
  - `loom_worker_selector`
  - `runtime_config.worker_policy`

#### ML/inference-specific placement
- `internal/service/ml_placement.go`
- `internal/service/ml_inference_provisioning_coordinator.go`
- `web/src/routes/ml/+page.svelte`

### Bahia already has a command + result + read-model pattern

Examples:
- service commands: `internal/controlplane/service_command_publisher.go`
- ML commands: `internal/controlplane/ml_command_publisher.go`
- package commands: `internal/controlplane/package_commands.go`
- reactor kinds and handlers: `internal/controlplane/reactor.go`, `internal/controlplane/ml_handlers.go`
- MCP adapter layer: `internal/mcp/server.go`, `internal/mcp/ml_tools.go`, `internal/mcp/agent_async_tools.go`

This worker-management work should follow the same pattern.

---

## Product Direction

### Core model

- **Worker** = any schedulable execution node
- **Capability** = declared feature or compatibility signal
- **Workload** = anything Bahia places on a worker
- **Inference endpoint** = one workload subtype
- **CI/CD run** = another workload subtype
- **Placement policy** = constraints and preferences for worker selection

### Terminology changes

#### Keep
- `Workers`

#### Rename
- `ML Fabric` → `Inference`
- `ML Task` filter → `Task Type` or `Supported Workloads`

#### Reframe
Workers are the shared compute pool for:
- CI/CD
- inference
- deployment helpers
- batch/recipe work
- future compute tasks

---

## Architecture Principles

### 1. No worker-management REST as primary architecture

Worker mutations should not be modeled as CRUD-first REST features.

Instead:
- publish a command event
- process it in the controlplane reactor/coordinator
- emit result/status events
- update read models
- let the web UI render those read models

HTTP endpoints may still exist as a **local adapter**, but they should not be the source of truth.

### 2. Operator trust requires explainable read models

Operators need to see:
- whether a worker is accepting new work
- why it is or is not eligible
- what is currently assigned to it
- what blocks a drain
- what a pin/selector will do before they commit

So worker management is not just command kinds; it also requires new read models.

### 3. Cordon/drain semantics must be global

A cordoned or draining worker must be treated consistently across:
- generic worker selection (`worker_policy.go`)
- ML placement (`ml_placement.go`)
- future LLM / CI / recipe placement paths

---

## Proposed Command Taxonomy

These should be introduced as new controlplane command kinds and handled through the same signer-first Nostr request flow used elsewhere.

## Worker lifecycle commands

- `worker.cordon.request`
- `worker.uncordon.request`
- `worker.drain.request`
- `worker.undrain.request`
- `worker.maintenance.enter.request`
- `worker.maintenance.exit.request`
- `worker.disable.request`
- `worker.enable.request`

## Worker metadata commands

- `worker.labels.update.request`
- `worker.capabilities.override.request` _(optional, probably later)_
- `worker.runtime-target.update.request` _(optional, probably later)_

## Placement-related commands

These should attach to workload/environment policy rather than mutate the worker itself.

- `worker-policy.apply.request`
- `worker-policy.clear.request`
- `workload.pin.request`
- `workload.unpin.request`
- `workload.rollout.request`

## Read-only/preview commands

Useful for operator UX:

- `worker.eligibility.preview.request`
- `worker.drain.preview.request`
- `worker.rollout.preview.request`

These may emit either:
- terminal result events only, or
- refreshable read models keyed by `d`/idempotency/correlation tags

---

## Proposed Read Models

These are the important missing pieces.

### 1. Worker State Read Model

Represents the operator-facing scheduling state of each worker.

Suggested shape:

- worker pubkey
- name
- liveness status: `online | stale | offline`
- scheduling state: `active | cordoned | draining | maintenance | disabled`
- scheduling note / reason
- labels
- workload kinds / capabilities
- queue depth
- max concurrency
- runtime target summary
- resources / accelerators
- timestamps

### 2. Worker Assignment State Read Model

Represents current work attached to a worker.

Suggested shape:

- worker pubkey
- active assignments
- assignment type (`service`, `inference`, `ci`, `recipe`, etc.)
- workload identifier
- assignment status
- pinned / movable flag
- started at / updated at

This read model is essential for drain UX.

### 3. Worker Eligibility Preview Read Model

For a proposed deployment/workload/policy change, show:

- eligible workers
- rejected workers
- rejection reasons
- ranking scores
- selected winner

This should be backed by:
- `WorkerPolicyService.RankWorkers(...)`
- ML placement scoring/rejection reasons from `ml_placement.go`

### 4. Worker Drain Status Read Model

For workers in drain mode:

- drain started at
- reason
- remaining assignments
- pinned blockers
- last migration attempt / reason
- whether worker is safe to enter maintenance or disable

### 5. Worker Rollout Preview Read Model

For canary/stable pool changes:

- selected worker set by label/selector
- current workload distribution
- delta if rollout is applied

---

## Domain Model Changes

## Worker scheduling state

### Existing liveness state
Current `WorkerStatus` in `internal/domain/worker.go` reflects advertisement freshness only:
- `online`
- `stale`
- `offline`

### Add separate scheduling state

Add a new concept:

```go
type WorkerSchedulingState string

const (
    WorkerSchedulingActive      WorkerSchedulingState = "active"
    WorkerSchedulingCordoned    WorkerSchedulingState = "cordoned"
    WorkerSchedulingDraining    WorkerSchedulingState = "draining"
    WorkerSchedulingMaintenance WorkerSchedulingState = "maintenance"
    WorkerSchedulingDisabled    WorkerSchedulingState = "disabled"
)
```

Suggested fields on `domain.Worker`:

- `SchedulingState WorkerSchedulingState`
- `SchedulingNote string`
- `Labels map[string]string`

This keeps liveness distinct from scheduler intent.

## Generic capabilities

Current worker model includes:
- `MLCapabilities WorkerMLCapabilities`

That is useful, but too narrow as the long-term operator model.

### Add generic capability envelope

Suggested addition:

```go
type WorkerCapabilities struct {
    WorkloadKinds   []string `json:"workload_kinds,omitempty"`
    Runtimes        []string `json:"runtimes,omitempty"`
    ArtifactFormats []string `json:"artifact_formats,omitempty"`
    Accelerators    []string `json:"accelerators,omitempty"`
    Toolchains      []string `json:"toolchains,omitempty"`
    Features        []string `json:"features,omitempty"`
}
```

Recommended rollout:
- keep `MLCapabilities` for now
- add `Capabilities` as a generic layer
- progressively migrate UI language toward generic capabilities

---

## Scheduling Semantics

## Cordon

Meaning:
- worker remains visible and healthy
- worker receives no new work
- existing work keeps running

Enforcement:
- generic selector path excludes cordoned workers from new placements
- ML placement excludes cordoned workers from new placements

## Drain

Meaning:
- no new work
- existing assignments should be retired, migrated, or allowed to complete
- operator can monitor progress and blockers

Enforcement:
- treated as ineligible for new assignments
- read model tracks outstanding work

## Maintenance

Meaning:
- worker is intentionally unavailable for scheduling
- typically used after drain is complete or for hard intervention windows

## Disabled

Meaning:
- administrative off switch
- stronger than cordon; worker should not be considered schedulable regardless of liveness

---

## Placement and Pinning Model

## Worker labels

Add explicit worker labels to support:
- role pools (`role=ci`, `role=inference`)
- canary/stable pools (`track=canary`, `track=stable`)
- region/zone placement
- cost tier / hardware class

This works naturally with `loom_worker_selector` and future previews.

## Hard pinning

Pinning should be modeled on the **workload or environment policy**, not the worker.

Add to policy structures something like:

- `PinnedWorker string`

Likely in:
- generic worker policy structures
- ML deployment payload metadata / placement policy

Behavior:
- if pinned worker exists, only that worker is eligible
- preview must show incompatibility reasons if the pin conflicts with requirements

## Rollout

Rollout should be selector/label driven, not worker-name driven.

Examples:
- move inference endpoint from `track=canary` pool to `track=stable`
- deploy only to workers labeled `region=us-west, role=inference`

The operator mental model should be:
- edit placement policy
- preview eligible worker set
- publish rollout request
- observe new assignment state via read models

---

## UX Design (Bahia-Native)

## Workers list page

Files:
- `web/src/routes/workers/+page.svelte`

### Goals
- show workers as shared execution substrate
- show both liveness and scheduling state
- expose operator actions through command publication

### Changes

#### Header/subtitle
- title stays `Workers`
- subtitle: `Shared execution pool for CI/CD, inference, and scheduled compute workloads.`

#### New filters
- scheduling state
- task type / workload kind
- label key/value
- accelerator
- runtime
- online only

#### Row status chips
Two distinct badges per row:
- liveness: online / stale / offline
- scheduling: active / cordoned / draining / maintenance / disabled

#### Per-row action menu
Actions:
- View details
- Cordon
- Uncordon
- Drain
- Cancel drain
- Enter maintenance
- Exit maintenance
- Edit labels
- Copy selector
- Preview eligibility _(future)_

These should publish Bahia-native commands, not mutate state directly.

## Worker detail page

Files:
- `web/src/routes/workers/[pubkey]/+page.svelte`

### Important note
Current detail page still appears to reference stale fields like:
- `worker.capabilities`
- `worker.price_per_sec`
- `worker.last_seen`
- `worker.metadata`

It should be refit to the actual current worker shape from `internal/domain/worker.go` and current handler responses.

### Desired sections

#### 1. Summary header
- name
- pubkey
- liveness badge
- scheduling badge
- quick actions

#### 2. Scheduling
- scheduling state
- note / reason
- queue depth
- max concurrency
- accepting new work? yes/no

#### 3. Capabilities
- workload kinds
- runtimes
- toolchains
- artifact formats
- accelerators

#### 4. Resources
- CPU
- memory
- disk
- GPUs / accelerator memory

#### 5. Labels & placement
- labels
- example selectors
- edit labels action

#### 6. Pricing
- pricing tiers

#### 7. Active assignments
- what is currently running here
- movable vs pinned
- blocking drain indicators

## Inference page

Files:
- `web/src/routes/ml/+page.svelte`

### Relabeling
- `ML Fabric` → `Inference`
- emphasize that deployments target the shared worker pool

### Placement UX improvements
- optional `Pin to worker`
- worker eligibility preview before submit
- label/selector-aware deploy targeting
- estimated eligible worker count

---

## Concrete Command UX Flows

## Cordon flow

### UI action
Operator clicks `Cordon worker`

### Published command
- `worker.cordon.request`

### Content
- worker pubkey
- optional reason
- operator/request metadata
- idempotency key

### Result path
- result event confirms command accepted/rejected
- worker state read model updates scheduling state to `cordoned`

### Operator-visible effect
- worker still online
- worker no longer eligible for new placement

## Drain flow

### UI action
Operator clicks `Drain worker`

### Preview first
Publish preview request or compute locally from read models:
- how many active assignments?
- how many movable?
- how many pinned blockers?

### Published command
- `worker.drain.request`

### Content
- worker pubkey
- reason
- drain mode (`graceful`, possibly later `force`)

### Result path
- drain result event
- worker state becomes `draining`
- drain status read model begins tracking progress

### Operator-visible effect
Show:
- remaining assignments
- pinned blockers
- last migration/retirement attempt

## Pin workload flow

### UI action
Operator pins an inference endpoint or environment to a worker

### Published command
Either:
- `workload.pin.request`, or
- `worker-policy.apply.request` carrying `pinned_worker`

### Preview
Before submit, show:
- selected worker compatibility
- conflicts with runtime/accelerator/toolchain requirements

### Result path
- updated placement policy read model
- future placements honor pin

## Rollout flow

### UI action
Operator changes placement from canary pool to stable pool

### Published command
- `workload.rollout.request`
or
- `worker-policy.apply.request`

### Content
- target workload / environment
- selector change or rollout metadata

### Result path
- rollout result event
- assignment read models show workload migration / reassignment

---

## Concrete Codebase Checklist

## Phase 1 — semantic cleanup and UI relabeling

### Frontend
- `web/src/routes/ml/+page.svelte`
  - rename page from `ML Fabric` to `Inference`
  - reword copy around shared worker pool
- `web/src/routes/workers/+page.svelte`
  - add shared-substrate subtitle
  - rename `ML Task` filter to `Task Type` / `Supported Workloads`
- `web/src/routes/workers/[pubkey]/+page.svelte`
  - refit to actual worker data shape
  - stop relying on stale pseudo-fields

## Phase 2 — worker state model enrichment

### Backend
- `internal/domain/worker.go`
  - add `SchedulingState`
  - add `SchedulingNote`
  - add `Labels`
  - optionally add generic `Capabilities`
- `internal/repository/pg_worker.go`
  - persist new fields
- `internal/db/migrations/`
  - add columns / JSONB fields for scheduling state, note, labels

### Read models
- add/update worker state projection to include scheduling state and labels

## Phase 3 — new worker management commands

### Controlplane
Add new kinds and handlers in:
- `internal/controlplane/reactor.go`
- new command publisher file, likely:
  - `internal/controlplane/worker_command_publisher.go`
- new handler file, likely:
  - `internal/controlplane/worker_handlers.go`

### Pattern reference
Use these as implementation models:
- `internal/controlplane/service_command_publisher.go`
- `internal/controlplane/ml_command_publisher.go`
- `internal/controlplane/package_commands.go`

### Commands to implement first
- cordon
- uncordon
- drain
- undrain
- maintenance enter/exit
- labels update

## Phase 4 — scheduling enforcement

### Generic worker placement
- `internal/service/worker_policy.go`
  - exclude workers whose scheduling state is not schedulable
  - preserve ranked explanations for UI/debug

### Inference placement
- `internal/service/ml_placement.go`
  - reject non-active workers for new placements
  - surface rejection reason in candidate preview/read model

### Later
Apply same semantics to:
- LLM placement paths
- future CI / recipe orchestration paths

## Phase 5 — MCP / async tool adapter support

### MCP layer
Add signer-first worker management tools in:
- `internal/mcp/server.go`
- likely new file:
  - `internal/mcp/worker_tools.go`
- maybe `internal/mcp/agent_async_tools.go`

These should mirror existing Bahia async command tools and return:
- request event id
- request kind
- result kind
- read model kinds
- correlation tags

## Phase 6 — worker read models for operator UX

Implement projections/read access for:
- worker state
- worker assignment state
- drain status
- eligibility preview

These will likely touch:
- reactor/projection plumbing
- repository/read-model access layers
- UI store bootstrapping

## Phase 7 — placement policy and pinning

### Backend
Extend placement policy structures to support:
- `pinned_worker`
- explicit label-driven rollout pools

Likely touchpoints:
- `internal/service/worker_policy.go`
- ML placement request structures in `internal/service/ml_placement.go`
- environment/runtime config parsing

### Frontend
- `web/src/routes/environments/+page.svelte`
- `web/src/routes/environments/[id]/+page.svelte`
- `web/src/routes/ml/+page.svelte`

Add:
- placement mode editor
- pinned worker selection
- eligibility preview before publish

---

## Recommended Delivery Order

### Milestone 1 — clarify the model
- relabel `ML Fabric` → `Inference`
- reframe `Workers` as shared pool
- fix worker detail page field drift

### Milestone 2 — make scheduling state real
- add `SchedulingState`
- add labels
- expose in read models/UI

### Milestone 3 — add core operator controls
- cordon / uncordon
- drain / undrain
- maintenance enter / exit
- labels update

### Milestone 4 — make operator actions trustworthy
- assignment read model
- drain status read model
- eligibility preview

### Milestone 5 — add precise placement control
- pinning
- rollout selector changes
- canary/stable worker pools

---

## Acceptance Criteria

## Cordon
- operator can publish a cordon request for a worker
- worker state read model updates to `cordoned`
- cordoned workers receive no new placements
- existing work remains running
- UI clearly distinguishes `online + cordoned` from offline

## Drain
- operator can publish a drain request
- worker becomes ineligible for new work
- active assignments remain visible
- drain progress is inspectable
- pinned blockers are visible

## Labels
- operator can update worker labels through command publication
- labels appear in worker state read model
- selectors and previews reflect label changes

## Pinning
- operator can pin a workload/environment to a worker
- preview explains whether the pin is valid
- placements honor the pin

## Rollout
- operator can move workload placement between label-selected pools
- preview shows before/after eligible worker sets
- assignment read model reflects transition

---

## Bottom Line

Bahia does **not** need a parallel REST-centric worker-management architecture.

It already has the right substrate:
- signed command publication
- result correlation
- read-model projections
- UI adapters

The missing work is to extend that native pattern to workers as first-class operator-managed schedulable resources.

That means the next implementation steps should be:

1. enrich worker state with scheduling intent and labels
2. add worker-management command kinds
3. project those actions into operator-grade read models
4. update placement services to respect scheduling state
5. redesign the web UI around Nostr-backed worker operations and previews
