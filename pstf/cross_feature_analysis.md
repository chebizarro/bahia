# Cross-Feature Analysis

Date: 2026-05-04
Agent: CrossFeatureReasoningAgent

## Recommendation

`request_hitl_and_expand_cross_feature_coverage_before_broad_release_planning`

## Overall Risk

`high`

The current PSTF portfolio has several strong, verified slices, but they still depend on shared contracts and shared policies that are not yet verified once at the system level.

The most important gap is not another isolated feature bug. It is that Bahia now has multiple approved relay-backed feature families sharing:
- one discovery contract (`Nostr discovery events (kind 31974 + NIP-51 kind 30002)`),
- one mixed transport model (public signer-first, encrypted request/result, compatibility-only fallback), and
- overlapping deployment-domain semantics (especially rollback and deployment-detail/log handoff).

Those shared contracts are real product behavior now. They should be treated as first-class release-planning inputs rather than implicit assumptions inherited from separate slices.

## Features Considered

Completed or approved slices:
- `CORE_SERVICE_TO_DEPLOYMENT`
- `ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS`
- `SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME`
- `LLM_ROUTE_RELEASE_DEPLOYMENT` (approved non-rollback slice)

Promoted / ranked adjacent slices considered for system reasoning:
- `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP`
- `HIVECI_RESULT_INGESTION_PIPELINE`
- `SOUL_FACTORY_PROVISIONING_TRACKING`
- `ENCRYPTED_DEPLOYMENT_RUN_LOGS`
- `ENCRYPTED_SERVICE_SECRETS_CRUD_REVEAL`
- `ORG_MEMBERSHIP_INVITES_ENCRYPTED_ADMIN`
- `PAYMENTS_HISTORY_FILTER_EXPORT`

## Feature Interaction Graph

### Shared dependency / gating edges
- `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` **gates** `CORE_SERVICE_TO_DEPLOYMENT`
  - Evidence: `CORE_SERVICE_TO_DEPLOYMENT/feature_spec.json` requires `Nostr discovery events (kind 31974 + NIP-51 kind 30002)` to advertise relay-read-model capability, public relay URLs, and a service pubkey.
- `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` **gates** `ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS`
  - Evidence: `ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS/feature_spec.json` requires `nostr.service_pubkey` and `nostr.browser_encrypted_request_relays` from `Nostr discovery events (kind 31974 + NIP-51 kind 30002)`.
- `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` **gates** `LLM_ROUTE_RELEASE_DEPLOYMENT`
  - Evidence: `LLM_ROUTE_RELEASE_DEPLOYMENT/feature_spec.json` depends on LLM kind advertisement from `Nostr discovery events (kind 31974 + NIP-51 kind 30002)`.
- `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` **produces_for** `SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME`
  - Evidence: `cmd/cli/operator_nostr.go` falls back to `BrowserRelays` and `SidecarURL` from `Nostr discovery events (kind 31974 + NIP-51 kind 30002)`.

### Shared state / user-journey edges
- `CORE_SERVICE_TO_DEPLOYMENT` **shares_state_with** `LLM_ROUTE_RELEASE_DEPLOYMENT`
  - Evidence: both consume `web/src/lib/stores/controlplane.svelte.js`, which holds deployment and LLM route/read-model state.
- `CORE_SERVICE_TO_DEPLOYMENT` **depends_on** `ENCRYPTED_DEPLOYMENT_RUN_LOGS`
  - Evidence: the core slice explicitly excludes encrypted run-log retrieval, but `/deployments/runs/[id]` is still part of the same user journey.
- `ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS` **shares_state_with** `ENCRYPTED_DEPLOYMENT_RUN_LOGS`
  - Evidence: both use the encrypted browser request/result transport family.

### Pattern / overlap edges
- `CORE_SERVICE_TO_DEPLOYMENT` **overlaps_with** `SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME`
  - Evidence: both enforce public signer-first request/result behavior and explicit pre/post-acceptance semantics.
- `ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS` **gates** `ENCRYPTED_SERVICE_SECRETS_CRUD_REVEAL`, `ORG_MEMBERSHIP_INVITES_ENCRYPTED_ADMIN`, and `PAYMENTS_HISTORY_FILTER_EXPORT`
  - Evidence: notifications is already the verified reference slice for the encrypted transport family.
