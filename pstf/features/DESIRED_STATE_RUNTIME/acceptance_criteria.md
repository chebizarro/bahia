# Acceptance Criteria — DESIRED_STATE_RUNTIME

## Status: draft

## Terminal Semantics (applies to all criteria)

All desired-state operations in this feature must complete through explicit terminal signals. No criterion may be satisfied by timeout-based completion, polling-based reconciliation, or implicit success derived from elapsed time. This principle is inherited from the PSTF-wide terminal semantics established in `SOUL_FACTORY_PROVISIONING_TRACKING` and is non-negotiable for this feature.

---

## DSR-AC-001 — Typed desired-state domain model exists with canonical hash serialization

**Type:** contract  
**Priority:** must  
**Work items:** DSR-WI-01

**Assertion:** A typed desired-state model must exist in `internal/domain/` that carries environment-scoped and service-scoped desired-state types, schema version, canonical hash, and renderer-specific extension payloads. Hash computation must use deterministic JSON with sorted object keys, stable list ordering, explicit null/empty handling, schema version included, and volatile fields excluded.

**Preconditions:**
- `internal/domain/runtime_desired_state.go` exists with the typed structs.

**Expected result:**
- Service desired-state struct carries: schema version, environment/runtime target metadata, service/artifact IDs, stable service key, image reference, command/entrypoint/working dir, env, ports, volumes, labels, healthcheck, dependency policy, network policy, restart/pull policy, desired hash, and renderer extension data.
- Environment desired-state struct carries: all managed service specs, environment revision hash, and assembly metadata.
- Golden hash fixtures prove deterministic serialization across runs.
- Secret-backed env entries retain refs and redaction metadata; resolved plaintext is never persisted.

**Negative cases:**
- If hash inputs include volatile fields (timestamps, container IDs) or are order-dependent on map iteration, the criterion fails.
- If resolved secret plaintext is persisted in desired-state snapshots, the criterion fails.

---

## DSR-AC-002 — Desired-state and hash columns exist in deployment persistence

**Type:** data_integrity  
**Priority:** must  
**Work items:** DSR-WI-01

**Assertion:** Additive DB migrations must add desired-state snapshot (JSONB) and hash columns to deployment intents, deployment runs, environment service state, and runtime observations. Repository persistence must load and save these new fields.

**Preconditions:**
- Migration files exist under `internal/db/migrations/`.

**Expected result:**
- Deployment intent rows carry desired-state snapshot and hash.
- Deployment run rows carry runtime apply metadata.
- Environment service state rows carry latest desired runtime state and hash.
- Runtime observation rows carry normalized observed state and hash.
- Repository load/save round-trips preserve new fields.

**Negative cases:**
- If existing queries break due to schema changes, the criterion fails.
- If hash columns are not indexed or queryable for comparison, the criterion fails.

---

## DSR-AC-003 — RuntimeLifecycleService uses a shared internal deploy helper

**Type:** functional  
**Priority:** must  
**Work items:** DSR-WI-02

**Assertion:** `RuntimeLifecycleService` must converge deployment execution, direct runtime `action=deploy`, and rollback-to-artifact onto a single shared internal deploy helper. `restart` and `stop` must remain operational lifecycle actions, not desired-state mutations.

**Preconditions:**
- `internal/service/runtime_lifecycle.go` is refactored.

**Expected result:**
- Deployment requests and direct `action=deploy` both flow through the same helper.
- The helper builds desired state, acquires environment lock, renders, applies, observes, persists, and publishes Nostr events.
- `restart` and `stop` do not create or mutate desired-state snapshots.

**Negative cases:**
- If deployment requests and direct deploys take different code paths for desired-state assembly, the criterion fails.
- If `restart` or `stop` creates a desired-state snapshot, the criterion fails.

---

## DSR-AC-004 — Environment-scoped advisory locking serializes concurrent deploys

**Type:** reliability  
**Priority:** must  
**Work items:** DSR-WI-02

**Assertion:** Full-project Compose rendering and Docker Engine apply must be serialized per environment using a DB-backed advisory lock keyed by `environment_id`. The lock must not hold a DB transaction across CLI/API runtime calls.

**Preconditions:**
- `internal/service/runtime_apply_lock.go` exists and is injected from `internal/app/app.go`.

**Expected result:**
- Concurrent same-environment deploys serialize deterministically without sleep-based waiting.
- Short DB transactions are used for persistence only; the advisory lock spans build/render/apply/observe.
- Lock release uses `defer` for deterministic cleanup.
- Failed runs release the lock and persist failure state.

