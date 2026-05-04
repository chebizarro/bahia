# HITL Decisions — SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME

## Metadata
- Feature ID: SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME
- Task ID: bahia-lusx
- Framework: PSTF
- Interaction Mode: RepoPrompt ask-user tool
- Current Stage: human_review
- Last Updated: 2026-05-04

---

## Decision Ledger

### Decision HITL-SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME-001 — Slice approval and release posture

**Stage:** human_review
**Agent:** HITL Review Agent
**Decision Type:** release_approval
**Status:** active

**Context Summary:**
The signer-first operator slice has passing focused verification across the reactor, operator client, CLI, and handler layers. No open implementation defects were found in the current slice scope. The remaining question was whether to approve the slice for PSTF purposes and what risk posture to record.

**Question Asked:**
What is the approval decision for SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME?

**Options Presented:**
- A) APPROVED
- B) APPROVED_WITH_RISK
- C) NEEDS_WORK
- D) DEFERRED
- E) REJECTED

**User Selection:**
APPROVED_WITH_RISK

**User Notes:**
None.

**Decision:**
APPROVED_WITH_RISK

**Impact:**
- Feature Spec: none
- Acceptance Criteria: updated — promoted from `draft` to `approved`
- Tests: none
- Defects: none
- Confidence / Release: adjusted — slice is accepted, but rehearsal-gate policy remains deferred to follow-up

**Required Follow-Up Actions:**
- [x] Promote `pstf/features/SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME/acceptance_criteria.json` from `draft` to `approved`.
- [ ] Track the deferred rehearsal-gate decision separately.

---

### Decision HITL-SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME-002 — Rehearsal artifact gate posture

**Stage:** human_review
**Agent:** HITL Review Agent
**Decision Type:** risk_acceptance
**Status:** active

**Context Summary:**
The implementation and focused tests are verified, but the slice still had one governance question: whether operator-only signer-first slices must include a stored local rehearsal or staged/live signoff artifact in addition to deterministic package-test evidence and rollout docs.

**Question Asked:**
How should rehearsal evidence be treated for this operator slice?

**Options Presented:**
- A) DEFER_TO_FOLLOWUP
- B) REQUIRE_AS_GATE
- C) NOT_REQUIRED

**User Selection:**
DEFER_TO_FOLLOWUP

**User Notes:**
None.

**Decision:**
DEFER_TO_FOLLOWUP

**Impact:**
- Feature Spec: update needed — keep the governance ambiguity explicit only as a deferred process question
- Acceptance Criteria: none
- Tests: none
- Defects: none
- Confidence / Release: adjusted — does not block current slice approval, but requires tracked process follow-up

**Required Follow-Up Actions:**
- [x] Create a follow-up issue for rehearsal-gate policy (`bahia-rn20`).
- [ ] Resolve whether rehearsal artifacts become a formal acceptance/release gate for future operator slices.

---

### Decision HITL-SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME-003 — Acceptance criteria approval

**Stage:** human_review
**Agent:** HITL Review Agent
**Decision Type:** spec_approval
**Status:** active

**Context Summary:**
The slice was approved with risk, but the acceptance criteria artifact still needed explicit human approval before the PSTF state could be synchronized.

**Question Asked:**
Do you approve the acceptance criteria for SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME as written?

**Options Presented:**
- A) APPROVE_AS_IS
- B) APPROVE_WITH_EDITS
- C) REJECT_AND_REVISE
- D) DEFER_FEATURE

**User Selection:**
APPROVE_AS_IS

**User Notes:**
None.

**Decision:**
APPROVE_AS_IS

**Impact:**
- Feature Spec: none
- Acceptance Criteria: updated — artifact status is now `approved`
- Tests: none
- Defects: none
- Confidence / Release: enables clean synchronization of the PSTF slice artifacts

**Required Follow-Up Actions:**
- [x] Update `acceptance_criteria.json` status to `approved`.

---

## Summary

- Final Status: APPROVED_WITH_RISK
- Open Questions: 0
- Accepted Risks: 0
- Deferred Items: 1 — rehearsal artifact gate policy tracked in `bahia-rn20`
- Blocking Issues: 0
- Superseded Decisions: 0

## Traceability

| Decision ID | Affects ACs | Affects Tests | Affects Defects | Affects Artifacts | Notes |
| --- | --- | --- | --- | --- | --- |
| HITL-SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME-001 | SFOAR-AC-001..011 | none | none | acceptance_criteria.json, hitl_decisions.md | Slice approved with explicit deferred process follow-up. |
| HITL-SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME-002 | none | none | none | feature_spec.json, verification_report.md, hitl_decisions.md | Rehearsal-gate policy deferred to follow-up bead `bahia-rn20`. |
| HITL-SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME-003 | SFOAR-AC-001..011 | none | none | acceptance_criteria.json, hitl_decisions.md | Acceptance criteria approved as written. |
