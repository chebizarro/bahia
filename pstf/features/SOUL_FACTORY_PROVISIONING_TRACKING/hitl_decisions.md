# HITL Decisions — SOUL_FACTORY_PROVISIONING_TRACKING

## Metadata
- Feature ID: SOUL_FACTORY_PROVISIONING_TRACKING
- Task ID: bahia-gktu
- Framework: PSTF
- Interaction Mode: RepoPrompt ask-user tool
- Current Stage: human_review
- Last Updated: 2026-05-05T06:28:05Z

---

## Decision Ledger

### Decision HITL-SOUL_FACTORY_PROVISIONING_TRACKING-001 — Feature scope classification

**Stage:** spec_reconstruction
**Agent:** PSTF Spec Reconstruction Agent
**Decision Type:** product_intent
**Status:** active

**Context Summary:**
`docs/soul-factory.md` describes Soul Factory as a full lifecycle product with CLI, MCP, provisioning infrastructure, and lifecycle management. The originally selected browser/store code and tests only proved the relay-backed Souls UI plus provisioning/action tracking. The spec could not safely infer whether the broader lifecycle was actually intended now.

**Question Asked:**
Which scope should this PSTF feature reconstruct right now?

**Options Presented:**
- A) Browser relay-backed Souls UI plus provisioning/action tracking only
- B) Browser slice now; full CLI/MCP/backend lifecycle later as a separate slice
- C) Full Soul Factory lifecycle including CLI/MCP/backend provisioning now
- D) Defer until more implementation proof exists

**User Selection:**
C

**User Notes:**
None.

**Decision:**
FULL_SOUL_FACTORY_LIFECYCLE_INTENDED_NOW

**Impact:**
- Feature Spec: updated — the spec treats the browser UI, backend provisioning engine, Bahia integration, CLI surface, and MCP surface as one intended feature contract.
- Acceptance Criteria: future ACs must include currently unproven CLI/MCP/backend lifecycle expectations rather than limiting the slice to browser tracking only.
- Tests: future matrices must cover backend provisioning, lifecycle handlers, status sync, and current CLI/MCP gaps.
- Defects: current explicit unavailability in CLI/MCP and unsupported lifecycle operations become product gaps, not out-of-scope omissions.
- Confidence / Release: adjusted — intended scope is broader than what current selected proof verifies.

**Required Follow-Up Actions:**
- [ ] Keep observed vs intended behavior explicitly separated in the reconstructed spec.
- [ ] Treat CLI/MCP unavailability and unsupported lifecycle paths as implementation gaps in later PSTF stages.

---

### Decision HITL-SOUL_FACTORY_PROVISIONING_TRACKING-002 — Local timeout and relay-closure failure classification

**Stage:** spec_reconstruction
**Agent:** PSTF Spec Reconstruction Agent
**Decision Type:** ambiguity_resolution
**Status:** active

**Context Summary:**
The Souls browser store currently marks a provisioning run failed if the relay closes the subscription or if 120 seconds pass without a terminal relay update. The PSTF gap report flags that timeout-based failure behavior as risky, and it may conflict with the repo’s Nostr event-driven guidance.

**Question Asked:**
How should the current local timeout / relay-closure failure behavior for Soul provisioning tracking be classified?

**Options Presented:**
- A) KEEP — intended UX guardrail
- B) FIX — not intended; should not be normative behavior
- C) REMOVE — should not exist
- D) DEFER / UNKNOWN

**User Selection:**
B

**User Notes:**
None.

**Decision:**
FIX_LOCAL_TIMEOUT_AND_RELAY_CLOSURE_FAILURE_BEHAVIOR

**Impact:**
- Feature Spec: updated — timeout-driven local run failure is documented as observed legacy behavior, not approved intended behavior.
- Acceptance Criteria: future ACs should require event-driven provisioning completion/failure semantics instead of allowing arbitrary local timeout failure.
- Tests: existing timeout-based browser-store tests remain useful as observed coverage but should not be treated as proof of the approved contract.
- Defects: a future defect/patch is implied for timeout-driven failure semantics.
- Confidence / Release: adjusted — observed browser behavior diverges from the approved event-driven contract.

**Required Follow-Up Actions:**
- [ ] Track replacement browser behavior for stalled or closed provisioning subscriptions without promoting the timeout path to normative behavior.
- [ ] Keep timeout-based failure logic visible as a contradiction in later PSTF artifacts.

---

### Decision HITL-SOUL_FACTORY_PROVISIONING_TRACKING-003 — Periodic status polling fallback classification

**Stage:** spec_reconstruction
**Agent:** PSTF Spec Reconstruction Agent
**Decision Type:** ambiguity_resolution
**Status:** active

**Context Summary:**
The Soul Factory backend has an event-driven `StatusSyncHandler`, but it also includes `StartPeriodicSync`, which uses a ticker to poll Bahia deployment state and republish updated soul status. That behavior is operationally significant and conflicts with the repository’s Nostr event-driven guardrails unless explicitly accepted.

**Question Asked:**
How should the ticker-based Soul status polling fallback be classified for this feature?

