# Review: Policy-Driven Reconciliation (bahia-f1yp / bahia-dskm)

**Date:** 2026-05-30
**Feature:** Policy-Driven Reconciliation
**PSTF ID:** bahia-dskm
**Scope:** Reconciler policy modes, auto-remediation via shared deploy helper, environment apply lock contention, failure/backoff persistence

---

## Context / Scope

Task 12 adds policy-driven reconciliation to the Bahia reconciler. Before this work, the reconciler observed runtime state and recorded drift but did not act on it — `auto_apply` did not invoke the desired-state deploy helper, and `approval_required` did not project a distinct `remediation_needed` drift status. The environment apply lock was used only by user-initiated deploys; scheduled reconciliation did not participate.

### Files reviewed

| File | Lines | Role |
|---|---|---|
| `internal/reconcile/reconciler.go` | 351 | Core reconciliation loop and policy dispatch |
| `internal/service/runtime_lifecycle.go` | 749 | `AutoRemediateDesiredState` + shared deploy helper |
| `internal/service/runtime_apply_lock.go` | 134 | PostgreSQL advisory lock with `TryLock` |
| `internal/domain/models.go` | — | `DriftStatus`, `ReconcileMode`, `EnvironmentServiceState` fields |
| `internal/domain/deployment_unit.go` | — | `ReconcileMode` constants, validation, normalization |
| `internal/db/migrations/000042_reconcile_policy_state.up.sql` | 8 | Schema additions for failure/backoff columns |
| `internal/reconcile/reconciler_test.go` | 283 | Unit tests for all reconcile modes |
| `internal/service/runtime_lifecycle_test.go` | — | Lock contention test |
| `internal/reconcile/remediation.go` | 393 | Legacy `Remediator` (pre-existing, not part of this task) |
| `docs/deployment.md` | 350 | Operator-facing reconciliation documentation |
| `pstf/features/bahia-dskm/` | 6 files | PSTF feature spec, acceptance criteria, test matrix, verification |

---

## Findings

### 1. Acceptance Criteria Coverage

| AC | Statement | Verdict | Evidence |
|---|---|---|---|
| AC1 | Reconciler uses shared environment apply lock; auto-remediation does not block behind active user operations | ✅ Met | `AutoRemediateDesiredState` calls `deployDesiredState(…, waitForLock: false)`. `acquireApplyLock` delegates to `TryLock` when `wait=false`, returning `ErrEnvironmentApplyLockContended` on contention. Test `TestRuntimeLifecycleAutoRemediationDoesNotBlockBehindActiveUserApply` validates this. |
| AC2 | `auto_apply` invokes the shared desired-state deploy helper using persisted desired artifact/state | ✅ Met | `reconcileOne` calls `r.autoApplyDesiredState` only when `mode == ReconcileModeAutoApply`. `autoApplyDesiredState` calls `r.deployer.AutoRemediateDesiredState`, which resolves the desired artifact from `environment_service_state.desired_artifact_id` via `resolveDeployArtifact(ctx, serviceID, envID, nil)`. Test `TestReconcilerAutoApplyUsesSharedDesiredStateHelper` validates the call count. |
| AC3 | `approval_required` marks drift as `remediation_needed` without deploying | ✅ Met | `driftStatusForMode(ReconcileModeApprovalRequired)` returns `DriftStatusRemediationNeeded`. The deploy helper is never called for this mode. Test `TestReconcilerApprovalRequiredMarksRemediationNeededWithoutInternalDeploy` confirms zero deployer calls and correct drift status. |
| AC4 | Failure/contention preserves desired state, stores failure metadata, applies backoff | ✅ Met | `recordReconcileFailure` persists `ReconcileFailureMetadata`, `ReconcileBackoffUntil`, and increments `ReconcileConsecutiveFailures`. `reconcileOne` skips entries where `ReconcileBackoffUntil` is in the future. `clearReconcileFailure` resets on success. Test `TestReconcilerAutoApplyFailureKeepsDesiredStateAndBacksOff` validates metadata, backoff, and preserved desired hash/artifact. |
| AC5 | Internal auto-remediation does not synthesize fake external control-plane events | ✅ Met | `autoApplyDesiredState` calls the deploy helper directly — no event publishing of external/public event kinds. The reconciler publishes only internal `EventDriftDetected`, `EventEnvironmentServiceStateChanged`, and `EventReconcileCompleted`. No `5968 DriftRemediate` events are synthesized. |
| AC6 | Normative deployment docs describe reconciliation behavior | ✅ Met | `docs/deployment.md` "Deployment Units and Targeting" section documents all four modes (`observe_only`, `auto_apply`, `approval_required`, `disabled`), lock contention behavior, failure/backoff persistence, and the relationship between environment `default_reconcile_mode` and unit `reconcile_mode` overrides. |

