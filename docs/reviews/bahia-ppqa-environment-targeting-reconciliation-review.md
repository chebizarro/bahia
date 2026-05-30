# Review Report: Environment Targeting & Reconciliation Foundation

**Review ID:** bahia-ppqa  
**Date:** 2026-05-30  
**Scope:** Tasks 5–6 (bahia-g486, bahia-jmea)  
**Status:** ✅ Approved

---

## Context / Scope

This review covers the implementation of two foundational tasks from the Environment Targeting & Reconciliation Foundation epic:

- **Task 5 (bahia-g486):** Deepen Environment Targeting and Placement — typed targeting on environments and deployment units, legacy `runtime_config` backward compatibility, API DTO additions, and Nostr projection unit tags.
- **Task 6 (bahia-jmea):** Reconcile Policy Fields and Observation Scheduler — reconcile mode persistence, policy-aware observation scheduling, and the full reconciliation loop including auto-apply and approval-required semantics.

### Key Files Reviewed

| File | Purpose |
|------|---------|
| `internal/domain/deployment_unit.go` | Domain types, normalization, validation |
| `internal/domain/models.go` | `EnvironmentTargeting`, `EnvironmentServiceState` |
| `internal/domain/deployment_unit_test.go` | Unit tests for implicit default units and validation |
| `internal/reconcile/reconciler.go` | Reconciliation loop, policy resolution, auto-apply |
| `internal/reconcile/reconciler_test.go` | Reconciler tests (5 test cases) |
| `internal/repository/pg_state.go` | `ListDueForObservation` query with policy filtering |
| `internal/db/migrations/000038_deployment_units.up.sql` | Deployment units table + nullable FK placements |
| `internal/db/migrations/000039_environment_targeting.up.sql` | Typed targeting column + unit field promotion |
| `docs/deployment.md` | Normative deployment documentation |
| `pstf/features/bahia-g486/*` | Feature spec, acceptance criteria, test matrix, verification |
| `pstf/features/bahia-jmea/*` | Feature spec, acceptance criteria, test matrix, verification, defects |

---

## Findings

### 1. Acceptance Criteria Coverage

#### Task 5 (bahia-g486) — All 6 criteria met

| AC | Description | Verdict | Evidence |
|----|-------------|---------|----------|
| AC1 | Typed targeting fields on Environment | ✅ Pass | `EnvironmentTargeting` struct in `models.go:222–228` with `DefaultUnitKey`, `FailureDomainLabels`, `SecretScopeMode`, `DefaultReconcileMode` |
| AC2 | Typed runtime/ownership fields on DeploymentUnit | ✅ Pass | `DeploymentUnit` struct in `deployment_unit.go:43–57` with `EndpointRef`, `ComposeDir`, `Namespace`, `NetworkProfile`, `ReconcileMode`, `OwnershipMode` |
| AC3 | Legacy runtime_config read-fallback + write-forward normalization | ✅ Pass | `NormalizeEnvironmentTargeting` and `NormalizeDeploymentUnitTargeting` read from `runtime_config` keys as fallback; migration 000039 promotes existing `runtime_config` values into typed columns |
| AC4 | Additive API DTO fields | ✅ Pass | Verified by handler compilation tests per test matrix |
| AC5 | Nostr projection unit tags (31961, 31963, 31967, 31968) | ✅ Pass | Verified by `go test ./internal/adapters/nostr` per test matrix |
| AC6 | Normative documentation | ✅ Pass | `docs/deployment.md` "Deployment Units and Targeting" section comprehensively documents typed targeting, compatibility semantics, implicit vs explicit units, and placement IDs |

#### Task 6 (bahia-jmea) — All 5 criteria met

| AC | Description | Verdict | Evidence |
|----|-------------|---------|----------|
| AC1 | ReconcileMode domain values | ✅ Pass | `observe_only`, `auto_apply`, `approval_required`, `disabled` defined with `ValidateReconcileMode` in `deployment_unit.go:196–207` |
| AC2 | Policy persistence (env + unit) | ✅ Pass | Migration 000038 `CHECK (reconcile_mode IN (...))` on `deployment_units`; migration 000039 `targeting` JSONB on `environments` with `default_reconcile_mode` |
| AC3 | Scheduler selects due rows, skips disabled | ✅ Pass | `ListDueForObservation` SQL uses `COALESCE` chain with unit → env targeting → env runtime_config fallback, excludes `'disabled'`; `TestReconcilerSkipsDisabledDeploymentUnit` verifies |
| AC4 | Observe-only records drift without remediation | ✅ Pass | `TestReconcilerObserveOnlyRecordsDriftWithoutRemediation` confirms observation created, drift detected, no deploy/undeploy calls |
| AC5 | Normative documentation | ✅ Pass | `docs/deployment.md` documents reconcile policy persistence, observe-only semantics, auto-apply, approval-required, disabled, backoff, and lock contention |

### 2. Code Quality & Consistency

**Domain layer (`deployment_unit.go`):**
- Clean separation of concerns: normalization functions are pure and reusable
- `NormalizeEnvironmentTargeting` and `NormalizeDeploymentUnitTargeting` follow a consistent pattern: check typed field → fallback to `runtime_config` → apply default
- Helper functions (`stringFromRuntimeConfig`, `stringMapFromRuntimeConfig`, `firstNonEmpty`, `copyRuntimeConfig`) are well-scoped private utilities — no over-abstraction
- `ValidateDeploymentUnit` enforces all required fields including policy and ownership — good defense-in-depth
- `NewImplicitDefaultDeploymentUnit` correctly normalizes before building, validates runtime type, and marks `Implicit: true`

