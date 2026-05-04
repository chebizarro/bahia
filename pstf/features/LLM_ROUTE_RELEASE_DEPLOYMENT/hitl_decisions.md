# HITL Decisions — LLM_ROUTE_RELEASE_DEPLOYMENT

## Metadata
- Feature ID: LLM_ROUTE_RELEASE_DEPLOYMENT
- Task ID: bahia-57ek
- Framework: PSTF
- Interaction Mode: RepoPrompt ask-user tool
- Current Stage: human_review
- Last Updated: 2026-05-04T19:46:08Z

---

## Decision Ledger

### Decision HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-001 — Intended user surface classification

**Stage:** human_review
**Agent:** PSTF Spec Reconstruction Agent
**Decision Type:** scope_classification
**Status:** active

**Context Summary:**
Planning docs and code prove the LLM control plane exists across domain models, Nostr command handling, projection, reconcile, and REST surfaces. The current web app only exposes LLM state through the shared control-plane store; no dedicated LLM page or full browser workflow is present under `web/src/routes`. The spec could not safely infer whether that absence is intentional or a product gap.

**Question Asked:**
What is the intended user surface today for `LLM_ROUTE_RELEASE_DEPLOYMENT`?

**Options Presented:**
- A) API/MCP/operator feature; no dedicated web UX is promised yet
- B) Shared web read-model visibility is in scope, but no dedicated LLM page is promised yet
- C) Full end-user web workflow is intended now

**User Selection:**
C

**User Notes:**
None.

**Decision:**
FULL_END_USER_WEB_WORKFLOW_INTENDED_NOW

**Impact:**
- Feature Spec: updated — current absence of a dedicated LLM web route/page is treated as a gap between observed and intended behavior, not as an out-of-scope omission.
- Acceptance Criteria: future criteria must include an end-user web workflow, not just API/MCP/operator flows.
- Tests: future browser coverage should expand beyond shared-store read-model tests.
- Defects: potential product gap to track — intended web workflow is not currently observed in selected route files.
- Confidence / Release: adjusted — observed implementation proves backend/control-plane depth, but intended browser workflow is under-realized.

**Required Follow-Up Actions:**
- [ ] Track the missing dedicated LLM browser workflow as a follow-up PSTF/implementation gap.
- [ ] Ensure future acceptance criteria include explicit browser-visible route/release/deploy/approval/rollback behavior.

---

### Decision HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-002 — Operational REST compatibility contract classification

**Stage:** human_review
**Agent:** PSTF Spec Reconstruction Agent
**Decision Type:** legacy_behavior_classification
**Status:** active

**Context Summary:**
Code mounts `/api/v1/llm/intents`, `/approve`, `/reject`, `/rollback`, `/hosts`, and `/observations` only when `cfg.LLM.AllowOperationalREST = true`, and tests prove they are disabled by default. Docs call the LLM control plane Nostr-first and describe REST as a compatibility surface, but they do not clearly state whether these operational REST mutations remain part of the intended long-term product contract.

**Question Asked:**
How should the operational REST endpoints for intents/approve/reject/rollback/hosts/observations be classified in the reconstructed spec?

**Options Presented:**
- A) KEEP as compatibility/admin surface; Nostr remains primary
- B) FIX docs/spec, but keep behavior
- C) REMOVE/deprecate from intended product contract
- D) DEFER decision

**User Selection:**
C

**User Notes:**
None.

**Decision:**
REMOVE_FROM_INTENDED_PRODUCT_CONTRACT

**Impact:**
- Feature Spec: updated — the intended behavior excludes those operational REST mutation endpoints even though code still mounts them behind a flag.
- Acceptance Criteria: should target canonical Nostr request/result/read-model flows instead of REST mutation parity.
- Tests: current router tests remain valid as observed legacy coverage, but should not be treated as proof of approved intended behavior.
- Defects: legacy/deprecation work is implied because observed code still exposes the flagged REST compatibility surface.
- Confidence / Release: adjusted — observed code and intended contract diverge here.