### 2. Code Quality & Consistency

**Strengths:**

- **Clean separation of concerns:** The reconciler owns observation/drift-detection logic and delegates remediation to an injected `AutoRemediationDeployer` interface. The `RuntimeLifecycleService` exposes `AutoRemediateDesiredState` as a thin wrapper over the shared `deployDesiredState` helper, differing only in lock acquisition strategy (`waitForLock: false`).
- **Functional options pattern:** `WithAutoRemediationDeployer` follows the same `Option func(*T)` pattern used throughout the codebase (e.g., `WithRuntimeApplyLock`, `WithRuntimeLifecycleSecrets`).
- **Backoff is bounded and deterministic:** Exponential backoff caps at 30 minutes and 6 doublings. No jitter, which is acceptable for single-instance reconciliation.
- **Lock safety:** `RuntimeApplyLock.acquire` uses a dedicated pgx connection for the advisory lock session, background context for unlock, and idempotent `unlocked` guard. `TryLock` cleanly releases the connection on non-acquisition.
- **Migration is additive and reversible:** `000042` adds nullable columns with `IF NOT EXISTS` guards and a partial index on `reconcile_backoff_until`. The down migration drops cleanly.

**Observations (non-blocking):**

1. **`remediation.go` coexistence:** The older `Remediator` struct in `remediation.go` has overlapping responsibility — it also handles drift-triggered redeployment with its own in-memory retry/cooldown state. This is the legacy remediation path (driven by `auto_remediation` in `RuntimeConfig`), while the new policy-driven path uses `ReconcileMode` on typed `EnvironmentTargeting`. Both can coexist, but operators may be confused by two remediation systems. A follow-up issue to deprecate the legacy `Remediator` or document the migration path would be valuable.

2. **No jitter on backoff:** The exponential backoff in `reconcileBackoff` is deterministic (power-of-two multiples of interval). For a single-instance reconciler this is fine. If multi-instance reconciliation is ever added, jitter would prevent thundering herd on shared environments.

3. **`driftStatusForMode` returns `DriftStatusDrifted` for `auto_apply`:** This is intentional — the status is set to `drifted` first, then `autoApplyDesiredState` is called. If auto-apply succeeds, `clearReconcileFailure` resets failure metadata but does not update `DriftStatus` to `in_sync`; that happens on the next reconciliation cycle when hashes match. This is a reasonable design choice but could cause a brief window where status shows `drifted` despite a successful remediation. The next observation cycle resolves it.

4. **`observe_only` uses `DriftStatusDrifted`:** `driftStatusForMode` is only called for `approval_required` (returns `remediation_needed`) and falls through to `DriftStatusDrifted` for all other modes including `auto_apply` and `observe_only`. For `observe_only`, `driftStatusForMode` is called via the same path since both `observe_only` and `auto_apply` reach `r.driftStatusForMode(mode)`. This is correct because the conditional at the end (`if newDrift == DriftStatusDrifted && mode == ReconcileModeAutoApply`) only triggers auto-apply for `auto_apply` mode. `observe_only` correctly records drift without remediation.

### 3. Test Coverage Assessment

| Test | Criteria | What it validates |
|---|---|---|
| `TestReconcilerObserveOnlyRecordsDriftWithoutRemediation` | observe_only behavior | Drift recorded, no deploy triggered, observation persisted |
| `TestReconcilerApprovalRequiredMarksRemediationNeededWithoutInternalDeploy` | AC3, AC5 | `remediation_needed` status, zero deployer calls |
| `TestReconcilerAutoApplyUsesSharedDesiredStateHelper` | AC2 | Deployer called exactly once, no failure metadata |
| `TestReconcilerAutoApplyFailureKeepsDesiredStateAndBacksOff` | AC4 | Desired state preserved, backoff set, failure count incremented, failure reason stored |
| `TestReconcilerSkipsDisabledDeploymentUnit` | disabled mode | No observations, no state updates |
| `TestRuntimeLifecycleAutoRemediationDoesNotBlockBehindActiveUserApply` | AC1 | `ErrEnvironmentApplyLockContended` returned, no deploy executed |