**Options Presented:**
- A) FIX — not intended; Soul status sync should be event-driven, not polling-based
- B) KEEP — intended fallback for deployment status reconciliation
- C) DEFER — keep it out of the approved feature slice for now
- D) REMOVE — should be deleted rather than specified

**User Selection:**
A

**User Notes:**
None.

**Decision:**
FIX_TICKER_BASED_STATUS_POLLING_FALLBACK

**Impact:**
- Feature Spec: updated — ticker-based backend status polling is treated as observed implementation debt, not approved intended behavior.
- Acceptance Criteria: future ACs should require event-driven deployment status propagation from Bahia into soul events.
- Tests: later test plans should distinguish event-driven sync from the current ticker fallback.
- Defects: a future defect/patch is implied for polling-based status sync.
- Confidence / Release: adjusted — current backend behavior contains an unapproved protocol smell.

**Required Follow-Up Actions:**
- [ ] Treat `StartPeriodicSync` as a candidate defect in later PSTF stages.
- [ ] Keep Bahia-to-soul status propagation explicitly event-driven in future ACs and tests.

---


### Decision HITL-SOUL_FACTORY_PROVISIONING_TRACKING-004 — Acceptance-criteria treatment for unresolved event-driven replacements

**Stage:** acceptance_criteria
**Agent:** PSTF Acceptance Criteria Agent
**Decision Type:** ambiguity_resolution
**Status:** active

**Context Summary:**
The feature spec explicitly classifies browser timeout / relay-closure terminal failure and backend ticker-based status polling as `FIX`, not intended behavior. Acceptance criteria still needed to decide whether to block on replacement product details or approve a higher-level event-driven contract now.

**Question Asked:**
How should the acceptance criteria handle the unresolved replacement semantics for timeout-free browser tracking and event-driven backend status sync?

**Options Presented:**
- A) Keep high-level event-driven ACs now, but do not specify replacement UX/operability details yet
- B) Defer ACs for those two behaviors until a later decision defines the replacements
- C) Defer the entire Soul Factory AC set until those replacements are fully defined

**User Selection:**
A

**User Notes:**
None.

**Decision:**
KEEP_HIGH_LEVEL_EVENT_DRIVEN_ACS_NOW

**Impact:**
- Feature Spec: none
- Acceptance Criteria: updated — the AC set includes high-level event-driven prohibitions against timeout-based browser completion and ticker-based backend polling without inventing replacement UX/operability detail.
- Tests: future tests must prove the negative contract (no timeout/polling as normative behavior) while leaving detailed replacement behavior for a later decision.
- Defects: future timeout/polling defects remain expected where code still violates this contract.
- Confidence / Release: adjusted — the AC artifact can proceed without waiting for full redesign detail.

**Required Follow-Up Actions:**
- [ ] Capture replacement browser UX for stalled provisioning subscriptions in a later decision.
- [ ] Capture replacement non-polling deploy-status synchronization design in a later decision.

---

### Decision HITL-SOUL_FACTORY_PROVISIONING_TRACKING-005 — Bahia integration strictness for acceptance criteria

**Stage:** acceptance_criteria
**Agent:** PSTF Acceptance Criteria Agent
**Decision Type:** product_intent
**Status:** active

**Context Summary:**
Soul Factory docs describe deep Bahia integration, but current code only clearly proves service registration and partial deploy-status logic while initial deployment hookup and multiple lifecycle hooks remain incomplete or fail-closed. The AC set needed an explicit ruling on whether Bahia integration should be treated as a must-have now or narrowed to a smaller verified slice.

**Question Asked:**
How strict should the Soul Factory acceptance criteria be about Bahia deployment integration in this feature slice?

**Options Presented:**
- A) Require full Bahia integration now: service registration, initial deployment hookup, deploy-status sync, and lifecycle propagation are all must-have
- B) Require service registration and deploy-status visibility now; allow initial deployment hookup and lifecycle propagation to be follow-up work
- C) Defer Bahia integration beyond basic soul publication for this slice

**User Selection:**
A

**User Notes:**
None.

**Decision:**
REQUIRE_FULL_BAHIA_INTEGRATION_NOW

**Impact:**
- Feature Spec: none
- Acceptance Criteria: updated — ACs now require service registration, initial deployment hookup, deploy-status visibility, and lifecycle propagation for Bahia-managed souls.
- Tests: future matrices must include Bahia integration and lifecycle propagation coverage instead of treating those paths as optional follow-up work.
- Defects: current incomplete Bahia hooks are expected to surface as product defects during verification.
- Confidence / Release: adjusted — the approved ACs are intentionally stricter than currently verified behavior.

**Required Follow-Up Actions:**
- [ ] Add Bahia integration coverage for initial deployment hookup and lifecycle propagation in the test matrix.
- [ ] Treat current unsupported Bahia lifecycle hooks as implementation gaps rather than scope exclusions.

---

### Decision HITL-SOUL_FACTORY_PROVISIONING_TRACKING-006 — Acceptance criteria approval

