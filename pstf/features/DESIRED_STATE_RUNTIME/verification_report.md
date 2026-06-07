# Verification Report — DESIRED_STATE_RUNTIME

## Summary

**Status:** partially verified — desired-state baseline persistence, deploy request/direct action/rollback runtime lifecycle routing, Compose ownership inventory/config-gate behavior, first-deploy legacy sibling hydration, and documentation/evidence closeout are covered by targeted tests or documentation review.

- **Verified:** canonical desired-state hash stability; repository persistence for desired snapshots/hashes; Compose/Docker desired-state adapter capability; deploy request, direct runtime `action=deploy`, and rollback-to-artifact converge through `RuntimeLifecycleService.DeployWithStatus`; `bahia_owned` runtime target config is loadable, resolvable, and enforced by the Compose ownership gate; first desired-state deploy plans include stored and legacy siblings, persist hydrated legacy specs, and record post-apply observation/drift evidence; required Nostr/runtime/user docs describe additive desired-state metadata and Bahia-owned Compose/Docker behavior.
- **Open defects:** unrelated flaky service-package timeout tracked separately as `bahia-54eo`.
- **Matrix status:** targeted ownership rollout test DSR-T-022, first-deploy hydration test DSR-T-021, and deploy-convergence test DSR-T-023 implemented; live staging/prod directory inspection remains an operator decision/evidence task.

## Commands Run

- PASS: `go test ./internal/controlplane ./internal/service ./internal/domain ./internal/repository ./internal/adapters/runtime`
- PARTIAL: `go test ./...` (all packages passed except `internal/service`, which hit unrelated timeout in `TestAssistantOrchestratorSuppressesDuplicateApprovalWhileExecuting`, tracked as `bahia-54eo`)
- PASS: `go build ./...`
- PASS: `go test ./internal/config ./internal/adapters/runtime -run "TestLoadNestedRuntimeConfig|TestConfigRuntimeResolver_Compose.*BahiaOwned" -count=1`
- PASS: `go test ./internal/service -run 'TestRuntimeLifecycleDesiredStateDeployHydratesLegacySiblingsAndRecordsDrift|TestAssemble_|TestRuntimeLifecycleDeploy' -count=1`
- PASS: `go test ./internal/adapters/runtime -run 'TestComposeDesiredStateApplier|TestSafety_FullProjectApplyIncludesAllServices|TestComposeRenderer' -count=1`
- PASS: `go test ./internal/controlplane ./internal/api/dto ./internal/service -run 'TestHandleDeployRequestInvokesRuntimeLifecycleAndPersistsDesiredState|TestHandleRollbackRequestExecutesSharedDesiredStateDeployPath|TestDirectRuntimeDeployResultCarriesDesiredHashFromState|TestRollback|TestRegistry|Test.*Desired|Test.*Runtime|TestRuntimeActionResponseFromDomainCopiesObservation'`
- PASS: `go test ./internal/controlplane -run 'TestDirectRuntimeDeployResultCarriesDesiredHashFromState|TestDirectRuntimeRestartResultDoesNotCarryDesiredHashFromState|TestHandleRollbackRequestExecutesSharedDesiredStateDeployPath|TestHandleRollbackRequestRecordsFailedRunWhenDesiredStateBuildFails|TestHandleRollbackRequestRecordsFailedRunWhenDeployFails' -count=1`
- PASS: `go test ./internal/nostrmigration -count=1`
- PASS: `go test ./internal/adapters/runtime`
- PARTIAL: `go test ./internal/controlplane ./internal/service` (controlplane passed; service package hit unrelated timeout in `TestAssistantOrchestratorSuppressesDuplicateApprovalWhileExecuting`, tracked as `bahia-54eo`)
- Documentation closeout review: `docs/nostr-event-implementation-guide.md`, `docs/control-planes.md`, `docs/nostr-commands.md`, `docs/event-spec.md`, `docs/protocol-compatibility.md`, `docs/user-guide/nostr-integration.md`, `docs/user-guide/features/deployments.md`, `docs/user-guide/features/services.md`, `docs/deployment.md`, and `docs/api.md` updated for additive desired-state metadata and Bahia-owned Compose/Docker behavior. No runtime code changed in this closeout.

## 2026-05-30 Task 1 Evidence