**Required Follow-Up Actions:**
- [ ] Track deprecation/removal of the flagged operational REST mutation surface from the intended LLM contract.
- [ ] Keep route/release read-model and command behavior distinct from legacy operational REST mutations in future PSTF artifacts.

---

### Decision HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-003 — Rollback target-selection rule classification

**Stage:** human_review
**Agent:** PSTF Spec Reconstruction Agent
**Decision Type:** legacy_behavior_classification
**Status:** active

**Context Summary:**
`internal/service/llm_registry.go` currently implements rollback by scanning recent intents, choosing the first previously deployed release that differs from the current desired release, and setting `SupersedesIntentID` to the latest deployed intent it encounters. This behavior is observable in code but is not explicitly documented as intended product behavior.

**Question Asked:**
How should the current rollback-selection rule be classified for the reconstructed spec?

**Options Presented:**
- A) KEEP
- B) FIX
- C) REMOVE
- D) UNKNOWN
- E) DEFER

**User Selection:**
B

**User Notes:**
None.

**Decision:**
FIX_CURRENT_ROLLBACK_SELECTION_RULE

**Impact:**
- Feature Spec: updated — the current rollback target-selection algorithm is not preserved as normative behavior.
- Acceptance Criteria: rollback remains in-scope, but explicit target-selection and supersedence semantics still need a replacement decision before rollback acceptance criteria can be finalized.
- Tests: current behavior can be documented as observed, but should not be promoted to approval-backed acceptance criteria.
- Defects: a product/implementation follow-up is implied because the existing rollback rule is treated as needing change.
- Confidence / Release: adjusted — rollback is a real feature surface, but its exact semantics are not yet approved.

**Required Follow-Up Actions:**
- [ ] Capture the replacement rollback-selection rule in a future HITL or design decision.
- [ ] Avoid treating the current implicit “previous deployed release” scan as approved product behavior.

---

### Decision HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-004 — Rollback acceptance-criteria scope during semantic deferral

**Stage:** human_review
**Agent:** PSTF Acceptance Criteria Agent
**Decision Type:** scope_classification
**Status:** active

**Context Summary:**
The feature spec keeps rollback in scope, but the current rollback target-selection and supersedence logic was explicitly classified as `FIX`, not approved behavior. The acceptance-criteria artifact needed a decision on whether to encode temporary rollback criteria anyway or defer them until replacement semantics are defined.

**Question Asked:**
How should rollback be represented in the acceptance criteria for `LLM_ROUTE_RELEASE_DEPLOYMENT` right now?

**Options Presented:**
- A) Keep rollback ACs at transport/visibility level only (request, correlation, user-visible action), and defer target-selection semantics until a later decision
- B) Defer all rollback acceptance criteria until the replacement rollback rule is decided
- C) Freeze the current rollback-selection behavior as temporary acceptance criteria anyway

**User Selection:**
B

**User Notes:**
None.

**Decision:**
DEFER_ALL_ROLLBACK_ACCEPTANCE_CRITERIA

**Impact:**
- Feature Spec: none
- Acceptance Criteria: updated — rollback-specific ACs are excluded from this artifact until replacement semantics are approved
- Tests: rollback test generation is blocked on a future semantic decision
- Defects: none
- Confidence / Release: adjusted — the feature slice remains approved for non-rollback criteria, but rollback verification is intentionally deferred

**Required Follow-Up Actions:**
- [ ] Capture the replacement rollback-selection rule before generating rollback-specific tests or final rollback ACs.
- [ ] Re-open the AC artifact to add rollback criteria once the replacement semantics are approved.

---

### Decision HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-005 — Acceptance criteria approval

**Stage:** human_review
**Agent:** PSTF Acceptance Criteria Agent
**Decision Type:** spec_approval
**Status:** active