**Negative cases:**
- If concurrent same-environment deploys can produce interleaved or partial Compose projects, the criterion fails.
- If the advisory lock is held across a DB transaction that spans runtime I/O, the criterion fails.

---

## DSR-AC-005 — Desired-state builder produces canonical service specs with Bahia labels

**Type:** functional  
**Priority:** must  
**Work items:** DSR-WI-03

**Assertion:** The desired-state builder must map service + environment + artifact + runtime config + secrets into a canonical desired service spec. It must inject stable Bahia labels (`bahia.managed`, service/environment/artifact/intent/run IDs, desired hash), split literal env from secret refs, and carry renderer-specific details in typed extension payloads.

**Preconditions:**
- `internal/service/runtime_desired_state_builder.go` exists.

**Expected result:**
- Builder output contains all required fields from the domain contract.
- Bahia labels are injected with correct service/environment/artifact/intent/run metadata.
- Literal env values are separated from secret-backed entries.
- Renderer extensions (Compose, Docker) are typed, not unstructured maps.
- `Service.RuntimeTargetName()` is used as the stable service key source, with normalization and collision handling.

**Negative cases:**
- If secret plaintext appears in the persisted desired-state spec, the criterion fails.
- If renderer extensions are untyped `map[string]interface{}` at call sites, the criterion fails.

---

## DSR-AC-006 — Legacy environment-plan hydration includes sibling services without stored desired state

**Type:** data_integrity  
**Priority:** must  
**Work items:** DSR-WI-03

**Assertion:** Environment-plan assembly must load active environment-service-state rows, replace the target service with the freshly built spec, hydrate sibling specs from stored desired state when present, reconstruct missing sibling specs from current persisted registry/runtime/artifact state, drop deleted/tombstoned services, sort deterministically, and compute the environment revision hash.

**Preconditions:**
- At least one environment has existing managed services without stored desired-state snapshots.

**Expected result:**
- First full-project render includes all legacy sibling services.
- Hydrated legacy sibling specs are persisted opportunistically for future renders.
- Missing hydration does not omit services from generated output.
- Environment revision hash is stable across identical inputs.

**Negative cases:**
- If a managed sibling service without a stored desired snapshot is omitted from the environment plan, the criterion fails.
- If the environment plan includes deleted or tombstoned services, the criterion fails.

---

## DSR-AC-007 — Desired-state adapter capability exists without breaking unsupported runtimes

**Type:** contract  
**Priority:** must  
**Work items:** DSR-WI-04

**Assertion:** A `DesiredStateApplier`-style interface must exist in the runtime adapter layer. Compose, Docker, and Podman adapters must expose the capability where supported. Unsupported runtime types (Kubernetes) must fail explicitly rather than silently falling back.

**Preconditions:**
- Runtime adapter interface exists under `internal/adapters/runtime/`.
- Factory wiring exposes the capability cleanly.

**Expected result:**
- Compose and Docker implement the desired-state apply capability.
- Podman reuses the Docker-compatible path where feasible.
- Kubernetes stays on the existing legacy deploy path.
- Apply result reports: renderer, revision hash, target resource IDs/names, observation hints, and non-fatal warnings.
- Unsupported types produce explicit errors, not silent fallback.

**Negative cases:**
- If a Kubernetes desired-state apply silently succeeds or falls through to the legacy path without an explicit error, the criterion fails.

---

## DSR-AC-008 — Compose phase 1 synthesizes a full Bahia-owned project

**Type:** functional  
**Priority:** must  
**Work items:** DSR-WI-05

**Assertion:** Compose rendering must replace per-service image substitution with authoritative full-project synthesis. The renderer must produce a canonical `docker-compose.yml`, generated env files under `.bahia/`, and render metadata. Staged output must be validated with `docker compose config -q` before atomic swap. Apply must use `docker compose --project-directory <compose_dir> up -d --remove-orphans`.

**Preconditions:**
- A Bahia-owned `compose_dir` is configured for the target environment.
- Environment plan assembly has produced a complete plan including sibling services.

**Expected result:**
- `<compose_dir>/docker-compose.yml` is deterministically rendered from the environment plan.
- `<compose_dir>/.bahia/env/<service-key>.env` files are generated per service.
- `<compose_dir>/.bahia/render-state.json` captures render metadata.
- Explicit Compose project `name:` is set (not relying on directory-basename).
- Staged output passes `docker compose config -q` validation.
- Apply uses full-project `up -d --remove-orphans`, not service-scoped `up <service>`.
- `<SERVICE>_IMAGE` env substitution, service-scoped `up`, and unconditional `--force-recreate` are removed from the desired-state path.