- `HIVECI_RESULT_INGESTION_PIPELINE` **produces_for** `CORE_SERVICE_TO_DEPLOYMENT`
  - Evidence: the backlog describes Hive-CI as the CI/OCI bridge into artifacts/build state that the deployment slice consumes.

### Conflict / policy edges
- `SOUL_FACTORY_PROVISIONING_TRACKING` **conflicts_with** repo event-driven guardrails used by signer-first flows
  - Evidence: `pstf/spec_gap_report.md` says soul provisioning still uses timeout-based failure behavior, which conflicts with the repo’s event-driven Nostr guidance.

## Findings

### XFA-001 — System discovery and relay bootstrap remain an unverified shared contract
- Severity: `major`
- Category: `spec_gap`
- Status: proven
- Affected features:
  - `CORE_SERVICE_TO_DEPLOYMENT`
  - `ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS`
  - `SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME`
  - `LLM_ROUTE_RELEASE_DEPLOYMENT`
  - `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP`
- Evidence:
  - `pstf/spec_gap_report.md` identifies `Nostr discovery events (kind 31974 + NIP-51 kind 30002)` and relay-read-model-first behavior as high-risk missing specification areas.
  - `pstf/feature_backlog.md` ranks `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` first because every relay-backed browser flow depends on it.
  - `internal/adapters/nostr/projector.go` emits `browser_relays`, `browser_encrypted_request_relays`, `sidecar_url`, `service_pubkey`, and control-plane kind advertisement from one payload.
  - All four completed/approved slices consume that contract directly or indirectly.
- Why this matters:
  - Release planning currently assumes one discovery contract is stable across browser public flows, browser encrypted flows, and operator CLI fallback discovery.
  - That assumption is not yet owned by a completed PSTF slice.
- Recommended actions:
  - `create_new_feature_slice` → `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP`
  - `add_cross_feature_test` → prove one `Nostr discovery events (kind 31974 + NIP-51 kind 30002)` payload satisfies all major consumers

### XFA-002 — No cross-feature browser test proves mixed public and encrypted flow separation in one session
- Severity: `major`
- Category: `test_gap`
- Status: proven
- Affected features:
  - `CORE_SERVICE_TO_DEPLOYMENT`
  - `ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS`
  - `LLM_ROUTE_RELEASE_DEPLOYMENT`
  - `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP`
- Evidence:
  - Core public browser proof exists in `service-deployment-public-smoke.spec.js`.
  - Encrypted browser proof exists in `notifications-encrypted-smoke.spec.js` and `notifications-form-error.spec.js`.
  - LLM browser proof exists in `llm-route-release-deployment.spec.js`.
  - No verified artifact shows one authenticated browser session executing both public and encrypted journeys from the same discovery payload while asserting relay separation.
  - `pstf/spec_gap_report.md` calls the mixed transport model high-risk.
- Why this matters:
  - Per-slice success can still hide a cross-slice regression where the wrong relay set is used once multiple transport families coexist in the same session.
- Recommended actions:
  - `add_cross_feature_test` → `web/tests/e2e/mixed-controlplane-transport.spec.js`
  - `update_existing_acceptance_criteria` → add explicit cross-slice boundary notes to the public and encrypted slices

### XFA-003 — Transport policy is fragmented across slices instead of specified once
- Severity: `major`
- Category: `spec_gap`
- Status: proven
- Affected features:
  - `CORE_SERVICE_TO_DEPLOYMENT`
  - `ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS`
  - `SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME`
  - `LLM_ROUTE_RELEASE_DEPLOYMENT`
  - future encrypted slices
- Evidence:
  - `pstf/spec_gap_report.md` describes the mixed transport model as high-risk and public-vs-encrypted separation as security-critical.
  - `web/src/lib/auth/route-access.js` has compatibility hooks, but `ROUTE_COMPATIBILITY_REQUIREMENTS` is empty.
  - Core explicitly excludes encrypted transport for the approved deployment journey.
  - Notifications explicitly requires encrypted request relays and forbids using public relay URLs for encrypted requests.
  - Operator signer-first allows explicit explicit relay configuration only for pre-acceptance failures.
  - LLM HITL decisions remove operational REST mutations from the intended product contract.
- Why this matters:
  - The portfolio has transport rules, but no single approved source of truth for those rules.
  - That will make future encrypted slices and compatibility work harder to govern consistently.
- Recommended actions:
  - `request_hitl_decision`
  - optionally `create_new_feature_slice` for a shared transport-policy contract
