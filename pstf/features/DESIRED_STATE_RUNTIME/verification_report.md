# Verification Report — DESIRED_STATE_RUNTIME

## Summary

**Status:** partially verified — desired-state baseline persistence, deploy request/direct action/rollback runtime lifecycle routing, Compose ownership inventory/config-gate behavior, first-deploy legacy sibling hydration, and documentation/evidence closeout are covered by targeted tests or documentation review.

- **Verified in targeted slices:** desired-state hash stability and persistence regressions touched by this rollout; Compose/Docker desired-state adapter capability; deploy request, direct runtime `action=deploy`, and rollback-to-artifact converge through `RuntimeLifecycleService.DeployWithStatus`; `bahia_owned` runtime target config is loadable, resolvable, and enforced by the Compose ownership gate; first desired-state deploy plans include stored and legacy siblings, persist hydrated legacy specs, and record post-apply observation/drift evidence; required Nostr/runtime/user docs describe additive desired-state metadata and Bahia-owned Compose/Docker behavior.
- **Open defects:** none in the touched desired-state scope; previously observed service-package timeout remains tracked separately as `bahia-54eo` but did not reproduce in the final full suite.
- **Matrix status:** targeted ownership rollout test DSR-T-022, first-deploy hydration test DSR-T-021, deploy-convergence test DSR-T-023, and full-project Compose apply guard DSR-T-012 implemented; live staging/prod directory inspection remains an operator decision/evidence task.

## Commands Run

- PASS: `go test ./internal/controlplane ./internal/service ./internal/domain ./internal/repository ./internal/adapters/runtime`
- PASS: `go test ./...`
- PASS: `go build ./...`
- PASS: `go test ./internal/config ./internal/adapters/runtime -run "TestLoadNestedRuntimeConfig|TestConfigRuntimeResolver_Compose.*BahiaOwned" -count=1`
- PASS: `go test ./internal/service -run 'TestRuntimeLifecycleDesiredStateDeployHydratesLegacySiblingsAndRecordsDrift|TestAssemble_|TestRuntimeLifecycleDeploy' -count=1`
- PASS: `go test ./internal/adapters/runtime -run 'TestComposeDesiredStateApplier|TestSafety_FullProjectApplyIncludesAllServices|TestComposeRenderer' -count=1`
- PASS: `go test ./internal/controlplane ./internal/api/dto ./internal/service -run 'TestHandleDeployRequestInvokesRuntimeLifecycleAndPersistsDesiredState|TestHandleRollbackRequestExecutesSharedDesiredStateDeployPath|TestDirectRuntimeDeployResultCarriesDesiredHashFromState|TestRollback|TestRegistry|Test.*Desired|Test.*Runtime|TestRuntimeActionResponseFromDomainCopiesObservation'`
- PASS: `go test ./internal/controlplane -run 'TestDirectRuntimeDeployResultCarriesDesiredHashFromState|TestDirectRuntimeRestartResultDoesNotCarryDesiredHashFromState|TestHandleRollbackRequestExecutesSharedDesiredStateDeployPath|TestHandleRollbackRequestPersistsFallbackDesiredStateBeforeCompletingRun|TestHandleRollbackRequestRecordsFailedRunWhenDesiredStateBuildFails|TestHandleRollbackRequestRecordsFailedRunWhenDeployFails' -count=1`
- PASS: `go test ./internal/nostrmigration -count=1`
- PASS: `go test ./internal/adapters/runtime`
- PASS: `go test ./internal/controlplane ./internal/service` on final full-suite coverage via `go test ./...`; earlier service-package timeout is tracked as `bahia-54eo` and did not reproduce in the final run.
- PASS: `go test ./internal/adapters/runtime -run 'TestComposeDesiredStateApplier_UpCommand|TestComposeDesiredStateApplier_NoServiceImageEnv|TestSafety_NoForceRecreate_UpCommandStructure|TestSafety_NoServiceImageEnvOverrides|TestSafety_FullProjectApply_NoServiceScopedUp' -count=1`
- PASS: `go test ./internal/adapters/runtime -run 'TestUnsupportedRuntimesReturnExplicitError|TestResolveDesiredStateApplier_KubernetesExplicitError|TestDesiredStateApplierInterfaceCompiles' -count=1`
- PASS: `go test ./internal/domain -run 'TestDesiredServiceSpec|TestDesiredState|TestCanonical|TestHash' -count=1`
- PASS: `go test ./internal/adapters/runtime ./internal/domain`
- PASS: `go test ./internal/service -run 'TestRegistry|TestRollback|TestCompleteDeploymentRun|TestCreateDeploymentRun' -count=1`
- PASS: `go test ./internal/repository -count=1`
- Documentation closeout review: `docs/nostr-event-implementation-guide.md`, `docs/control-planes.md`, `docs/nostr-commands.md`, `docs/event-spec.md`, `docs/protocol-compatibility.md`, `docs/user-guide/nostr-integration.md`, `docs/user-guide/features/deployments.md`, `docs/user-guide/features/services.md`, `docs/deployment.md`, and `docs/api.md` updated for additive desired-state metadata and Bahia-owned Compose/Docker behavior. No runtime code changed in this closeout.