**Reconciler (`reconciler.go`):**
- Option pattern (`WithAutoRemediationDeployer`) follows Go conventions cleanly
- `reconcileMode` resolution correctly cascades: explicit unit → environment targeting (normalized)
- `reconcileAll` uses `ListDueForObservation` with interval-based `dueBefore` — correct scheduling semantics
- `driftStatusForMode` properly distinguishes `approval_required` (`remediation_needed`) from `auto_apply` (`drifted`)
- Auto-apply path: invokes shared deployer, clears failure on success, records failure metadata + exponential backoff on error
- Backoff: capped at 30 minutes, binary exponential from interval, capped at 6 doublings — reasonable production-ready defaults
- Lock contention handled gracefully: records specific reason `"environment_apply_lock_contended"` vs generic `"auto_apply_failed"`

**Migrations:**
- 000038: `CHECK` constraints on enum columns enforce data integrity at the DB level — good
- 000038: Nullable FK `deployment_unit_id` on all four tables with `ON DELETE SET NULL` — correct for transition-triggered persistence
- 000039: Data migration promotes existing `runtime_config` values with `COALESCE`/`NULLIF` chains — safe and idempotent
- Both migrations have corresponding `.down.sql` files (verified by path search)

**SQL query (`ListDueForObservation`):**
- Multi-level `COALESCE` chain mirrors domain normalization logic — good consistency
- `LEFT JOIN deployment_units` handles NULL `deployment_unit_id` correctly
- Excludes disabled at the query level — avoids loading unnecessary rows

### 3. Test Coverage

**Domain tests (2 tests):**
- `TestNewImplicitDefaultDeploymentUnitUsesEnvironmentRuntimeConfig` — validates the full normalization path from raw `runtime_config` to typed fields; checks `RuntimeType`, `EndpointRef`, `ComposeDir`, `ReconcileMode`, `SecretScopeMode`, `OwnershipMode`
- `TestValidateDeploymentUnitRequiresPolicyAndOwnership` — validates both positive and negative cases for required policy fields

**Reconciler tests (5 tests):**
- `TestReconcilerObserveOnlyRecordsDriftWithoutRemediation` — core observe-only path
- `TestReconcilerApprovalRequiredMarksRemediationNeededWithoutInternalDeploy` — approval mode sets `remediation_needed`, deployer never called
- `TestReconcilerAutoApplyUsesSharedDesiredStateHelper` — auto-apply invokes deployer, clears failure metadata
- `TestReconcilerAutoApplyFailureKeepsDesiredStateAndBacksOff` — failure records metadata, backoff, preserves desired state
- `TestReconcilerSkipsDisabledDeploymentUnit` — disabled unit prevents observation entirely

**Coverage assessment:** Tests cover all four reconcile modes, the happy/failure paths for auto-apply, the disabled-skip path, and the implicit/explicit unit resolution. The test infrastructure uses comprehensive mocks for all repository interfaces. This is adequate coverage for the foundation layer.

**Minor gap (non-blocking):** No dedicated test for `reconcileBackoff` capping at 30 minutes or at >6 failures. These are low-risk implementation details with straightforward math.

### 4. Documentation

`docs/deployment.md` is thorough and well-structured:
- "Deployment Units and Targeting" section covers typed targeting, compatibility semantics, implicit vs explicit units, placement, and Nostr projection tags
- Reconcile policy section documents all four modes with operational semantics
- Auto-apply lock contention and backoff behavior are documented
- Desired-state persistence and drift detection are documented with hash comparison semantics
- Compose cross-unit dependency rejection is called out
- Secret redaction policy is stated

No documentation gaps identified.

### 5. No Placeholder / Stub Code

All reviewed code is production-quality:
- No `TODO`, `FIXME`, or `HACK` markers in the reviewed files
- No empty function bodies or panic stubs
- `AutoRemediationDeployer` interface is cleanly optional via the Option pattern — nil check in `autoApplyDesiredState` returns a proper failure record rather than panicking
- All validation functions return domain-typed errors (`ErrInvalidValue`, `ErrNilUUID`, `ErrEmptyField`)

### 6. PSTF Completeness

Both features have complete PSTF artifacts:
- `feature_spec.json` — well-scoped intent and scope
- `acceptance_criteria.json` — traceable criteria with test references
- `test_matrix.json` — maps tests to criteria with evidence
- `verification_report.md` — includes test evidence and notes on concurrent task conflicts
- `defects.json` — empty (no defects)
- `hitl_decisions.md` (bahia-jmea only) — documents that no human decisions were required

---

## Recommendations

1. **Consider adding a backoff unit test** (P4, non-blocking): A small test for `reconcileBackoff` verifying the 30-minute cap and the failure-count clamp at 6 would improve confidence in edge-case behavior.

2. **Monitor migration 000039 on large datasets** (operational note): The `UPDATE environments SET targeting = ...` data migration scans all rows. For very large `environments` tables, consider running this during a maintenance window. Not a code issue — just an operational note.

---

## Verdict

**✅ Approved.** Tasks 5 and 6 are complete, well-tested, consistently implemented, and thoroughly documented. All acceptance criteria are met. No placeholder code exists. The implementation follows project patterns (domain normalization, Option pattern, repository interfaces, PSTF artifacts) and maintains backward compatibility through read-tolerant, write-forward semantics.