- Proposed HITL question:
  - How should transport policy be governed across public browser flows, encrypted browser flows, and compatibility-only operator/browser fallback?
  - Options:
    - create one dedicated shared transport-policy slice now
    - keep policy embedded per feature slice
    - standardize browser transport now, keep operator fallback feature-specific
    - defer and accept current risk for this release

### XFA-004 — Deployment run logs are a broken handoff between public deployment and encrypted transport
- Severity: `major`
- Category: `correctness`
- Status: proven
- Affected features:
  - `CORE_SERVICE_TO_DEPLOYMENT`
  - `ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS`
  - `ENCRYPTED_DEPLOYMENT_RUN_LOGS`
- Evidence:
  - `CORE_SERVICE_TO_DEPLOYMENT/feature_spec.json` explicitly excludes encrypted run-log retrieval on `/deployments/runs/[id]`.
  - `CORE_SERVICE_TO_DEPLOYMENT/verification_report.md` explicitly recommends adding signer-first replacements for deployment run-log/detail browser coverage.
  - `pstf/feature_backlog.md` ranks `ENCRYPTED_DEPLOYMENT_RUN_LOGS` as a partially mapped candidate.
  - `internal/controlplane/encrypted_route_handlers.go` hosts encrypted route handlers for deployment run-log retrieval.
- Why this matters:
  - The deployment journey is only partially verified at the system level. The run-log path is user-visible and transport-sensitive.
- Recommended actions:
  - `create_new_feature_slice` → `ENCRYPTED_DEPLOYMENT_RUN_LOGS`
  - `add_cross_feature_test` → deployment history/detail to encrypted run-log retrieval in one deterministic browser scenario

### XFA-005 — Rollback policy is not standardized across deployment domains
- Severity: `major`
- Category: `spec_gap`
- Status: proven
- Affected features:
  - `CORE_SERVICE_TO_DEPLOYMENT`
  - `LLM_ROUTE_RELEASE_DEPLOYMENT`
- Evidence:
  - `LLM_ROUTE_RELEASE_DEPLOYMENT/hitl_decisions.md` defers all rollback acceptance criteria until replacement semantics are approved.
  - `LLM_ROUTE_RELEASE_DEPLOYMENT/feature_spec.json` keeps rollback in scope but explicitly classifies the current target-selection rule as FIX, not intended behavior.
  - `CORE_SERVICE_TO_DEPLOYMENT/verification_report.md` recommends future signer-first rollback coverage.
- Why this matters:
  - Rollback is not just a single-feature issue anymore. It is now a cross-domain policy gap in the deployment family.
- Recommended actions:
  - `request_hitl_decision`
  - then either `create_new_feature_slice` for shared rollback policy or separate domain rollback follow-ups
- Proposed HITL question:
  - How should rollback target-selection and user-visible semantics be handled across core service deployments and LLM deployments?
  - Options:
    - standardize one rollback policy across both domains
    - keep rollback domain-specific
    - defer and create a dedicated rollback-policy slice
    - exclude rollback from the current release train until approved

### XFA-006 — The promoted Hive-CI slice produces deployment inputs, but the handoff is still not end-to-end proven
- Severity: `major`
- Category: `test_gap`
- Status: proven
- Affected features:
  - `HIVECI_RESULT_INGESTION_PIPELINE`
  - `CORE_SERVICE_TO_DEPLOYMENT`
- Evidence:
  - `pstf/spec_gap_report.md` says the selected CI-to-registry-to-artifact integration file is mostly scaffold and does not prove the flow end to end.
  - `pstf/backlog_decisions.md` records `HIVECI_RESULT_INGESTION_PIPELINE` as a promoted next slice.
  - `pstf/feature_backlog.md` describes Hive-CI as the CI/OCI bridge into the build-artifact pipeline.
- Why this matters:
  - Deployment-facing slices assume artifact/build availability, but the upstream producer path remains under-proven.
- Recommended actions:
  - `create_new_feature_slice` → `HIVECI_RESULT_INGESTION_PIPELINE`
  - `add_cross_feature_test` → CI ingestion to artifact/build state to deploy intent flow

### XFA-007 — PSTF artifact refresh sequencing is inconsistent
- Severity: `minor`
- Category: `operability`
- Status: proven
- Affected features:
  - `LLM_ROUTE_RELEASE_DEPLOYMENT`