- `internal/controlplane/reactor.go`: kind `5961` now builds a desired-state snapshot before intent persistence and invokes `RuntimeLifecycleService.DeployWithStatus` for approved intents.
- `internal/app/app.go`: reactor wiring passes `runtimeLifecycleSvc` via `controlplane.WithRuntimeLifecycleService(runtimeLifecycleSvc)`.
- `internal/service/runtime_lifecycle.go`: deploy path builds `DesiredServiceSpec`, assembles an environment plan, applies through `DesiredStateApplier` when supported, records observations, and updates `EnvironmentServiceState` with desired runtime state/hash.
- `internal/db/migrations/000037_desired_state_persistence.up.sql`: additive columns exist for intent desired state/hash, run apply metadata, state desired runtime state/hash, and observation normalized state/hash.
- `internal/domain/runtime_desired_state_golden_test.go`: golden hash fixture locks deterministic canonical hashing.
- `internal/controlplane/reactor_policy_gate_test.go`: `TestHandleDeployRequestInvokesRuntimeLifecycleAndPersistsDesiredState` proves 5961 routes to runtime lifecycle and persists desired state/hash on the intent.

## 2026-06-07 bahia-zu2p.8.9 Evidence

- `internal/service/runtime_lifecycle_test.go::TestRuntimeLifecycleDesiredStateDeployHydratesLegacySiblingsAndRecordsDrift` seeds a target service, a sibling with stored desired state, and a legacy sibling with only persisted service/runtime/artifact state. The lifecycle deploy path sends all three services to the desired-state apply seam, proving the first Compose desired-state plan does not omit existing managed siblings.
- The same test asserts the legacy sibling spec is reconstructed from persisted registry/runtime/artifact state and written back to `EnvironmentServiceState.DesiredRuntimeState`/`DesiredHash` opportunistically.
- The mock desired-state runtime reports a normalized observed hash matching the target desired hash; `RegistryService.RecordObservation` then records the post-apply observation, sets `CurrentObservationID`, updates `LastReconciledAt`, and advances target drift to `in_sync`.
- Focused runtime adapter tests continue to prove Compose full-project apply/render behavior covers multi-service plans without service-scoped `up`, `<SERVICE>_IMAGE`, or `--force-recreate` patterns.

## 2026-06-07 bahia-zu2p.8.10 Evidence

- `internal/controlplane/reactor.go::handleRollbackRequest` now creates the rollback intent, builds or reuses the selected artifact's desired snapshot/hash, creates a deployment run with apply metadata, executes the selected artifact via `RuntimeLifecycleService.DeployWithStatus`, completes the run, and publishes progression plus terminal ContextVM result metadata.
- `internal/service/registry.go` preserves desired runtime state/hash when deployment intents and completed runs update `EnvironmentServiceState`; rollback intents inherit desired snapshot/hash from the selected previously deployed intent when available.
- `internal/controlplane/operator_actions.go` and `internal/api/dto/responses.go` enrich direct runtime deploy terminal results with `desired_hash` from persisted environment service state when available; restart/stop action results deliberately do not carry desired-state metadata.
- `internal/controlplane/reactor_policy_gate_test.go::TestHandleRollbackRequestExecutesSharedDesiredStateDeployPath` proves rollback-to-artifact uses the shared desired-state helper, emits status progression, persists rollback intent/run desired metadata, and publishes terminal result metadata.
- `internal/controlplane/reactor_policy_gate_test.go::TestHandleRollbackRequestRecordsFailedRunWhenDesiredStateBuildFails` and `TestHandleRollbackRequestRecordsFailedRunWhenDeployFails` prove rollback failure paths record terminal failed deployment runs instead of leaving rollback state approved/deploying.
- `internal/controlplane/reactor_policy_gate_test.go::TestDirectRuntimeDeployResultCarriesDesiredHashFromState` proves direct runtime deploy terminal results expose the persisted desired hash.
- `internal/controlplane/reactor_policy_gate_test.go::TestDirectRuntimeRestartResultDoesNotCarryDesiredHashFromState` proves non-deploy direct runtime actions do not leak desired hashes from existing state.
- `internal/controlplane/reactor_policy_gate_test.go::TestHandleDeployRequestInvokesRuntimeLifecycleAndPersistsDesiredState` continues to prove deploy request desired snapshot/hash and run apply metadata persistence.
- `internal/adapters/runtime/compose_desired_state_test.go` and `internal/adapters/runtime/docker_apply_test.go` remain the adapter evidence that Compose/Docker desired-state paths do not use legacy Compose image substitution for desired-state-managed deploys.

## 2026-06-07 Documentation Closeout Evidence

- `docs/nostr-event-implementation-guide.md`, `docs/control-planes.md`, `docs/nostr-commands.md`, `docs/event-spec.md`, `docs/protocol-compatibility.md`, and `docs/user-guide/nostr-integration.md` now state that desired-state metadata is additive on ContextVM/canonical observables, does not allocate new Nostr kinds or d-tag coordinates, documents the shared status steps, and preserves backward-compatible decoding expectations.
- The same Nostr/control-plane docs document sanitized metadata boundaries: public relay content may include hashes, renderer/target keys, IDs, revision/apply summaries, and observation IDs, but not resolved secrets, generated Compose env-file contents, raw Docker transport material, Docker TLS material, bearer credentials, or NIP-98 credentials.
- `docs/user-guide/features/deployments.md`, `docs/user-guide/features/services.md`, and `docs/deployment.md` now document Bahia-owned Compose full-project synthesis, `.bahia/env/` generated secret material handling, `.bahia/render-state.json` ownership markers, full-project `up -d --remove-orphans`, Docker desired-hash no-op/recreate behavior, server-managed endpoint aliases, and deferred Kubernetes/fragments.
- `docs/deployment.md` no longer instructs desired-state-managed Compose users to rely on the legacy `<SERVICE>_IMAGE` image override pattern.
- `docs/api.md` documents the additive `desired_hash` response field for direct runtime deploy results when persisted desired-state metadata is available.

