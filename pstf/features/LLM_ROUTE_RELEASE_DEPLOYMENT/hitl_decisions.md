# HITL Decisions — LLM_ROUTE_RELEASE_DEPLOYMENT

## Metadata
- Feature ID: LLM_ROUTE_RELEASE_DEPLOYMENT
- Task ID: bahia-57ek
- Framework: PSTF
- Interaction Mode: RepoPrompt ask-user tool
- Current Stage: human_review
- Last Updated: 2026-05-06T00:20:00Z

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
**Status:** superseded
**Superseded By:** HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-012

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


### Decision HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-009 — Canonical signer-first route/release requirement for release scope

**Stage:** human_review
**Agent:** HITL Review Agent
**Decision Type:** ambiguity_resolution
**Status:** active

**Context Summary:**
The adversarial review identified that route creation and release registration still used direct MCP registry mutation instead of the canonical signer-first `5971` / `5972` request path required by the approved contract. Release scope needed an explicit decision before verification could continue.

**Question Asked:**
How should route creation and release registration be treated for this release?

**Options Presented:**
- A) Require canonical signer-first `5971` / `5972` request publishing before release
- B) Allow direct MCP registry mutation as a temporary compatibility path for this release
- C) Defer route/release creation from the approved release scope until canonical publishing exists
- D) Revise AC-002 and AC-003 to describe the mixed contract explicitly

**User Selection:**
A

**User Notes:**
None.

**Decision:**
REQUIRE_CANONICAL_SIGNER_FIRST_5971_5972_BEFORE_RELEASE

**Impact:**
- Feature Spec: none — this confirms the existing intended contract rather than changing it.
- Acceptance Criteria: none — AC-002 and AC-003 remain in force as written.
- Tests: updated — route/release requester coverage now includes publisher, MCP, browser-helper, and E2E evidence for canonical signer-first behavior.
- Defects: updated — D-005 is no longer an accepted-risk escape hatch and is now resolved by implementation plus passing coverage.
- Confidence / Release: adjusted — release scope no longer permits direct MCP registry mutation as an alternative contract for route/release operations.

**Required Follow-Up Actions:**
- [x] Implement canonical signer-first requester publishing for route creation and release registration.
- [x] Verify that MCP tooling uses the canonical publisher path instead of direct registry mutation.
- [x] Update PSTF verification artifacts to remove D-005 as an accepted risk.

---

### Decision HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-010 — Coverage exception rejection for confidence gate

**Stage:** human_review
**Agent:** HITL Review Agent
**Decision Type:** risk_acceptance
**Status:** active

**Context Summary:**
The approved non-rollback slice was behaviorally verified, but the confidence gate still failed because no touched-module coverage artifact existed. The release path needed an explicit choice between accepting that gap as policy exception or requiring additional frontend coverage work before return to final review.

**Question Asked:**
For the LLM route deployment confidence gate, should the release proceed with a coverage exception or require more frontend coverage first?

**Options Presented:**
- A) ACCEPT_COVERAGE_EXCEPTION_FOR_THIS_RELEASE
- B) REQUIRE_ADDITIONAL_FRONTEND_COVERAGE_BEFORE_RELEASE

**User Selection:**
B

**User Notes:**
None.

**Decision:**
REQUIRE_ADDITIONAL_FRONTEND_COVERAGE_BEFORE_RELEASE

**Impact:**
- Feature Spec: none
- Acceptance Criteria: none
- Tests: updated — added direct unit coverage for `route-access.js`, extracted `nav-model.js`, and extracted `page-model.js` to cover the dedicated `/llm` workflow logic with deterministic browser-unit tests.
- Defects: none
- Confidence / Release: adjusted — the confidence gate cannot rely on a policy exception and must be resolved with real coverage artifacts before final human review.

**Required Follow-Up Actions:**
- [x] Generate touched-module frontend coverage artifacts for the `/llm` browser workflow logic.
- [x] Re-run the confidence gate with the new coverage evidence.
- [x] Return to final human review without relying on a coverage exception.

---


### Decision HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-011 — Final release approval for approved non-rollback slice

**Stage:** human_review
**Agent:** HITL Review Agent
**Decision Type:** release_approval
**Status:** active

**Context Summary:**
The approved non-rollback slice of `LLM_ROUTE_RELEASE_DEPLOYMENT` reached the final release gate with AC-001 through AC-009 verified, 19/19 mapped tests passing, no open defects for the approved slice, and confidence scoring at 0.93. Rollback remains explicitly deferred and is not part of this approval decision.

**Question Asked:**
What is the final release decision for the approved non-rollback slice of `LLM_ROUTE_RELEASE_DEPLOYMENT`?

**Options Presented:**
- A) APPROVED
- B) APPROVED_WITH_RISK
- C) NEEDS_WORK
- D) REJECTED
- E) DEFERRED

**User Selection:**
APPROVED

**User Notes:**
None.

**Decision:**
APPROVED