- PASS (2026-08-02 review remediation): `go test ./internal/repository ./internal/service ./internal/controlplane ./internal/workflow ./pkg/client ./cmd/cli`
- PASS (2026-08-02 review remediation): `go build ./... && go vet ./...`
- EXPECTED PRE-EXISTING FAILURE (2026-08-02): `go test ./...` fails only in `internal/soulfactory::TestOpenClawCommandDriverDefaultsToWrapperSupportedMethods`, tracked as `bahia-csxyx`; isolated reproduction confirmed the same wrapper-method expectation mismatch.

## 2026-08-02 Path A Review Remediation Evidence

- **P0 default resolution:** repository tests prove normalized `targeting.default_unit_key` selects an explicit non-default unit, while missing configured keys and explicit sets without the configured default fail closed; implicit synthesis remains limited to the normalized `default` key with no explicit units.
- **P0 pre-intent Path A snapshot:** runtime lifecycle tests prove a new, non-adopted service can build a desired-state snapshot through a Bahia-managed Compose unit and that the snapshot retains unit ID, key, and runtime type before intent creation.
- **P1 complete-set concurrency:** repository/service/control-plane/client/CLI tests prove `expected_updated_at` is required for complete-set unit writes, checked under `FOR UPDATE`, stale writes return ContextVM code `-32009` without local mutation, event emission, or relay-first canonical publication, and the CLI rereads/remerges for at most three newly signed attempts while preserving unrelated concurrent units.
- **P1 durable unit identity:** service tests prove run completion selects deployment-unit ID in order from run, intent, then desired state; coordinator routing also records the resolved explicit unit on non-Compose runs.
- **P1 implicit-to-explicit targeting:** CLI tests prove unit create publishes `targeting.default_unit_key=max` and the single explicit `max` unit in one signed update, and both unit create/update expose `--default-unit-key`.
- Contract documentation records authoritative complete-set semantics, the revision precondition/conflict code, bounded retry behavior, and atomic targeting changes without adding new Nostr kinds or exposing secrets.

## 2026-05-30 Task 1 Evidence

- `internal/controlplane/reactor.go`: kind `5961` now builds a desired-state snapshot before intent persistence and invokes `RuntimeLifecycleService.DeployWithStatus` for approved intents.
- `internal/app/app.go`: reactor wiring passes `runtimeLifecycleSvc` via `controlplane.WithRuntimeLifecycleService(runtimeLifecycleSvc)`.
- `internal/service/runtime_lifecycle.go`: deploy path builds `DesiredServiceSpec`, assembles an environment plan, applies through `DesiredStateApplier` when supported, records observations, and updates `EnvironmentServiceState` with desired runtime state/hash.
- `internal/db/migrations/000037_desired_state_persistence.up.sql`: additive columns exist for intent desired state/hash, run apply metadata, state desired runtime state/hash, and observation normalized state/hash.
- `internal/domain/runtime_desired_state_golden_test.go`: golden hash fixture locks deterministic canonical hashing.
- `internal/controlplane/reactor_policy_gate_test.go`: `TestHandleDeployRequestInvokesRuntimeLifecycleAndPersistsDesiredState` proves 5961 routes to runtime lifecycle and persists desired state/hash on the intent.

