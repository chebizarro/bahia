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
**Status:** superseded

**Context Summary:**
The implementation and focused tests were verified, but the slice still had one governance question: whether operator-only signer-first slices must include a stored local rehearsal or staged/live signoff artifact in addition to deterministic package-test evidence and rollout docs.

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
- Feature Spec: updated later — this temporary deferral was resolved by HITL-SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME-004
- Acceptance Criteria: none
- Tests: none
- Defects: none
- Confidence / Release: adjusted at the time — final policy now requires a stored rehearsal artifact before release approval

**Required Follow-Up Actions:**
- [x] Create a follow-up issue for rehearsal-gate policy (`bahia-rn20`).
- [x] Resolve whether rehearsal artifacts become a formal acceptance/release gate for future operator slices.

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

### Decision HITL-SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME-004 — Rehearsal artifact gate formalization

**Stage:** human_review
**Agent:** HITL Review Agent
**Decision Type:** risk_acceptance
**Status:** active

**Context Summary:**
The slice was already approved with risk while rehearsal-gate policy was deferred. The remaining decision was whether a stored local Docker+relay rehearsal artifact is optional, required for PSTF verification, or required before release approval while keeping staged/live signoff as the production gate.

**Question Asked:**
For operator-only signer-first PSTF slices, how should local Docker+relay rehearsal evidence be treated?

**Options Presented:**
- A) Required before release approval; staged/live signoff still required for production enablement
- B) Optional supporting evidence only; deterministic tests and staged/live signoff are sufficient
- C) Required for PSTF slice verification itself

**User Selection:**
Required before release approval; staged/live signoff still required for production enablement

**User Notes:**
None.

**Decision:**
REQUIRED_BEFORE_RELEASE_APPROVAL

**Impact:**
- Feature Spec: updated — release-gate ambiguity removed and replaced with explicit policy
- Acceptance Criteria: updated — rollout evidence policy synchronized in coverage notes
- Tests: none
- Defects: none
- Confidence / Release: adjusted — slice remains implementation-verified, but release approval now requires a stored rehearsal artifact and production enablement still requires staged/live signoff

**Required Follow-Up Actions:**
- [x] Update rollout docs to require a stored local Docker+relay rehearsal artifact before release approval.
- [x] Update PSTF slice artifacts to remove the deferred policy ambiguity.
- [x] Capture a rehearsal artifact bundle for the release candidate before release approval (`bahia-noxg`): `docs/investigations/signer-first-operator-rehearsal-2026-05-04/` (executed 2026-05-04 UTC).

## Summary

- Final Status: APPROVED_WITH_RISK
- Open Questions: 0
- Accepted Risks: 0
- Deferred Items: 0
- Blocking Issues: 0 — local rehearsal artifact captured; staged/live signer-first signoff remains the remaining production enablement gate
- Superseded Decisions: 1 — HITL-SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME-002 superseded by HITL-SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME-004

## Traceability

| Decision ID | Affects ACs | Affects Tests | Affects Defects | Affects Artifacts | Notes |
| --- | --- | --- | --- | --- | --- |
| HITL-SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME-001 | SFOAR-AC-001..011 | none | none | acceptance_criteria.json, hitl_decisions.md | Slice approved with explicit deferred process follow-up. |
| HITL-SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME-002 | none | none | none | feature_spec.json, verification_report.md, hitl_decisions.md | Temporary deferral later superseded by HITL-SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME-004. |
| HITL-SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME-003 | SFOAR-AC-001..011 | none | none | acceptance_criteria.json, hitl_decisions.md | Acceptance criteria approved as written. |
| HITL-SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME-004 | none | none | none | feature_spec.json, acceptance_criteria.json, verification_report.md, hitl_decisions.md, rollout docs | Local rehearsal artifact is required before release approval; evidence captured at `docs/investigations/signer-first-operator-rehearsal-2026-05-04/`; staged/live signoff remains the production gate. |
