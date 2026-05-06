# HITL Decisions — CORE_SERVICE_TO_DEPLOYMENT

## Metadata
- Feature ID: CORE_SERVICE_TO_DEPLOYMENT
- Task ID: bahia-xl4y
- Framework: PSTF
- Interaction Mode: RepoPrompt ask-user tool
- Current Stage: hitl_risk_review
- Last Updated: 2026-05-06

---

## Decision Ledger

### Decision HITL-CORE_SERVICE_TO_DEPLOYMENT-001 — Post-create service list hydration classification

**Stage:** spec_reconstruction
**Agent:** PSTF Spec Reconstruction Agent
**Decision Type:** legacy_behavior_classification
**Status:** active

**Context Summary:**
The current `/services` flow publishes signer-first service-create kind `5964`, awaits a correlated `7963` result, closes the modal, and then calls `loadServices()`. However, `loadServices()` only reuses relay bootstrap and does not itself prove a fresh service-registry projection will arrive immediately. Existing tests prove command success and relay-backed eventual state, but they do not prove immediate list hydration after create.

**Question Asked:**
How should we classify post-create service list hydration for this feature slice?

**Options Presented:**
- KEEP current eventual-consistency behavior (7963 success is enough; list may update when the relay projection arrives)
- FIX it into an explicit requirement that `/services` must show the new service immediately after create succeeds
- DEFER the decision and leave it out of the approved intended behavior for now

**User Selection:**
FIX it into an explicit requirement that `/services` must show the new service immediately after create succeeds

**User Notes:**
None.

**Decision:**
FIX

**Impact:**
- Feature Spec: update needed — approved intended behavior now requires immediate visible service hydration after successful create
- Acceptance Criteria: update needed — current slice criteria still treat service-create success without immediate list hydration as sufficient
- Tests: update needed — active unit/E2E coverage does not yet prove immediate hydration
- Defects: likely follow-up implementation defect if current relay projection timing or UI reconciliation cannot satisfy the approved UX promise
- Confidence / Release: adjusted — prior `fully_verified` posture is no longer valid against approved intent

**Required Follow-Up Actions:**
- [x] Create or update implementation work to make `/services` show the created service immediately after successful signer-first create.
- [x] Add or update tests that prove immediate list hydration in the same browser session.
- [x] Synchronize acceptance and verification artifacts with this approved decision.

---

### Decision HITL-CORE_SERVICE_TO_DEPLOYMENT-002 — Deployment policy preview classification

**Stage:** spec_reconstruction
**Agent:** PSTF Spec Reconstruction Agent
**Decision Type:** legacy_behavior_classification
**Status:** active

**Context Summary:**
The current deployment modal requests signer-first policy preview through kind `5989`, but the UI explicitly says preview failure does not block deployment intent creation and backend enforcement remains authoritative. The planning docs confirm the kind family exists, but they do not explicitly decide whether preview is merely advisory UX or a required gate for intent creation.

**Question Asked:**
How should policy preview be classified in the core service-to-deployment slice?

**Options Presented:**
- KEEP it advisory/non-blocking (preview is useful UX, but deployment intent creation must still work if preview is unavailable)
- FIX it into a required gate (preview must succeed and/or block creation when it reports blockers)
- DEFER the decision and do not treat preview behavior as approved product intent yet

**User Selection:**
FIX it into a required gate (preview must succeed and/or block creation when it reports blockers)

**User Notes:**
None.

**Decision:**
FIX

**Impact:**
- Feature Spec: update needed — approved intended behavior now requires successful policy preview before deployment intent creation and blocks preview-reported blockers
- Acceptance Criteria: update needed — current criteria describe policy preview as advisory signer-first behavior rather than a MUST gate
- Tests: update needed — existing unit/E2E coverage traces kind `5989` but does not prove preview-required gating or blocker prevention
- Defects: current route logic and modal copy conflict with the approved intent and should be treated as follow-up implementation defects
- Confidence / Release: adjusted — current verification proves the observed advisory behavior, not the approved gating behavior

**Required Follow-Up Actions:**
- [x] Create or update implementation work so deployment intent creation is blocked until policy preview succeeds and reports no blockers.
- [x] Update modal copy and error handling to match the required-gate behavior.
- [x] Add or update tests covering preview failure, preview blockers, and successful gated creation.
- [x] Synchronize acceptance and verification artifacts with this approved decision.

---

### Decision HITL-CORE_SERVICE_TO_DEPLOYMENT-003 — Canonical backend policy-gate enforcement scope

**Stage:** hitl_risk_review
**Agent:** CriticAgent follow-up implementation pass
**Decision Type:** product_scope_clarification
**Status:** active

**Context Summary:**
The web `/services/[id]` route now blocks deployment intent creation until signer-first policy preview succeeds, but the canonical backend `5961` request path still allowed any authorized signer-first client to create a deployment intent directly without policy evaluation. That left the approved gate bypassable outside the browser modal.

**Question Asked:**
Should deployment policy gating be enforced only in the web `/services/[id]` UX, or must backend `5961` deployment-intent creation reject policy-blocked requests from any authorized signer-first client?