**Negative cases:**
- If a non-Bahia-owned `compose_dir` is written to without an explicit ownership gate, the criterion fails.
- If service-scoped `up <service>` or `--force-recreate` is used in the desired-state path, the criterion fails.
- If staged output is swapped into the live location without passing `docker compose config -q`, the criterion fails.

---

## DSR-AC-009 — Docker Engine desired-state apply with hash-based no-op detection

**Type:** functional  
**Priority:** must  
**Work items:** DSR-WI-06

**Assertion:** Docker Engine desired-state apply must map desired service specs to Engine container config, use stable Bahia-managed container naming and labels, compare `bahia.desired_hash` for no-op detection, and manage the full container lifecycle (pull, ensure volumes/networks, stop/remove, create, start).

**Preconditions:**
- Docker Engine adapter is wired with the desired-state capability.
- Endpoint resolution uses existing resolver/factory rules.

**Expected result:**
- Existing Bahia-managed container is inspected for service/environment.
- Hash match with acceptable health produces a no-op apply.
- Hash mismatch triggers pull (per policy), volume/network ensure, stop/remove, create, attach, start.
- Podman reuses the Docker-compatible path where current adapter behavior allows.
- Apply result includes observation hints and non-fatal warnings.

**Negative cases:**
- If a hash-matching healthy container is unnecessarily recreated, the criterion fails.
- If required networks/volumes are not ensured before container creation, the criterion fails.

---

## DSR-AC-010 — Observation normalization produces comparable hashes for Compose and Docker

**Type:** data_integrity  
**Priority:** must  
**Work items:** DSR-WI-07

**Assertion:** Runtime observations for Compose and Docker must be normalized into a comparable subset that includes: image ref/digest, command, entrypoint, working directory, non-secret env values, secret env key presence, ports, volumes, restart policy, network attachments, and Bahia labels. Container IDs, timestamps, ephemeral IPs, Compose-generated non-semantic labels, and secret plaintext must be excluded.

**Preconditions:**
- Compose and Docker observation paths are producing normalized output.

**Expected result:**
- Normalized observation hash is deterministic for the same runtime state.
- Compose and Docker observations of semantically identical services produce identical normalized hashes.
- Secret plaintext is excluded from normalized observations.

**Negative cases:**
- If container IDs or timestamps are included in normalized observations, the criterion fails.
- If Compose and Docker observations of identical service state produce different hashes, the criterion fails.

---

## DSR-AC-011 — Drift detection uses desired hash vs normalized observed hash

**Type:** functional  
**Priority:** must  
**Work items:** DSR-WI-07

**Assertion:** For desired-state-managed Compose/Docker services, drift status must be: `in_sync` when normalized observed hash equals desired hash and runtime health is acceptable, `drifted` when hashes differ, and `unknown` when observation cannot be produced.

**Preconditions:**
- Desired-state and observation normalization are both active.

**Expected result:**
- `in_sync` is reported only when both hashes match and health is acceptable.
- `drifted` is reported when hashes differ, regardless of health state.
- `unknown` is reported when observation fails (best-effort observation is attempted).
- Failure paths persist failure state and publish explicit drift status.

**Negative cases:**
- If drift is determined by polling-derived snapshots rather than hash comparison, the criterion fails.
- If observation failure results in silent `in_sync` rather than `unknown`, the criterion fails.

---

## DSR-AC-012 — Nostr status events carry step progression without new kinds

**Type:** contract  
**Priority:** must  
**Work items:** DSR-WI-08

**Assertion:** Existing status kinds must carry additive step progression metadata: `building_desired_state`, `locking_environment`, `rendering`, `applying`, `observing`, and `projecting`. No new kinds or d-tag coordinates may be added.

**Preconditions:**
- Status publication path in `RuntimeLifecycleService` emits step progression.

**Expected result:**
- Status events within existing `6961` kind carry step metadata tags.
- Step progression is emitted from the lifecycle path at each meaningful phase.
- Older readers that ignore the new tags continue to function.

**Negative cases:**
- If new Nostr event kinds are introduced for step progression, the criterion fails.
- If status events omit step progression and only report terminal state, the criterion fails.

---

## DSR-AC-013 — Result and read-model payloads are enriched additively

**Type:** contract  
**Priority:** must  
**Work items:** DSR-WI-08

**Assertion:** Result (`7961`/`7962`) and read-model (`31961`/`31967`/`31968`) payloads must carry additive metadata: renderer, desired-state hash, environment revision, runtime target, apply metadata summary, and observation ID. Catalog decoders must tolerate richer content. Replaceable semantics must be preserved.

**Preconditions:**
- Projector and catalog changes exist in `internal/adapters/nostr/`.