## Acceptance Criteria Status

| AC ID | Status | Basis |
|-------|--------|-------|
| DSR-AC-001 | Not verified | DSR-WI-01 not started |
| DSR-AC-002 | Not verified | DSR-WI-01 not started |
| DSR-AC-003 | Verified | DSR-T-004 and DSR-T-023 cover deploy request, direct runtime deploy action, rollback-to-artifact, status progression, terminal results, and shared `RuntimeLifecycleService.DeployWithStatus` convergence. |
| DSR-AC-004 | Not verified | DSR-WI-02 not started |
| DSR-AC-005 | Not verified | DSR-WI-03 not started |
| DSR-AC-006 | Verified | DSR-T-008 covers assembler hydration; DSR-T-021 covers lifecycle first-deploy plan inclusion and opportunistic persistence for legacy siblings. |
| DSR-AC-007 | Not verified | DSR-WI-04 not started |
| DSR-AC-008 | Not verified | DSR-WI-05 not started |
| DSR-AC-009 | Not verified | DSR-WI-06 not started |
| DSR-AC-010 | Not verified | DSR-WI-07 not started |
| DSR-AC-011 | Partially verified | DSR-T-021 covers post-apply observation recording and `in_sync` drift when normalized observed hash matches desired hash. Broader `drifted`/`unknown` cases remain in DSR-WI-07. |
| DSR-AC-012 | Partially verified | Documentation closeout records the additive status step contract on canonical status observables; DSR-T-006 remains the implementation-level guard. |
| DSR-AC-013 | Partially verified | Documentation closeout records backward-compatible additive result/read-model metadata and redaction expectations; DSR-T-018 remains the implementation-level catalog/decoder guard. |
| DSR-AC-014 | Partially verified | Compose ownership gate exists before writes; DSR-T-022 proves explicit config allow/block wiring for ownership inventory. |
| DSR-AC-015 | Not verified | DSR-WI-02 not started |
| DSR-AC-016 | Partially verified | DSR-T-021 proves first desired-state deploy includes stored and legacy managed siblings and persists hydration; DSR-T-022 proves ownership inventory/config gate; DSR-T-023 proves deploy request/direct action/rollback convergence for Compose/Docker desired-state-managed deploys; documentation closeout covers operator-facing rollout behavior. Live staging/prod ownership inspection remains operator evidence. |

## Test Matrix Status

- Total tests in matrix: 23
- Passing: 4
- Failing: 0
- Not implemented: 19
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
9. **DSR-WI-09** (rollout): DSR-T-021, DSR-T-022

## Defects

_No defects recorded yet._

## Ambiguities / Human Decisions Needed

1. **Compose directory ownership opt-in:** `bahia_owned` now makes the operator decision recordable per target. The checked-in staging and production `config.yaml` targets are recorded as `bahia_owned: false`, so rollout remains blocked unless a valid `.bahia/render-state.json` marker exists at the target directory or an operator explicitly changes the target to `bahia_owned: true` after confirming Bahia ownership.

2. **Deploy request routing:** Does `5961` deploy reach `RuntimeLifecycleService` directly or through an intermediate orchestration layer? This affects the scope of DSR-WI-02.

## Confidence Assessment

- **Specification confidence:** High — acceptance criteria are derived directly from the architecture plan with full work-item traceability.
- **Implementation confidence:** Not applicable — no implementation has begun.
- **Verification confidence:** Not applicable — no tests have been run.

## Recommendation

For bahia-zu2p.8.8, operators should inspect the actual staging and production `compose_dir` paths before rollout. Leave `bahia_owned: false` for any unknown/operator-authored directory; set `bahia_owned: true` only for dedicated Bahia-owned project directories, or rely on the `.bahia/render-state.json` marker created by prior Bahia rendering.

For bahia-zu2p.8.9, the first-deploy hydration path is covered by deterministic lifecycle and Compose adapter tests.

For bahia-zu2p.8.10, deploy request, direct runtime deploy action, and rollback-to-artifact now share the desired-state apply path for Compose/Docker-covered deploys. Continue separate live rollout/operator ownership verification before enabling authoritative Compose generation in staging or production.
