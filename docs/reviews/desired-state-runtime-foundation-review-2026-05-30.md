# Review: Desired-State Runtime Foundation (bahia-eov1)

**Date:** 2026-05-30
**Scope:** Tasks 1–4 of the Desired-State Runtime epic
**Status:** ✅ Approved — all acceptance criteria met, no blocking issues

---

## Context / Scope

This review covers the foundational desired-state runtime implementation across four tasks:

| Task | Issue | Title |
|------|-------|-------|
| 1 | bahia-wcwi | Land and Validate Desired-State Baseline |
| 2 | bahia-swkz | Introduce DeploymentUnit Domain Types and Persistence |
| 3 | bahia-pesd | Make Desired-State Planning Unit-Aware |
| 4 | bahia-jrkp | Switch Service State/Drift to Desired-vs-Observed Hashes |

The parent epic (`DESIRED_STATE_RUNTIME`) defines the broader vision: moving Bahia from monolithic Compose operations toward Nostr-native structured desired state that can render to Compose and Docker Engine targets.

### Key Files Reviewed

- `internal/domain/runtime_desired_state.go` (688 lines)
- `internal/domain/deployment_unit.go` (266 lines)
- `internal/service/runtime_desired_state_builder.go` (312 lines)
- `internal/service/runtime_environment_plan.go` (375 lines)
- `internal/service/registry.go` (1134 lines — drift detection path)
- `docs/deployment.md` (350 lines)
- Database migrations: `000037`, `000038`, `000039`
- PSTF features: `bahia-swkz`, `bahia-pesd`, `bahia-jrkp`, `DESIRED_STATE_RUNTIME`

---

## Findings

### 1. Task 1: Desired-State Baseline (bahia-wcwi) ✅

**What it delivers:** Canonical `DesiredServiceSpec` domain type with deterministic SHA-256 hashing, `NormalizedObservation` for observed runtime state, secret redaction via `DesiredSecretRef`, `DesiredStateBuilder` for constructing specs from service/artifact/config/secrets, and JSONB persistence columns on intents, runs, state, and observations.

**Acceptance criteria assessment:**

| Criterion | Status | Evidence |
|-----------|--------|----------|
| Typed desired-state domain model | ✅ Met | `runtime_desired_state.go`: `DesiredServiceSpec`, `DesiredEnvironmentPlan`, `NormalizedObservation` |
| Canonical hash serialization | ✅ Met | `hashInput` struct with deterministic JSON; nil→empty normalization; sorted slices |
| Golden hash fixtures | ✅ Met | `runtime_desired_state_golden_test.go`: 4 golden hashes locked across desired/observation/plan |
| Secret redaction safety | ✅ Met | `ContainsPlaintextSecret()` guard; `RedactedPlaceholder()` convention; builder strips secrets from env |
| JSONB persistence columns | ✅ Met | Migration `000037`: `desired_state`/`desired_hash` on intents, `apply_metadata` on runs, `desired_runtime_state`/`desired_hash` on state, `normalized_state`/`normalized_hash` on observations |
| Builder constructs specs | ✅ Met | `DesiredStateBuilder.Build()` with Bahia label injection, unit identity, secret separation |
| ValidateSpec safety checks | ✅ Met | Checks plaintext leaks, required labels, ID consistency, hash presence |

**Code quality observations:**
- Hash computation correctly excludes extensions and the `DesiredHash` field itself — verified by dedicated tests.
- The `hashInput` struct approach (separate from the main type) is a clean pattern that makes the hash contract explicit.
- Schema versioning (`DesiredStateSchemaVersion = "2"`) with documented bump-on-change protocol is good practice.
- The `canonicalDesiredLabels` function correctly strips `bahia.desired_hash` from hash inputs to avoid circular dependency.

### 2. Task 2: Deployment Units (bahia-swkz) ✅

**What it delivers:** `DeploymentUnit` domain type as a runtime ownership boundary inside an environment, implicit default-unit resolution, validation, and database schema.

**Acceptance criteria assessment:**

| Criterion | Status | Evidence |
|-----------|--------|----------|
| AC1: DeploymentUnit domain type | ✅ Met | `deployment_unit.go`: environment ref, runtime type, reconcile mode, ownership mode, endpoint ref, compose dir, namespace, network profile |
| AC2: Database migration | ✅ Met | Migration `000038`: `deployment_units` table + nullable `deployment_unit_id` FK on intents, runs, observations, state. Migration `000039`: endpoint/compose/namespace/network columns |
| AC3: Implicit default resolution | ✅ Met | `NewImplicitDefaultDeploymentUnit()` synthesizes in-memory unit from environment config without persisting |
| AC4: Normative documentation | ✅ Met | `docs/deployment.md` "Deployment Units and Targeting" section covers the model, transition triggers, and reconcile policies |