**Coverage gaps (minor):**

- **Lock contention recorded as failure metadata:** The reconciler distinguishes `"environment_apply_lock_contended"` from `"auto_apply_failed"` in `autoApplyDesiredState`, but no test specifically validates the `"environment_apply_lock_contended"` reason string in `ReconcileFailureMetadata`. The lock contention test validates the error return but not the reconciler's metadata recording path.
- **Backoff escalation:** No test exercises multiple consecutive failures to verify exponential backoff growth (e.g., 1× → 2× → 4× interval).
- **`clearReconcileFailure` after successful auto-apply:** `TestReconcilerAutoApplyUsesSharedDesiredStateHelper` checks that failure metadata is nil, but doesn't seed prior failure state to verify the clear path specifically.
- **`reconcileMode` unit override path:** `TestReconcilerSkipsDisabledDeploymentUnit` tests unit-level `ReconcileModeDisabled` overriding environment `ReconcileModeObserveOnly`. No test validates a unit override of `auto_apply` over an environment default of `observe_only`.

These are edge-case gaps, not blockers. The core behavioral paths are well-covered.

### 4. Documentation Assessment

`docs/deployment.md` provides thorough operator-facing documentation:

- ✅ All four reconcile modes documented with clear behavioral descriptions
- ✅ Lock contention behavior explained (user-initiated blocks, scheduled non-blocking)
- ✅ Failure/backoff persistence described
- ✅ Unit-level override semantics documented
- ✅ Relationship to `5968 DriftRemediate` for `approval_required` stated correctly ("Bahia does not synthesize a public remediation request internally")
- ✅ Backward compatibility (implicit default unit, legacy digest comparison) documented

**Minor note:** The documentation describes `approval_required` waiting for "an authorized operator-authored `5968 DriftRemediate` event" but does not reference where that event handler is implemented. This is acceptable since the handler is out of scope for this task (it's the control plane's responsibility), but a cross-reference would help operators.

### 5. Placeholder / Stub Code Check

- ✅ No TODO, FIXME, or placeholder comments in any of the reviewed files
- ✅ No stub implementations — all methods have complete logic
- ✅ `AutoRemediateDesiredState` is a real implementation delegating to `deployDesiredState`, not a stub
- ✅ `TryLock` uses real `pg_try_advisory_lock`, not a mock or no-op
- ✅ Migration is complete SQL, not a placeholder

### 6. PSTF Completeness

| Artifact | Status |
|---|---|
| `feature_spec.json` | ✅ Complete — intent, observed-before, intended-behavior, scope |
| `acceptance_criteria.json` | ✅ 6 criteria, all verified |
| `test_matrix.json` | ✅ Maps tests to criteria; all tests exist and are referenced |
| `verification_report.md` | ✅ Evidence references correct files, test commands documented |
| `defects.json` | ✅ Empty — no defects found |
| `hitl_decisions.md` | ✅ Explicitly states no human decision required |

---

## Recommendations

### Follow-up (non-blocking)

1. **Deprecate legacy `Remediator`:** Create a follow-up issue to document the migration path from `remediation.go`'s `auto_remediation` config-driven system to the new `ReconcileMode`-driven policy system, and eventually deprecate the former.

2. **Add lock-contention metadata test:** A small test seeding `ErrEnvironmentApplyLockContended` through the mock deployer and verifying `ReconcileFailureMetadata["reason"] == "environment_apply_lock_contended"` would close the remaining coverage gap.

3. **Add backoff escalation test:** A test running 3+ consecutive failures and asserting increasing `ReconcileBackoffUntil` values would validate the exponential backoff math.

---

## Verdict

**✅ APPROVED — All acceptance criteria met. Implementation is consistent, well-tested, and fully documented. No placeholder or stub code exists. Ready to close.**