**Context Summary:**
A draft AC set was prepared for system discovery, signer-first route/release/deploy/approval flows, route-state projection, browser-visible LLM state, the required end-user web workflow, and exclusion of the deprecated operational REST mutation surface. Rollback ACs were removed from the draft pending a replacement rollback rule.

**Question Asked:**
Do you approve the acceptance criteria for `LLM_ROUTE_RELEASE_DEPLOYMENT` on that basis?

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
- Acceptance Criteria: updated — artifact status promoted to `approved`
- Tests: non-rollback test design may proceed from the approved AC set
- Defects: none
- Confidence / Release: adjusted — ACs are approved for the scoped non-rollback behavior, with rollback intentionally deferred

**Required Follow-Up Actions:**
- [x] Write `pstf/features/LLM_ROUTE_RELEASE_DEPLOYMENT/acceptance_criteria.json` with the approved AC set.
- [ ] Add rollback ACs in a later revision after the replacement rollback rule is decided.

---

### Decision HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-006 — Initial final release selection

**Stage:** human_review
**Agent:** HITL Review Agent
**Decision Type:** release_approval
**Status:** superseded

**Context Summary:**
Verification at the current gate showed AC-001, AC-004, AC-005, AC-007, and AC-008 verified; AC-002 and AC-003 only partially verified; AC-006 unverified due missing provisioning coordinator tests; and AC-009 failed because the approved browser workflow does not exist.

**Question Asked:**
For `LLM_ROUTE_RELEASE_DEPLOYMENT`, what is the final release decision at this gate?

**Options Presented:**
- A) NEEDS_WORK
- B) APPROVED_WITH_RISK
- C) DEFERRED
- D) APPROVED
- E) REJECTED

**User Selection:**
APPROVED_WITH_RISK

**User Notes:**
Later clarified that only D-005 was an accepted risk and that D-003 should still block release.

**Decision:**
APPROVED_WITH_RISK

**Impact:**
- Feature Spec: none
- Acceptance Criteria: none
- Tests: none
- Defects: follow-up triage required for D-001, D-003, and D-005 to resolve approval scope
- Confidence / Release: adjusted — this release selection was not internally consistent until risk scope was clarified

**Required Follow-Up Actions:**
- [x] Ask which unresolved defects are explicit accepted risks.
- [x] Clarify whether any non-accepted unresolved defect remains a blocker.

---

### Decision HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-007 — Release gate clarification after risk scoping

**Stage:** human_review
**Agent:** HITL Review Agent
**Decision Type:** release_approval
**Status:** active

**Context Summary:**
After the initial `APPROVED_WITH_RISK` answer, the human accepted only D-005 as release risk. That left D-001 (missing approved browser workflow) and D-003 (missing coordinator verification for AC-006) unresolved and not accepted as risk, so the release posture still needed explicit clarification.

**Question Asked:**
How should this approval be scoped given that D-001 and D-003 were not accepted risks?

**Options Presented:**
- A) Scope approval to the currently verified/partially verified non-browser slice; defer D-001 and D-003 follow-up
- B) Treat D-001 as a blocker; release should actually be NEEDS_WORK
- C) Treat D-003 as a blocker; release should actually be NEEDS_WORK

**User Selection:**
C

**User Notes:**
D-005 remains an accepted risk; D-003 is the blocker that prevents release approval.

**Decision:**
NEEDS_WORK

**Impact:**
- Feature Spec: none
- Acceptance Criteria: none
- Tests: update needed — T-012 and T-013 remain required before AC-006 can be marked verified
- Defects: update/keep open — D-003 remains open and blocking; D-005 is accepted risk; D-001 still needs explicit triage
- Confidence / Release: blocks approval — the feature cannot be released until D-003 is addressed