## 2026-06-07 bahia-zu2p.9.2 Evidence

- `internal/adapters/runtime/desired_state_capability.go`: `KubernetesRuntime.SupportsDesiredState()` returns `false`, and `KubernetesRuntime.ApplyDesiredState` returns `ErrDesiredStateNotSupported` instead of rendering, applying, or falling back to the legacy deploy path.
- `internal/adapters/runtime/resolver.go`: `ResolveDesiredStateApplier` resolves through the normal runtime factory/resolver path, then rejects any adapter that does not support desired-state convergence with an error that includes the runtime type.
- `internal/adapters/runtime/desired_state_capability_test.go::TestUnsupportedRuntimesReturnExplicitError` proves Kubernetes desired-state apply returns a nil result plus `ErrDesiredStateNotSupported`.
- `internal/adapters/runtime/resolver_desired_state_test.go::TestResolveDesiredStateApplier_KubernetesExplicitError` proves Kubernetes desired-state capability resolution fails explicitly and names the `kubernetes` runtime type.
- `internal/domain/runtime_desired_state.go`: `KubernetesExtension` is documented as a reserved typed namespace only; it is not treated as a Kubernetes desired-state implementation.
- Follow-up implementation work is tracked in Beads as `bahia-amqy` (Kubernetes desired-state renderer/adapter slice), referencing the desired-state domain contract and runtime adapter capability seam.

## 2026-06-07 bahia-zu2p.8.9 Evidence

- `internal/service/runtime_lifecycle_test.go::TestRuntimeLifecycleDesiredStateDeployHydratesLegacySiblingsAndRecordsDrift` seeds a target service, a sibling with stored desired state, and a legacy sibling with only persisted service/runtime/artifact state. The lifecycle deploy path sends all three services to the desired-state apply seam, proving the first Compose desired-state plan does not omit existing managed siblings.
- The same test asserts the legacy sibling spec is reconstructed from persisted registry/runtime/artifact state and written back to `EnvironmentServiceState.DesiredRuntimeState`/`DesiredHash` opportunistically.
- The mock desired-state runtime reports a normalized observed hash matching the target desired hash; `RegistryService.RecordObservation` then records the post-apply observation, sets `CurrentObservationID`, updates `LastReconciledAt`, and advances target drift to `in_sync`.
- Focused runtime adapter tests continue to prove Compose full-project apply/render behavior covers multi-service plans without service-scoped `up`, `<SERVICE>_IMAGE`, or `--force-recreate` patterns.

## 2026-06-07 bahia-zu2p.8.10 Evidence

