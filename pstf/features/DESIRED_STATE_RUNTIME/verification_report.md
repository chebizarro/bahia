# Verification Report — DESIRED_STATE_RUNTIME

## Summary

**Status:** partially verified — desired-state baseline persistence and 5961 runtime lifecycle routing are implemented and covered by targeted tests.

- **Verified:** canonical desired-state hash stability; repository persistence for desired snapshots/hashes; Compose/Docker desired-state adapter capability; 5961 deploy path into `RuntimeLifecycleService.DeployWithStatus`.
- **Open defects:** none recorded for the Task 1 touched scope.
- **Matrix status:** baseline Task 1 tests passing; broader roadmap items remain outside this verification pass.

## Commands Run

- `go test ./internal/controlplane ./internal/service ./internal/domain ./internal/repository ./internal/adapters/runtime`
- `go test ./...`
- `go build ./...`

## 2026-05-30 Task 1 Evidence

- `internal/controlplane/reactor.go`: kind `5961` now builds a desired-state snapshot before intent persistence and invokes `RuntimeLifecycleService.DeployWithStatus` for approved intents.
- `internal/app/app.go`: reactor wiring passes `runtimeLifecycleSvc` via `controlplane.WithRuntimeLifecycleService(runtimeLifecycleSvc)`.
- `internal/service/runtime_lifecycle.go`: deploy path builds `DesiredServiceSpec`, assembles an environment plan, applies through `DesiredStateApplier` when supported, records observations, and updates `EnvironmentServiceState` with desired runtime state/hash.
- `internal/db/migrations/000037_desired_state_persistence.up.sql`: additive columns exist for intent desired state/hash, run apply metadata, state desired runtime state/hash, and observation normalized state/hash.
- `internal/domain/runtime_desired_state_golden_test.go`: golden hash fixture locks deterministic canonical hashing.
- `internal/controlplane/reactor_policy_gate_test.go`: `TestHandleDeployRequestInvokesRuntimeLifecycleAndPersistsDesiredState` proves 5961 routes to runtime lifecycle and persists desired state/hash on the intent.

## Acceptance Criteria Status

| AC ID | Status | Basis |
|-------|--------|-------|
| DSR-AC-001 | Not verified | DSR-WI-01 not started |
| DSR-AC-002 | Not verified | DSR-WI-01 not started |
| DSR-AC-003 | Not verified | DSR-WI-02 not started |
| DSR-AC-004 | Not verified | DSR-WI-02 not started |
| DSR-AC-005 | Not verified | DSR-WI-03 not started |
| DSR-AC-006 | Not verified | DSR-WI-03 not started |
| DSR-AC-007 | Not verified | DSR-WI-04 not started |
| DSR-AC-008 | Not verified | DSR-WI-05 not started |
| DSR-AC-009 | Not verified | DSR-WI-06 not started |
| DSR-AC-010 | Not verified | DSR-WI-07 not started |
| DSR-AC-011 | Not verified | DSR-WI-07 not started |
| DSR-AC-012 | Not verified | DSR-WI-08 not started |
| DSR-AC-013 | Not verified | DSR-WI-08 not started |
| DSR-AC-014 | Not verified | DSR-WI-05 not started |
| DSR-AC-015 | Not verified | DSR-WI-02 not started |
| DSR-AC-016 | Not verified | DSR-WI-09 not started |

## Test Matrix Status

- Total tests in matrix: 21
- Passing: 0
- Failing: 0
- Not implemented: 21
- Blocked: 0

### Verification Sequence

Tests should be verified in work-item dependency order:

1. **DSR-WI-01** (domain/schema): DSR-T-001, DSR-T-002, DSR-T-003
2. **DSR-WI-02** (lifecycle/locking): DSR-T-004, DSR-T-005, DSR-T-006, DSR-T-020
3. **DSR-WI-03** (builder/hydration): DSR-T-007, DSR-T-008
4. **DSR-WI-04** (adapter capability): DSR-T-009
5. **DSR-WI-05** (Compose renderer): DSR-T-010, DSR-T-011, DSR-T-012, DSR-T-019
6. **DSR-WI-06** (Docker Engine): DSR-T-013, DSR-T-014
7. **DSR-WI-07** (observation/drift): DSR-T-015, DSR-T-016, DSR-T-017
8. **DSR-WI-08** (Nostr enrichment): DSR-T-018
9. **DSR-WI-09** (rollout): DSR-T-021

## Defects

_No defects recorded yet._

## Ambiguities / Human Decisions Needed

1. **Compose directory ownership opt-in:** Is every configured production `compose_dir` Bahia-owned, or does rollout need a per-environment opt-in flag? This must be resolved before DSR-WI-05 can be verified in production-like staging.

2. **Deploy request routing:** Does `5961` deploy reach `RuntimeLifecycleService` directly or through an intermediate orchestration layer? This affects the scope of DSR-WI-02.

## Confidence Assessment

- **Specification confidence:** High — acceptance criteria are derived directly from the architecture plan with full work-item traceability.
- **Implementation confidence:** Not applicable — no implementation has begun.
- **Verification confidence:** Not applicable — no tests have been run.

## Recommendation

Implementation should begin with DSR-WI-01 (domain contract and schema). The two open questions in the feature spec should be resolved early, ideally before DSR-WI-02 (lifecycle refactor) and DSR-WI-05 (Compose renderer) begin.
