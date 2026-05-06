# HITL Decisions — CORE_SERVICE_TO_DEPLOYMENT

## Metadata
- Feature ID: CORE_SERVICE_TO_DEPLOYMENT
- Task ID: bahia-xl4y
- Framework: PSTF
- Interaction Mode: RepoPrompt ask-user tool
- Current Stage: spec_reconstruction
- Last Updated: 2026-05-05

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
- [ ] Create or update implementation work to make `/services` show the created service immediately after successful signer-first create.
- [ ] Add or update tests that prove immediate list hydration in the same browser session.
- [ ] Synchronize acceptance and verification artifacts with this approved decision.

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
- [ ] Create or update implementation work so deployment intent creation is blocked until policy preview succeeds and reports no blockers.
- [ ] Update modal copy and error handling to match the required-gate behavior.
- [ ] Add or update tests covering preview failure, preview blockers, and successful gated creation.
- [ ] Synchronize acceptance and verification artifacts with this approved decision.

---

## Summary

- Final Status: decisions_recorded_followup_required
- Open Questions: 0
- Accepted Risks: 0
- Deferred Items: 0
- Blocking Issues: 2
- Superseded Decisions: 0

## Traceability

| Decision ID | Affects ACs | Affects Tests | Affects Defects | Affects Artifacts | Notes |
| --- | --- | --- | --- | --- | --- |
| HITL-CORE_SERVICE_TO_DEPLOYMENT-001 | current service-create ACs need revision | service-create unit/E2E coverage | likely implementation follow-up for `/services` hydration | feature_spec.json, acceptance_criteria.json, test_matrix.json, verification_report.md, hitl_decisions.md | Human classified eventual-only hydration as insufficient and required immediate visible hydration. |
| HITL-CORE_SERVICE_TO_DEPLOYMENT-002 | current policy-preview ACs need revision | policy-preview and deploy-modal coverage | current preview UX/logic now below approved intent | feature_spec.json, acceptance_criteria.json, test_matrix.json, verification_report.md, hitl_decisions.md | Human classified policy preview as a required gate rather than advisory UX. |