**Expected result:**
- Result events include renderer, desired hash, environment revision, and observation ID where available.
- Read-model events expose sanitized desired/apply metadata.
- Tolerant decoding in catalog does not reject enriched events.
- Replaceable semantics and validation are preserved.
- Tests inject events directly and prove backward-compatible decoding.

**Negative cases:**
- If enriched events cause older catalog decoders to reject or misparse payloads, the criterion fails.
- If replaceable semantics (d-tag coordinates) are changed, the criterion fails.

---

## DSR-AC-014 — Compose ownership gate prevents non-Bahia-owned directory writes

**Type:** data_integrity  
**Priority:** must  
**Work items:** DSR-WI-05, DSR-WI-09

**Assertion:** A Compose ownership gate must reject or hold back environments whose `compose_dir` is not explicitly Bahia-owned before any authoritative file generation occurs. This must be validated before phase 1 rendering begins.

**Preconditions:**
- Environment Compose target configuration includes ownership metadata.

**Expected result:**
- Non-Bahia-owned `compose_dir` targets are explicitly rejected with a clear error.
- No files are generated or swapped into a non-owned directory.
- Ownership validation occurs before any staging or rendering work.

**Negative cases:**
- If authoritative Compose files are written to a directory without ownership validation, the criterion fails.

---

## DSR-AC-015 — Failure paths release locks, persist failed runs, and publish terminal failure

**Type:** reliability  
**Priority:** must  
**Work items:** DSR-WI-02, DSR-WI-05, DSR-WI-06

**Assertion:** When desired-state apply fails at any stage, the system must: release advisory locks via `defer`, persist the failed run with failure metadata, attempt best-effort observation, keep the requested desired snapshot available for drift comparison, and publish a terminal failure result through the existing `7961` result kind.

**Preconditions:**
- A desired-state deploy is triggered and fails during rendering, apply, or observation.

**Expected result:**
- Advisory lock is released regardless of failure point.
- Failed deployment run is persisted with failure reason and metadata.
- Best-effort observation is attempted even after apply failure.
- Desired-state snapshot remains available for future drift comparison.
- Terminal failure is published as a correlated `7961` result event.

**Negative cases:**
- If advisory locks are leaked on failure, the criterion fails.
- If failed runs are not persisted, the criterion fails.
- If failure does not produce a terminal Nostr result event, the criterion fails.

---

## DSR-AC-016 — Rollout validates mixed-version safety and existing environment hydration

**Type:** functional  
**Priority:** must  
**Work items:** DSR-WI-09

**Assertion:** Staging must validate that existing environments hydrate sibling desired state correctly on the first full-project Compose deploy, that direct Docker deploy and rollback-to-older-artifact use the shared desired-state path, and that non-Bahia-owned `compose_dir` targets are correctly rejected.

**Preconditions:**
- A staging environment with existing managed services is available.

**Expected result:**
- First full-project Compose deploy includes all existing managed siblings.
- Direct Docker deploy, rollback-to-artifact, and direct `action=deploy` all flow through the shared desired-state path.
- Non-Bahia-owned Compose targets are rejected before any file generation.
- Docs describe Bahia-owned Compose directories and additive Nostr payload changes.

**Negative cases:**
- If rollback or direct deploy bypasses the desired-state path, the criterion fails.
- If docs are not updated to reflect the new operational model, the criterion fails.

---

## Criteria-to-Work-Item Mapping

| Criterion | Work Items | Category |
|-----------|-----------|----------|
| DSR-AC-001 | DSR-WI-01 | Domain model |
| DSR-AC-002 | DSR-WI-01 | Persistence |
| DSR-AC-003 | DSR-WI-02 | Lifecycle refactor |
| DSR-AC-004 | DSR-WI-02 | Locking |
| DSR-AC-005 | DSR-WI-03 | Builder |
| DSR-AC-006 | DSR-WI-03 | Legacy hydration |
| DSR-AC-007 | DSR-WI-04 | Adapter capability |
| DSR-AC-008 | DSR-WI-05 | Compose rendering |
| DSR-AC-009 | DSR-WI-06 | Docker Engine apply |
| DSR-AC-010 | DSR-WI-07 | Observation normalization |
| DSR-AC-011 | DSR-WI-07 | Drift detection |
| DSR-AC-012 | DSR-WI-08 | Nostr status |
| DSR-AC-013 | DSR-WI-08 | Nostr result/read-model |
| DSR-AC-014 | DSR-WI-05, DSR-WI-09 | Safety gate |
| DSR-AC-015 | DSR-WI-02, DSR-WI-05, DSR-WI-06 | Failure handling |
| DSR-AC-016 | DSR-WI-09 | Rollout verification |