**Impact:**
- Feature Spec: none
- Acceptance Criteria: none
- Tests: none
- Defects: none
- Confidence / Release: updated — final human approval now exists for the approved non-rollback slice.

**Required Follow-Up Actions:**
- [x] Update `confidence_report.json` to reflect final approval.
- [x] Keep rollback out of release claims until replacement semantics are explicitly approved.

---

### Decision HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-012 — Replacement rollback target-selection rule

**Stage:** human_review
**Agent:** PSTF Spec Reconstruction Agent
**Decision Type:** ambiguity_resolution
**Status:** active

**Context Summary:**
The current rollback implementation in `internal/service/llm_registry.go` was previously classified as `FIX`, not approved behavior, and the docs still do not define which prior release rollback should target. The reconstructed feature spec needed an explicit replacement rule before it could stop treating rollback semantics as unresolved product intent.

**Question Asked:**
How should LLM rollback target selection be handled in the reconstructed spec right now?

**Options Presented:**
- A) DEFER (recommended): keep rollback in feature scope but leave its exact target-selection semantics unspecified until a later decision
- B) Rollback to previous deployed different release automatically (n-1 style)
- C) Rollback must require explicit release selection by the user/operator
- D) Rollback to the last successful deployment before the current desired release, with current desired intent superseded

**User Selection:**
B

**User Notes:**
None.

**Decision:**
APPROVE_N_MINUS_1_AUTOMATIC_LLM_ROLLBACK_SELECTION

**Impact:**
- Feature Spec: updated — rollback target selection is now explicitly intended as automatic rollback to the previous deployed different release (n-1 style).
- Acceptance Criteria: rollback-specific criteria may now be authored/refreshed from an approved semantic rule instead of staying fully deferred for lack of product intent.
- Tests: rollback tests can now be designed against explicit target-selection semantics once the AC artifact is refreshed.
- Defects: resolved by bahia-3nsy — implementation review found and verified explicit rollback target-selection helpers for the approved n-1 rule.
- Confidence / Release: unchanged for the approved non-rollback slice; rollback remains outside verified release claims until rollback ACs/tests/browser coverage are added.

**Required Follow-Up Actions:**
- [ ] Refresh `acceptance_criteria.json` with rollback-specific criteria based on the approved n-1 rule.
- [ ] Refresh `test_matrix.json` with rollback-specific tests.
- [x] Re-verify `RollbackWithMetadata` against explicit n-1 ordering and supersedence expectations.

---

### Decision HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-013 — Browser-visible rollback classification

**Stage:** human_review
**Agent:** PSTF Spec Reconstruction Agent
**Decision Type:** legacy_behavior_classification
**Status:** active

**Context Summary:**
The current dedicated `/llm` browser workflow supports route creation, release registration, deployment request, approval/rejection, and relay-backed route-state/activity visibility, but it does not expose rollback. Earlier PSTF decisions approved a full end-user workflow for this feature family, and the new rollback target-selection decision removed the last semantic blocker to deciding whether the missing browser rollback action is acceptable or a product gap.

**Question Asked:**
How should browser-visible rollback be treated in the spec right now?

**Options Presented:**
- A) FIX (recommended): dedicated `/llm` workflow is intended to expose rollback, and its current absence is a product gap
- B) DEFER: rollback semantics are approved, but browser rollback UI is not yet required in the current intended workflow
- C) REMOVE: rollback should remain API/MCP/operator-only, not part of the dedicated browser workflow

**User Selection:**
A

**User Notes:**
None.

**Decision:**
FIX_MISSING_BROWSER_ROLLBACK_ACTION

**Impact:**
- Feature Spec: updated — rollback remains part of the intended dedicated browser workflow, and the missing browser action is now classified as a product gap instead of an allowed omission.
- Acceptance Criteria: future rollback criteria should include browser-visible rollback behavior, not just transport/backend semantics.
- Tests: browser and E2E rollback coverage is now required once rollback ACs are refreshed.
- Defects: a follow-up defect/work item is implied because `web/src/routes/llm/+page.svelte` still lacks rollback UI/action handling.
- Confidence / Release: unchanged for the approved non-rollback slice; rollback still is not part of the verified release scope.

**Required Follow-Up Actions:**
- [ ] Add rollback action support to the dedicated `/llm` workflow.
- [ ] Add browser/E2E rollback coverage once the workflow exists.
- [ ] Keep rollback out of verified release claims until the browser gap is closed and rollback is re-verified.

---

### Decision HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-014 — Acceptance criteria approval for rollback-aware AC refresh

**Stage:** human_review
**Agent:** PSTF Acceptance Criteria Agent
**Decision Type:** spec_approval
**Status:** active

**Context Summary:**
The feature spec now explicitly approves rollback target selection as automatic previous deployed different release (n-1 style) and classifies missing browser rollback in `/llm` as a FIX. The acceptance-criteria artifact therefore needed to be refreshed by preserving the verified non-rollback ACs and adding rollback-specific ACs for signer-first rollback request semantics, rollback target-selection behavior, and the intended browser-visible rollback action.