**Stage:** acceptance_criteria
**Agent:** PSTF Acceptance Criteria Agent
**Decision Type:** spec_approval
**Status:** active

**Context Summary:**
A draft AC set was prepared for the full intended Soul Factory lifecycle: relay-backed browser flows, signer-first provisioning and lifecycle actions, the backend eight-step provisioning workflow, authoritative soul publication, full Bahia integration, real CLI/MCP behavior, and high-level event-driven constraints that reject timeout-based browser completion and ticker-based backend polling as normative behavior.

**Question Asked:**
Do you approve the acceptance criteria for `SOUL_FACTORY_PROVISIONING_TRACKING` on that basis?

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
- Tests: test-matrix generation may proceed against the full approved lifecycle contract
- Defects: none
- Confidence / Release: adjusted — the approved AC set is now the explicit release-quality contract, even where current implementation is expected to fail it

**Required Follow-Up Actions:**
- [x] Write `pstf/features/SOUL_FACTORY_PROVISIONING_TRACKING/acceptance_criteria.json` with the approved AC set.
- [ ] Use the approved AC set, not the narrower browser-only slice, for subsequent test design and verification.

---


### Decision HITL-SOUL_FACTORY_PROVISIONING_TRACKING-007 — Final release review after confidence and adversarial gating

**Stage:** human_review
**Agent:** HITL Review Agent
**Decision Type:** release_approval
**Status:** active

**Context Summary:**
`SOUL_FACTORY_PROVISIONING_TRACKING` is behaviorally verified against the approved contract: all 14 acceptance criteria are verified and all 18 mapped tests pass. The remaining gate gap is evidence quality rather than product behavior. The confidence report scores the slice at 0.84 instead of the 0.90 threshold because no touched-module coverage artifact exists, and adversarial review surfaced that gap as the only major release concern.

**Question Asked:**
What is the final release decision for `SOUL_FACTORY_PROVISIONING_TRACKING`?

**Options Presented:**
- A) APPROVED_WITH_RISK — release the verified slice and explicitly accept the missing coverage evidence risk
- B) NEEDS_WORK — require coverage artifacts before release approval
- C) DEFERRED — do not make a release decision for this slice yet
- D) REJECTED — do not release this slice in its current state

**User Selection:**
NEEDS_WORK — require coverage artifacts before release approval

**User Notes:**
None.

**Decision:**
NEEDS_WORK

**Impact:**
- Feature Spec: none
- Acceptance Criteria: none
- Tests: update needed — generate touched-module coverage artifacts so the confidence gate can score code coverage above `0.0`
- Defects: none — there are no open PSTF defects, but release remains blocked by evidence quality
- Confidence / Release: blocks approval — the feature remains below the default 0.90 confidence threshold and is not approved for release at this gate

**Required Follow-Up Actions:**
- [ ] Generate touched-module coverage artifacts for the Soul Factory backend and web suites.
- [ ] Re-run confidence scoring after coverage evidence exists.
- [ ] Repeat HITL release review once the confidence gate is re-evaluated.

---

## Summary

- Final Status: NEEDS_WORK
- Open Questions: 0
- Accepted Risks: 0
- Deferred Items: 0
- Blocking Issues: 1 — missing touched-module coverage evidence keeps confidence below threshold and blocks release approval
- Superseded Decisions: 0

## Traceability

| Decision ID | Affects ACs | Affects Tests | Affects Defects | Affects Artifacts | Notes |
| --- | --- | --- | --- | --- | --- |
| HITL-SOUL_FACTORY_PROVISIONING_TRACKING-001 | whole AC set | future matrix | future defects | feature_spec.json, acceptance_criteria.json | Full lifecycle remains the intended scope. |
| HITL-SOUL_FACTORY_PROVISIONING_TRACKING-002 | SFTP-AC-013 | future matrix | future defects | feature_spec.json, acceptance_criteria.json | Timeout/relay-close terminal behavior is not normative. |
| HITL-SOUL_FACTORY_PROVISIONING_TRACKING-003 | SFTP-AC-014 | future matrix | future defects | feature_spec.json, acceptance_criteria.json | Ticker polling is not normative status-sync behavior. |
| HITL-SOUL_FACTORY_PROVISIONING_TRACKING-004 | SFTP-AC-013, SFTP-AC-014 | future matrix | future defects | acceptance_criteria.json | Approved high-level event-driven ACs without replacement-detail invention. |
| HITL-SOUL_FACTORY_PROVISIONING_TRACKING-005 | SFTP-AC-007, SFTP-AC-009 | future matrix | future defects | acceptance_criteria.json | Bahia integration remains strict must-have behavior. |
| HITL-SOUL_FACTORY_PROVISIONING_TRACKING-006 | whole AC set | future matrix | future defects | acceptance_criteria.json | AC artifact approved as written. |
| HITL-SOUL_FACTORY_PROVISIONING_TRACKING-007 | none | coverage follow-up for existing matrix | none | confidence_report.json, hitl_decisions.md | Final release decision = NEEDS_WORK until coverage evidence exists. |