**Required Follow-Up Actions:**
- [ ] Implement coordinator success/failure coverage for AC-006 (`LLMRD-T-012`, `LLMRD-T-013`).
- [ ] Re-run PSTF verification after D-003 is addressed.
- [x] Triage D-001 explicitly as blocker, follow-up, accepted risk, or not-a-defect.

---

### Decision HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-008 — Missing browser workflow defect triage

**Stage:** human_review
**Agent:** HITL Review Agent
**Decision Type:** defect_triage
**Status:** active

**Context Summary:**
`LLMRD-D-001` is a major product defect: the approved dedicated end-user LLM browser workflow required by AC-009 is still missing under `web/src/routes`. After release clarification, this defect still needed explicit triage so downstream work can distinguish “fix now” from “follow-up later.”

**Question Asked:**
How should D-001 be handled at this stage? D-001 = the approved dedicated end-user LLM browser workflow in AC-009 is still missing.

**Options Presented:**
- A) DEFER_TO_FOLLOWUP
- B) BLOCKER
- C) ACCEPTED_RISK
- D) NOT_A_DEFECT

**User Selection:**
BLOCKER

**User Notes:**
None.

**Decision:**
BLOCKER

**Impact:**
- Feature Spec: none
- Acceptance Criteria: none
- Tests: update needed — T-018 remains blocked until the browser workflow exists
- Defects: update/keep open — D-001 is explicitly blocking at this stage
- Confidence / Release: blocks approval — AC-009 remains a known failed product behavior

**Required Follow-Up Actions:**
- [ ] Implement the dedicated browser workflow required by AC-009.
- [ ] Add and run the blocked E2E workflow test `LLMRD-T-018` once the workflow exists.
- [ ] Re-run PSTF verification after D-001 is addressed.

## Summary

- Final Status: NEEDS_WORK
- Open Questions: 1 — what explicit rollback target-selection rule should replace the current implementation
- Accepted Risks: 1 — D-005 requester-side relay-acceptance verification for signer-first route/release requests
- Deferred Items: 1 — rollback-specific acceptance criteria and tests remain deferred until replacement semantics are approved
- Blocking Issues: 2 — D-003 missing provisioning coordinator verification for AC-006; D-001 missing approved browser workflow for AC-009
- Superseded Decisions: 1 — HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-006 was superseded by HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-007

## Traceability

| Decision ID | Affects ACs | Affects Tests | Affects Defects | Affects Artifacts | Notes |
| --- | --- | --- | --- | --- | --- |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-001 | LLMRD-AC-009 | future browser/E2E coverage | possible web-workflow gap issue | feature_spec.json, acceptance_criteria.json | Current shared-store-only web visibility is not the approved end state. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-002 | LLMRD-AC-009 | router compatibility tests become observed-only evidence | deprecation/removal follow-up | feature_spec.json, acceptance_criteria.json | Operational REST mutations are not part of the approved intended contract. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-003 | future rollback AC set | future rollback tests | rollback semantic-fix follow-up | feature_spec.json, future acceptance_criteria.json | Rollback remains in scope, but its current selection rule is not approved. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-004 | rollback AC scope | rollback test generation | none | acceptance_criteria.json, hitl_decisions.md | Rollback ACs are intentionally omitted until replacement semantics are approved. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-005 | LLMRD-AC-001..009 | non-rollback test generation | none | acceptance_criteria.json, hitl_decisions.md | Current AC set approved as written. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-006 | release gate | none | D-001, D-003, D-005 | hitl_decisions.md | Initial APPROVED_WITH_RISK response was not self-consistent once risk scope was clarified. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-007 | LLMRD-AC-006 | T-012, T-013 | D-003 | hitl_decisions.md, verification_report.md | D-003 is a blocker, so the release gate outcome is NEEDS_WORK rather than APPROVED_WITH_RISK. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-008 | LLMRD-AC-009 | T-018 | D-001 | hitl_decisions.md, verification_report.md | Missing browser workflow is explicitly treated as a blocker at this stage. |
