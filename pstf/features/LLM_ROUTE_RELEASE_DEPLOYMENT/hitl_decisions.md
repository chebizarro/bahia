# HITL Decisions — LLM_ROUTE_RELEASE_DEPLOYMENT

## Metadata
- Feature ID: LLM_ROUTE_RELEASE_DEPLOYMENT
- Task ID: bahia-57ek
- Framework: PSTF
- Interaction Mode: RepoPrompt ask-user tool
- Current Stage: human_review
- Last Updated: 2026-05-04

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

## Summary

- Final Status: decisions captured
- Open Questions: 1 — what explicit rollback target-selection rule should replace the current implementation
- Accepted Risks: 0
- Deferred Items: 0
- Blocking Issues: 1 — intended full web workflow is approved but not currently observed as a dedicated route/page implementation
- Superseded Decisions: 0

## Traceability

| Decision ID | Affects ACs | Affects Tests | Affects Defects | Affects Artifacts | Notes |
| --- | --- | --- | --- | --- | --- |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-001 | future LLM AC set | future browser/E2E coverage | possible web-workflow gap issue | feature_spec.json, future acceptance_criteria.json | Current shared-store-only web visibility is not the approved end state. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-002 | future LLM AC set | router compatibility tests become observed-only evidence | deprecation/removal follow-up | feature_spec.json, future acceptance_criteria.json | Operational REST mutations are not part of the approved intended contract. |
| HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-003 | future rollback ACs | future rollback tests | rollback semantic-fix follow-up | feature_spec.json, future acceptance_criteria.json | Rollback remains in scope, but its current selection rule is not approved. |