**Code quality observations:**
- Clean separation of concerns: `NormalizeEnvironmentTargeting()` and `NormalizeDeploymentUnitTargeting()` handle backward-compatible config extraction from `RuntimeConfig` maps.
- `ReconcileMode` and `OwnershipMode` are validated exhaustively with clear error messages referencing allowed values.
- The `Implicit: true` flag on synthesized units correctly prevents accidental persistence.
- Helper functions (`stringFromRuntimeConfig`, `stringMapFromRuntimeConfig`, `firstNonEmpty`, `copyRuntimeConfig`) are well-factored and nil-safe.
- `ValidateDeploymentUnit` is thorough: checks nil, nil UUID, empty key, runtime type, reconcile mode, and ownership mode.

### 3. Task 3: Unit-Aware Planning (bahia-pesd) ✅

**What it delivers:** Deployment-unit identity on `DesiredServiceSpec`, `DesiredDeploymentUnitPlan` grouping, per-unit revision hashes, environment revision hash aggregation, cross-unit Compose dependency rejection, and `EnvironmentPlanAssembler`.

**Acceptance criteria assessment:**

| Criterion | Status | Evidence |
|-----------|--------|----------|
| AC1: Unit identity on specs + hashing | ✅ Met | `DeploymentUnitID`, `DeploymentUnitKey`, `UnitRuntimeType` fields; included in `hashInput` |
| AC2: Unit plan grouping | ✅ Met | `GroupByDeploymentUnit()` builds `UnitPlans` from flat services; `DesiredDeploymentUnitPlan` type |
| AC3: Deterministic revision hashes | ✅ Met | `ComputeRevisionHash()` on both unit and environment plans; sorted by unit identity then service key |
| AC4: Cross-unit dependency rejection | ✅ Met | `validateComposeDependenciesStayWithinUnit()` checks both `DependsOn` and `ComposeExtension.DependsOn` |
| AC5: Normative documentation | ✅ Met | `docs/deployment.md` covers unit-aware planning, unit-local Compose dependencies, revision hash semantics |

**Code quality observations:**
- The `EnvironmentPlanAssembler` cleanly handles three service categories: target (freshly built), siblings with stored state, and legacy siblings (hydrated). This is well-documented in the type's godoc.
- Legacy sibling hydration with opportunistic persistence (`persistHydratedSpec`) is best-effort with logged warnings — good resilience pattern.
- `deploymentUnitIndex` is a simple lookup struct that keeps the assembler logic clean.
- The `applyUnitIdentity` method correctly normalizes unit identity, injects unit labels, and recomputes the desired hash after label changes.
- Cross-unit validation covers both the flat `DependsOn` and Compose extension `DependsOn` map — no gaps.
- Narrow dependency interfaces (`envServiceStateLoader`, `serviceLoader`, etc.) keep the assembler testable without concrete repository implementations.

**Test coverage:** `runtime_environment_plan_test.go` covers: target replacement, sibling loading, legacy hydration, persistence failure resilience, tombstoned service exclusion, deterministic ordering, revision hash computation, deployment-unit grouping, cross-unit dependency rejection, nil target spec errors, first deploy, legacy without artifact, state load errors, and secrets inclusion. This is comprehensive.

### 4. Task 4: Hash-Based Drift Detection (bahia-jrkp) ✅

**What it delivers:** Desired-vs-observed hash drift comparison for desired-state-managed workloads, with fallback to artifact digest comparison for legacy rows.

**Acceptance criteria assessment:**

| Criterion | Status | Evidence |
|-----------|--------|----------|
| AC1: Hash-based drift for desired-state rows | ✅ Met | `registry.go:RecordObservation` — `desiredStateManaged()` gate selects hash path |
| AC2: in_sync requires hash match + healthy | ✅ Met | `observationHealthOK()` checks `Healthy` or `Starting`; both conditions required |
| AC3: drifted on hash mismatch | ✅ Met | Explicit `desiredHash != observedHash` comparison |
| AC4: unknown when hashes unavailable | ✅ Met | Empty desired or observed hash → `DriftStatusUnknown` |
| AC5: Observation normalization persisted | ✅ Met | `normalizeRuntimeObservationHash()` computes and propagates hashes |
| AC6: Legacy artifact digest fallback | ✅ Met | `else if state.DesiredArtifactID != nil` branch preserves old behavior |
| AC7: Nostr projections include hashes | ✅ Met | Per verification report: projections include `desired_hash` and `observed_hash` |
| AC8: Normative documentation | ✅ Met | `docs/deployment.md` "Desired-State Persistence" section describes drift semantics |

**Code quality observations:**
- The `desiredStateManaged()` predicate is clean: checks for `DesiredHash` or `DesiredRuntimeState` presence.
- `desiredStateHash()` has a nice lazy-compute pattern: checks stored hash first, then computes from stored state if needed.
- `observedStateHash()` correctly prefers `NormalizedState.ObservationHash` over top-level `NormalizedHash` — the former is authoritative.
- The `normalizeRuntimeObservationHash()` function is called early in `RecordObservation` to ensure hashes are available before drift comparison.
- Health status check (`observationHealthOK`) accepting both `Healthy` and `Starting` is appropriate — `Starting` is transient and should not trigger drift alerts.

---

## Cross-Cutting Observations

### Consistency ✅