- Evidence:
  - `pstf/features/LLM_ROUTE_RELEASE_DEPLOYMENT/adversarial_review.json` still says coverage artifacts do not exist.
  - `pstf/features/LLM_ROUTE_RELEASE_DEPLOYMENT/confidence_report.json` now records coverage artifacts and a 0.93 approved confidence score.
- Why this matters:
  - Portfolio-level reporting can drift unless the artifact refresh order is treated as a process rule.
- Recommended actions:
  - `defer_with_known_risk` or document PSTF artifact refresh order explicitly

## Missing Shared Policies

1. **System discovery contract governance**
   - One shared discovery payload currently serves browser public bootstrap, browser encrypted gating, and operator CLI relay discovery.
   - No completed slice owns this contract directly.

2. **Transport boundary governance**
   - Public signer-first, encrypted request/result, and compatibility fallback rules are real product behavior.
   - They are not yet governed once as a portfolio rule.

3. **Rollback semantics governance**
   - Rollback has become a portfolio concern across deployment-oriented slices.
   - Current treatment is inconsistent or deferred.

## Candidate Cross-Feature Tests

### XFT-001 — Single-session mixed transport journey
- Type: `e2e`
- Covers:
  - `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP`
  - `CORE_SERVICE_TO_DEPLOYMENT`
  - `ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS`
  - `LLM_ROUTE_RELEASE_DEPLOYMENT`
- Purpose:
  - Prove one authenticated browser session can load system discovery, execute one public signer-first action, execute one encrypted notification action, visit `/llm`, and preserve relay separation throughout.

### XFT-002 — Nostr discovery multi-consumer contract
- Type: `integration`
- Covers:
  - `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP`
  - `ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS`
  - `SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME`
  - `LLM_ROUTE_RELEASE_DEPLOYMENT`
- Purpose:
  - Prove one canonical `Nostr discovery events (kind 31974 + NIP-51 kind 30002)` fixture satisfies browser public bootstrap, browser encrypted transport gating, LLM kind discovery, and CLI operator relay discovery.

### XFT-003 — Deployment history to encrypted run-log handoff
- Type: `e2e`
- Covers:
  - `CORE_SERVICE_TO_DEPLOYMENT`
  - `ENCRYPTED_DEPLOYMENT_RUN_LOGS`
- Purpose:
  - Prove the user can move from public deployment history/detail into encrypted run-log retrieval without a contradictory transport handoff.

### XFT-004 — Hive-CI ingestion to artifact-backed deploy
- Type: `integration`
- Covers:
  - `HIVECI_RESULT_INGESTION_PIPELINE`
  - `CORE_SERVICE_TO_DEPLOYMENT`
- Purpose:
  - Prove CI result ingestion produces or enriches the artifact/build state used by deployment flows.

### XFT-005 — Public signer-first requester policy consistency
- Type: `contract`
- Covers:
  - `CORE_SERVICE_TO_DEPLOYMENT`
  - `SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME`
  - `LLM_ROUTE_RELEASE_DEPLOYMENT`
- Purpose:
  - Prove public signer-first request families consistently subscribe before publish, require accepted relay OK responses, preserve correlation tags, and treat post-acceptance failures as terminal rather than fallback-eligible.

## HITL

No new HITL decision was captured in this analysis artifact.

The two cross-feature decisions that most directly affect the next work are:
1. shared transport-policy governance
2. shared vs domain-specific rollback governance

Those questions are proposed above because they change whether the next step is:
- one shared policy slice,
- updates across existing acceptance criteria,
- or multiple domain-specific follow-up slices.

## Next Recommended Stage

1. Capture HITL decisions for:
   - shared transport policy
   - rollback policy across deployment domains
2. Promote and complete:
   - `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP`
   - `ENCRYPTED_DEPLOYMENT_RUN_LOGS`
3. Add the first mandatory cross-feature tests:
   - `XFT-001` mixed-transport browser journey
   - `XFT-002` Nostr discovery multi-consumer contract
4. Complete the already promoted upstream producer slice:
   - `HIVECI_RESULT_INGESTION_PIPELINE`

## Bottom Line

The portfolio is no longer missing only isolated feature work. It is now missing explicit ownership of shared contracts.

The release-planning risk is concentrated in:
- shared discovery,
- shared transport governance,
- deployment-detail/log handoff,
- and rollback semantics.

Those are the next system-level constraints to close.