**Question Asked:**
Do you approve the updated acceptance-criteria direction for `LLM_ROUTE_RELEASE_DEPLOYMENT`?

**Options Presented:**
- A) APPROVE_AS_IS (recommended)
- B) APPROVE_WITH_EDITS
- C) REJECT_AND_REVISE

**User Selection:**
APPROVE_AS_IS

**User Notes:**
None.

**Decision:**
APPROVE_ROLLBACK_AWARE_AC_REFRESH_AS_IS

**Impact:**
- Feature Spec: none
- Acceptance Criteria: updated — AC-001 through AC-009 remain intact, and AC-010 through AC-012 now capture rollback request semantics, n-1 rollback selection, and browser-visible rollback expectations.
- Tests: updated — future test generation may now include rollback-specific backend and browser coverage.
- Defects: none directly, though the existing missing browser rollback action remains a FIX-classified product gap per HITL-013.
- Confidence / Release: unchanged for the approved non-rollback slice; rollback is newly specified but not yet re-verified.

**Required Follow-Up Actions:**
- [x] Refresh `acceptance_criteria.json` with rollback-aware criteria.
- [ ] Refresh `test_matrix.json` to add rollback-specific tests.
- [ ] Re-verify rollback implementation and browser workflow against the new AC set.

---

## Summary

- Final Status: APPROVED
- Open Questions: 0 — rollback product intent is now explicitly captured by HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-012 and HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-013
- Accepted Risks: 0 — none currently recorded for the approved non-rollback slice
- Deferred Items: 0 — semantic deferral is resolved; rollback artifact refresh and browser follow-up remain implementation work
- Blocking Issues: 0 — none for the approved non-rollback slice; rollback still is not re-verified
- Superseded Decisions: 2 — HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-004 was superseded by HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-012, and HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-006 was superseded by HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-007

## Traceability

| Decision ID | Affects ACs | Affects Tests | Affects Defects | Affects Artifacts | Notes |
| --- | --- | --- | --- | --- | --- |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-001 | LLMRD-AC-009 | future browser/E2E coverage | possible web-workflow gap issue | feature_spec.json, acceptance_criteria.json | Current shared-store-only web visibility is not the approved end state. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-002 | LLMRD-AC-009 | router compatibility tests become observed-only evidence | deprecation/removal follow-up | feature_spec.json, acceptance_criteria.json | Operational REST mutations are not part of the approved intended contract. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-003 | future rollback AC set | future rollback tests | rollback semantic-fix follow-up | feature_spec.json, future acceptance_criteria.json | Rollback remains in scope, but its current selection rule is not approved. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-004 | rollback AC scope | rollback test generation | none | acceptance_criteria.json, hitl_decisions.md | Superseded by HITL-012 once rollback target-selection semantics were approved. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-005 | LLMRD-AC-001..009 | non-rollback test generation | none | acceptance_criteria.json, hitl_decisions.md | Current AC set approved as written. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-006 | release gate | none | D-001, D-003, D-005 | hitl_decisions.md | Initial APPROVED_WITH_RISK response was not self-consistent once risk scope was clarified. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-007 | LLMRD-AC-006 | T-012, T-013 | D-003 | hitl_decisions.md, verification_report.md | D-003 is a blocker, so the release gate outcome is NEEDS_WORK rather than APPROVED_WITH_RISK. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-008 | LLMRD-AC-009 | T-018 | D-001 | hitl_decisions.md, verification_report.md | Missing browser workflow is explicitly treated as a blocker at this stage. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-009 | LLMRD-AC-002, LLMRD-AC-003 | T-002, T-003, T-004, T-005, T-018 | D-005 | hitl_decisions.md, verification_report.md, defects.json | Route/release operations must use canonical signer-first 5971/5972 publishing before release. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-010 | none | coverage support for T-016, T-018 and new browser-helper tests | none | hitl_decisions.md, confidence_report.json | Coverage exception rejected; real frontend coverage was required before return to final review. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-011 | none | none | none | hitl_decisions.md, confidence_report.json | Final human approval granted for the approved non-rollback slice only. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-012 | future rollback AC set | future rollback tests | rollback implementation/browser follow-up | feature_spec.json, future acceptance_criteria.json, future test_matrix.json | Rollback target selection is now approved as automatic n-1 rollback to the previous deployed different release. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-013 | future rollback AC set | future browser/E2E rollback coverage | missing browser rollback action follow-up | feature_spec.json, future acceptance_criteria.json, future test_matrix.json | The dedicated `/llm` workflow is intended to expose rollback; its current absence is a FIX-classified product gap. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-014 | LLMRD-AC-010, LLMRD-AC-011, LLMRD-AC-012 | future rollback backend/browser tests | browser rollback follow-up remains open | acceptance_criteria.json, hitl_decisions.md | Updated acceptance criteria approved with rollback-specific ACs added to the artifact. |