- `internal/controlplane/reactor.go::handleRollbackRequest` now creates the rollback intent, builds or reuses the selected artifact's desired snapshot/hash, persists any fallback-built desired snapshot/hash before run creation, creates a deployment run with apply metadata, executes the selected artifact via `RuntimeLifecycleService.DeployWithStatus`, completes the run, and publishes progression plus terminal ContextVM result metadata.
- `internal/service/registry.go` and `internal/repository/pg_deployment.go` preserve desired runtime state/hash when deployment intents and completed runs update `EnvironmentServiceState`; rollback intents inherit desired snapshot/hash from the selected previously deployed intent when available, and fallback-built rollback snapshots are persisted to the existing rollback intent before completion reloads it.
- `internal/controlplane/operator_actions.go` and `internal/api/dto/responses.go` enrich direct runtime deploy terminal results with `desired_hash` from persisted environment service state when available; restart/stop action results deliberately do not carry desired-state metadata.
- `internal/controlplane/reactor_policy_gate_test.go::TestHandleRollbackRequestExecutesSharedDesiredStateDeployPath` proves rollback-to-artifact uses the shared desired-state helper, emits status progression, persists rollback intent/run desired metadata, and publishes terminal result metadata.
- `internal/controlplane/reactor_policy_gate_test.go::TestHandleRollbackRequestPersistsFallbackDesiredStateBeforeCompletingRun` proves rollback fallback snapshots are persisted before run completion so environment service state keeps desired runtime metadata even when the previous deployed intent lacked stored desired state.
- `internal/controlplane/reactor_policy_gate_test.go::TestHandleRollbackRequestRecordsFailedRunWhenDesiredStateBuildFails` and `TestHandleRollbackRequestRecordsFailedRunWhenDeployFails` prove rollback failure paths record terminal failed deployment runs instead of leaving rollback state approved/deploying.
- `internal/controlplane/reactor_policy_gate_test.go::TestDirectRuntimeDeployResultCarriesDesiredHashFromState` proves direct runtime deploy terminal results expose the persisted desired hash.
- `internal/controlplane/reactor_policy_gate_test.go::TestDirectRuntimeRestartResultDoesNotCarryDesiredHashFromState` proves non-deploy direct runtime actions do not leak desired hashes from existing state.
- `internal/controlplane/reactor_policy_gate_test.go::TestHandleDeployRequestInvokesRuntimeLifecycleAndPersistsDesiredState` continues to prove deploy request desired snapshot/hash and run apply metadata persistence.
- `internal/adapters/runtime/compose_desired_state_test.go` and `internal/adapters/runtime/docker_apply_test.go` remain the adapter evidence that Compose/Docker desired-state paths do not use legacy Compose image substitution for desired-state-managed deploys.

## 2026-06-07 bahia-zu2p.9.1 Evidence

- `docs/plans/desired-state-runtime-architecture-2026-05-26.md` now states that Compose phase 1 has no dependency on per-service fragments: the canonical operational output remains `docker-compose.yml`, `.bahia/env/<service-key>.env`, and `.bahia/render-state.json` rendered by the final `ComposeRenderer` / `ComposeDesiredStateApplier` shape and applied with full-project `up -d --remove-orphans`.
- The same plan records the fragment implementation design checklist that must be handled in Beads before service-scoped Compose apply is introduced: fragment layout/merge order, dependency and sibling-hydration eligibility, project-wide network/volume declaration safety, explicit project naming, and operator visibility for generated artifacts and diagnostics.
- `pstf/features/DESIRED_STATE_RUNTIME/acceptance_criteria.md` now makes fragment independence a DSR-AC-008 expected result and negative case.
- `pstf/features/DESIRED_STATE_RUNTIME/test_matrix.md` marks DSR-T-012 implemented against `internal/adapters/runtime/compose_desired_state_test.go` and `internal/adapters/runtime/compose_safety_test.go`.
- Focused test evidence shows the phase-1 desired-state Compose path uses the rendered full project without service-scoped `up`, `<SERVICE>_IMAGE` substitution, or `--force-recreate`; fragment implementation remains tracked separately in Beads.

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
| DSR-AC-006 | Verified | DSR-T-021 covers lifecycle first-deploy plan inclusion and opportunistic persistence for stored and legacy siblings; lower-level assembler test DSR-T-008 remains planned. |
| DSR-AC-007 | Verified | DSR-T-009 covers desired-state adapter capability wiring and proves Kubernetes desired-state apply/capability resolution fails explicitly with `ErrDesiredStateNotSupported` instead of falling back or pretending support. Future Kubernetes implementation work is tracked in `bahia-amqy`. |
| DSR-AC-008 | Partially verified | DSR-T-012 proves phase-1 desired-state Compose apply is full-project/unit-owned and does not use service-scoped `up`, `<SERVICE>_IMAGE`, `--force-recreate`, or fragment-dependent apply behavior. Other renderer/staging checks remain under DSR-WI-05. |
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
- Passing: 6
- Failing: 0
- Not implemented: 17
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
