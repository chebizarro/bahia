# HITL Decisions — SOUL_FACTORY_PROVISIONING_TRACKING

## Metadata
- Feature ID: SOUL_FACTORY_PROVISIONING_TRACKING
- Task ID: bahia-gktu
- Framework: PSTF
- Interaction Mode: RepoPrompt ask-user tool
- Current Stage: spec_reconstruction
- Last Updated: 2026-05-05T04:11:12Z

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

## Summary

- Final Status: PENDING
- Open Questions: 0
- Accepted Risks: 0
- Deferred Items: 0
- Blocking Issues: 4 — CLI surface explicitly unavailable; MCP mutation/list surfaces explicitly unavailable; lifecycle actions remain unsupported in configured provisioning engines; Bahia initial deployment and lifecycle integration contain incomplete / fail-closed paths.
- Superseded Decisions: 0

## Traceability

| Decision ID | Affects ACs | Affects Tests | Affects Defects | Affects Artifacts | Notes |
| --- | --- | --- | --- | --- | --- |
| HITL-SOUL_FACTORY_PROVISIONING_TRACKING-001 | future Soul Factory AC set | future Soul Factory matrix | future Soul Factory defects | feature_spec.json | Intended scope is full lifecycle now, despite partial current proof. |
| HITL-SOUL_FACTORY_PROVISIONING_TRACKING-002 | future Soul Factory AC set | `web/tests/unit/souls-store.test.js` | future Soul Factory defects | feature_spec.json | Timeout/relay-close run failure is not normative behavior. |
| HITL-SOUL_FACTORY_PROVISIONING_TRACKING-003 | future Soul Factory AC set | future status-sync coverage | future Soul Factory defects | feature_spec.json | Ticker-based deployment status polling is not normative behavior. |