- **Naming conventions** are consistent: `DesiredServiceSpec`, `DesiredEnvironmentPlan`, `DesiredDeploymentUnitPlan`, `NormalizedObservation` follow Go conventions and domain language.
- **Hash format** (`sha256:<hex>`) is uniform across desired hashes, observation hashes, and revision hashes.
- **Nil-to-empty normalization** is applied consistently in both `ComputeDesiredHash()` and `ComputeObservationHash()` — preventing nil/empty divergence.
- **Secret handling** follows the same pattern everywhere: `DesiredSecretRef` with `RedactedPlaceholder()`, `ContainsPlaintextSecret()` safety check, builder strips secrets from env maps.
- **Unit identity normalization** is centralized in `NormalizeDesiredServiceUnitIdentity()` and used by both the builder and the plan assembler.

### Test Coverage ✅

Test files covering the implementation:

| Area | Test File | Coverage |
|------|-----------|----------|
| Domain types & hashing | `runtime_desired_state_test.go` | JSON round-trip, hash stability, hash mutation, extension exclusion, secret key hashing, redaction, service key normalization, helpers |
| Golden hash fixtures | `runtime_desired_state_golden_test.go` | 4 locked golden hashes; nil/empty normalization; extension exclusion; slice ordering; redaction serialization; environment revision |
| Deployment units | `deployment_unit_test.go` | Implicit default synthesis, validation rules |
| Builder | `runtime_desired_state_builder_test.go` | Full build, Bahia labels, explicit unit identity, secret exclusion, hash stability, validation errors, no-config fallback, Docker runtime, ValidateSpec |
| Plan assembler | `runtime_environment_plan_test.go` | 15+ scenarios covering all assembler paths |
| Drift detection | Covered within registry + adapter tests | Hash comparison, legacy fallback, health gating |
| Runtime adapters | 5 test files in `adapters/runtime/` | Compose, Docker, Podman desired-state apply; capability; resolver |
| Persistence | `pg_desired_state_persistence_test.go`, `pg_deployment_unit_test.go` | JSONB storage, nullable FKs |

This is strong coverage with golden fixtures locking hash stability — a critical safety net for drift detection correctness.

### Documentation ✅

- `docs/deployment.md` is comprehensive and covers:
  - Deployment units and targeting model
  - Reconcile modes and ownership modes
  - Desired-state persistence schema
  - Drift detection semantics (hash-based vs. legacy)
  - Cross-unit Compose dependency rules
  - Secret handling guarantees
  - Backward compatibility strategy
  - Unit persistence transition triggers
- PSTF feature specs and verification reports provide traceability from requirements through implementation to test evidence.

### No Placeholder or Stub Code ✅

- `KubernetesExtension` and `PodmanExtension` are intentionally empty structs documented as "placeholder for future renderer data" — this is correct per the epic's out-of-scope boundary (Kubernetes stays on legacy path, Podman reuses Docker-compatible path).
- No `TODO`, `FIXME`, `STUB`, or `HACK` comments found in the reviewed files.
- All functions have real implementations with complete logic.

---

## Minor Observations (Non-Blocking)

1. **`bahia-wcwi` PSTF feature directory missing:** The beads issue exists and is closed, but there is no corresponding `pstf/features/bahia-wcwi/` directory. Task 1 evidence is documented under `pstf/features/DESIRED_STATE_RUNTIME/verification_report.md` instead. This is acceptable but inconsistent with Tasks 2–4 which each have their own PSTF feature directory.

2. **`newUUID` test helper in production file:** `runtime_desired_state_builder.go` line 312 declares `var newUUID = uuid.New` described as "a test helper alias" but it lives in the production file and appears unused. This is harmless but could be cleaned up.

3. **DESIRED_STATE_RUNTIME verification report stale:** The parent feature's verification report (`pstf/features/DESIRED_STATE_RUNTIME/verification_report.md`) still shows all 16 acceptance criteria as "Not verified" and all 21 test matrix items as "Not implemented." This predates the task-level work and should be updated to reflect current state. Non-blocking since individual task verification reports are current.

---

## Recommendations

1. **Update parent verification report** — Refresh `DESIRED_STATE_RUNTIME/verification_report.md` to reflect Tasks 1–4 completion. Low priority.
2. **Create `bahia-wcwi` PSTF directory** — For consistency with Tasks 2–4, add a feature spec and verification report for Task 1. Low priority.
3. **Clean up `newUUID` declaration** — Remove the unused test-helper alias from the production builder file if it's not needed.

---

## Verdict

**Approved.** All four tasks meet their acceptance criteria. The implementation is:

- **Consistent** — naming, hashing, nil normalization, and secret handling follow uniform patterns.
- **Well-tested** — golden hash fixtures lock serialization stability; comprehensive scenario coverage in unit tests; adapter-level desired-state tests cover Compose, Docker, and Podman.
- **Well-documented** — `docs/deployment.md` covers the full desired-state model including drift semantics, unit targeting, reconcile policies, and backward compatibility.
- **Production-ready** — no stubs, no placeholder logic, proper error handling, and defensive nil checks throughout.

The foundation is solid for subsequent work items (Compose phase 1 renderer, Docker Engine apply, observation normalization, Nostr enrichment).