**Options Presented:**
- Enforce policy during backend `5961` intent creation for all signer-first clients
- Keep backend permissive and require the gate only in the web `/services/[id]` UX
- Enforce backend blocking only for `block` policies and treat warnings as UI-only preflight
- Defer policy-gate scope from this release

**User Selection:**
Enforce policy during backend `5961` intent creation for all signer-first clients

**User Notes:**
None.

**Decision:**
FIX

**Impact:**
- Feature Spec: update needed — approved intended behavior now requires canonical backend `5961` handling to fail closed on policy-blocked requests
- Acceptance Criteria: update needed — route-scoped policy-preview wording is insufficient; backend signer-first enforcement must be explicit
- Tests: update needed — add direct canonical request coverage proving blocked artifacts cannot create deployment intents by bypassing the browser modal
- Defects: current backend `5961` handling is below approved intent until policy evaluation is enforced in the reactor
- Confidence / Release: adjusted — prior readiness claims overstated coverage because direct signer-first clients could bypass the policy gate

**Required Follow-Up Actions:**
- [x] Enforce policy evaluation in backend canonical `5961` deployment-intent handling.
- [x] Add contract coverage proving policy-blocked direct signer-first requests fail and do not create intents.
- [x] Synchronize acceptance and verification artifacts with this approved decision.

---

### Decision HITL-CORE_SERVICE_TO_DEPLOYMENT-004 — Post-create visibility behavior under active list state

**Stage:** hitl_risk_review
**Agent:** CriticAgent follow-up implementation pass
**Decision Type:** ux_behavior_clarification
**Status:** active

**Context Summary:**
Immediate service visibility after create was implemented by force-resetting `/services` search, runtime filter, and pagination. That satisfied the original immediate-visibility decision in the default view, but it changed the operator’s current list context without an approved product decision for filtered, searched, or paged states.

**Question Asked:**
When a new service is created while `/services` is filtered, searched, or paged away from the first page, how should immediate visibility work?

**Options Presented:**
- Reset filters and pagination so the new service is always shown immediately
- Preserve current list state and show a success message with a \"view service\" action
- Preserve current list state and only auto-show/highlight the new service when it already matches the current view
- Defer filtered-view behavior from this release

**User Selection:**
Preserve current list state and only auto-show/highlight the new service when it already matches the current view

**User Notes:**
None.

**Decision:**
FIX

**Impact:**
- Feature Spec: update needed — immediate hydration remains required, but list-state preservation is now part of approved intent
- Acceptance Criteria: update needed — current wording overstates unconditional visibility and must be qualified for filtered/search/paged contexts
- Tests: update needed — add browser coverage for preserved search/filter/page state and immediate visibility only when the new service already matches the active view
- Defects: current `/services` create flow is below approved intent until it stops resetting operator list state
- Confidence / Release: adjusted — the existing smoke test covered only the default first-page view and did not settle filtered/paged behavior

**Required Follow-Up Actions:**
- [x] Remove forced `/services` filter and pagination resets after successful create.
- [x] Add browser coverage for preserved search, filter, and pagination state.
- [x] Synchronize acceptance and verification artifacts with this approved decision.

---

## Summary

- Final Status: decisions_recorded_implementation_in_progress
- Open Questions: 0
- Accepted Risks: 0
- Deferred Items: 0
- Blocking Issues: 0
- Superseded Decisions: 0

## Traceability

| Decision ID | Affects ACs | Affects Tests | Affects Defects | Affects Artifacts | Notes |
| --- | --- | --- | --- | --- | --- |
| HITL-CORE_SERVICE_TO_DEPLOYMENT-001 | current service-create ACs need revision | service-create unit/E2E coverage | likely implementation follow-up for `/services` hydration | feature_spec.json, acceptance_criteria.json, test_matrix.json, verification_report.md, hitl_decisions.md | Human classified eventual-only hydration as insufficient and required immediate visible hydration. |
| HITL-CORE_SERVICE_TO_DEPLOYMENT-002 | current policy-preview ACs need revision | policy-preview and deploy-modal coverage | current preview UX/logic now below approved intent | feature_spec.json, acceptance_criteria.json, test_matrix.json, verification_report.md, hitl_decisions.md | Human classified policy preview as a required gate rather than advisory UX. |
| HITL-CORE_SERVICE_TO_DEPLOYMENT-003 | policy-gate ACs and canonical deploy-intent contract need revision | backend deploy-intent contract coverage | direct signer-first `5961` path bypassed approved gate | feature_spec.json, acceptance_criteria.json, test_matrix.json, verification_report.md, hitl_decisions.md | Human required backend policy enforcement for all signer-first `5961` clients, not just browser UX gating. |
| HITL-CORE_SERVICE_TO_DEPLOYMENT-004 | service-create visibility ACs need qualification for filtered/search/paged views | new `/services` context-preservation browser coverage | `/services` create flow reset operator list context without approval | feature_spec.json, acceptance_criteria.json, test_matrix.json, verification_report.md, hitl_decisions.md | Human required preserving current list state and only auto-showing the new service when it already matches the active view. |
