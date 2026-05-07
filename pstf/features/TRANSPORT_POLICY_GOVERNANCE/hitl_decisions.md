# HITL Decisions — TRANSPORT_POLICY_GOVERNANCE

## Metadata
- Feature ID: TRANSPORT_POLICY_GOVERNANCE
- Task ID: bahia-z1ak
- Framework: PSTF
- Interaction Mode: RepoPrompt ask-user tool
- Current Stage: acceptance_criteria
- Last Updated: 2026-05-06

---

## Decision Ledger

### Decision HITL-TRANSPORT_POLICY_GOVERNANCE-001 — Route-level REST compatibility gating classification

**Stage:** spec_reconstruction
**Agent:** PSTF Spec Reconstruction Agent
**Decision Type:** legacy_behavior_classification
**Status:** active

**Context Summary:**
`docs/investigations/signer-first-auth-audit-2026-05-03.md` says route-level REST compatibility gating had been updated for several protected route groups. Current code disagrees: `web/src/lib/auth/route-access.js` has an empty `ROUTE_COMPATIBILITY_REQUIREMENTS` map, and current route files sampled for `/payments`, `/orgs`, and `/notifications` are signer/encrypted-store driven rather than direct REST pages. The spec could not safely infer whether the empty map was a regression or whether the audit note had simply gone stale.

**Question Asked:**
How should the current empty route-level REST-compatibility map be classified in TRANSPORT_POLICY_GOVERNANCE?

**Options Presented:**
- A) KEEP_EMPTY_AS_INTENDED_NOW — treat the old audit statement as stale; route-level REST compatibility gating is no longer part of the intended browser policy
- B) REINSTATE_ROUTE_GATING_FOR_ANY_REMAINING_REST_DEPENDENT_PROTECTED_ROUTES — treat the empty map as a regression that should be fixed
- C) DEFER_CLASSIFICATION — record the contradiction but do not classify the current empty map yet

**User Selection:**
KEEP_EMPTY_AS_INTENDED_NOW — treat the old audit statement as stale; route-level REST compatibility gating is no longer part of the intended browser policy

**User Notes:**
None.

**Decision:**
KEEP_EMPTY_ROUTE_COMPATIBILITY_MAP_AS_INTENDED_NOW

**Impact:**
- Feature Spec: updated — route-level REST compatibility gating is not part of the intended browser transport policy today.
- Acceptance Criteria: future browser transport criteria should not require route-level REST compatibility gates for protected routes.
- Tests: any old assertions expecting protected-route REST compatibility gating should be treated as stale unless tied to a new explicit compatibility exception.
- Defects: documentation follow-up implied — the 2026-05-03 audit statement is now stale relative to intended policy.
- Confidence / Release: narrows ambiguity by removing one false transport-policy dependency from the intended contract.

**Required Follow-Up Actions:**
- [ ] Refresh stale audit or planning references that still describe route-level REST compatibility gating as current browser policy.
- [ ] Keep future compatibility exceptions explicit per feature or per surface instead of reviving a blanket route-prefix gate silently.

---

### Decision HITL-TRANSPORT_POLICY_GOVERNANCE-002 — Browser payment transport classification

**Stage:** spec_reconstruction
**Agent:** PSTF Spec Reconstruction Agent
**Decision Type:** legacy_behavior_classification
**Status:** active

**Context Summary:**
Current observed behavior is split. `web/src/routes/payments/+page.svelte` loads browser payment history through encrypted `payments.history`, but `web/src/routes/+page.svelte` still calls REST `api.getPaymentHistory(...)` for dashboard cost summary and `web/src/lib/api/client.js` still exposes REST `getPaymentHistory`, `estimateCost`, and `getRunCost`. Existing docs say sensitive browser domains may use encrypted request/result flows, but the canonical payment transport boundary was not explicit enough to reconstruct safely without a human decision.

**Question Asked:**
For TRANSPORT_POLICY_GOVERNANCE, how should browser-visible payment transport be classified?

**Options Presented:**
- A) ENCRYPTED_HISTORY_CANONICAL__REST_SUMMARY_COMPAT_ONLY — browser payment history belongs on encrypted transport; REST payment endpoints may remain compatibility/admin surfaces for summaries or other non-canonical uses
- B) REST_AND_ENCRYPTED_BOTH_SUPPORTED_CANONICALLY — keep both browser REST and encrypted payment surfaces as intended first-class product behavior
- C) MOVE_ALL_BROWSER_PAYMENT_SURFACES_TO_ENCRYPTED — treat remaining browser REST payment usage as legacy to remove/fix

**User Selection:**
MOVE_ALL_BROWSER_PAYMENT_SURFACES_TO_ENCRYPTED — treat remaining browser REST payment usage as legacy to remove/fix

**User Notes:**
None.

**Decision:**
MOVE_ALL_BROWSER_PAYMENT_SURFACES_TO_ENCRYPTED

**Impact:**
- Feature Spec: updated — browser-visible payment data belongs on encrypted transport, not REST.
- Acceptance Criteria: future transport criteria should fail if browser payment journeys depend on REST payment history or similar REST payment reads.
- Tests: dashboard and any other browser payment flows using REST should be treated as legacy coverage or migration gaps, not as proof of intended behavior.
- Defects: implementation follow-up implied — remaining browser REST payment usage should migrate or be removed.
- Confidence / Release: clarifies a previously under-specified mixed-transport edge.

**Required Follow-Up Actions:**
- [ ] Track migration of dashboard/browser payment reads away from REST and onto approved encrypted transport.
- [ ] Keep non-browser/admin payment compatibility surfaces explicit if they remain, rather than letting browser usage drift back to REST by convenience.

---

### Decision HITL-TRANSPORT_POLICY_GOVERNANCE-003 — EventSource log-streaming classification

**Stage:** spec_reconstruction
**Agent:** PSTF Spec Reconstruction Agent
**Decision Type:** legacy_behavior_classification
**Status:** active

**Context Summary:**
`web/src/lib/api/client.js` still exposes EventSource-based `streamLogs(...)` for live service logs. Meanwhile, current control-plane docs explicitly removed the old dashboard SSE stream, and separate PSTF evidence already treats deployment run-log retrieval as an encrypted request/result domain (`deployments.run_logs.get`). The spec could not safely infer whether SSE log streaming remained an approved compatibility surface or was legacy drift.

**Question Asked:**
How should EventSource-based live log streaming be classified in TRANSPORT_POLICY_GOVERNANCE?

**Options Presented:**
- A) REMOVE_FROM_INTENDED_BROWSER_CONTRACT — treat EventSource log streaming as legacy behavior to deprecate in favor of approved Nostr-native/encrypted log flows
- B) KEEP_AS_COMPATIBILITY_ONLY — SSE log streaming may remain as an explicit compatibility/admin surface, but not the canonical browser contract
- C) DEFER_DECISION — keep the ambiguity open for a later slice

**User Selection:**
REMOVE_FROM_INTENDED_BROWSER_CONTRACT — treat EventSource log streaming as legacy behavior to deprecate in favor of approved Nostr-native/encrypted log flows

**User Notes:**
None.

**Decision:**
REMOVE_EVENTSOURCE_LOG_STREAMING_FROM_INTENDED_BROWSER_CONTRACT

**Impact:**
- Feature Spec: updated — EventSource log streaming is excluded from the intended browser transport contract.
- Acceptance Criteria: future browser transport criteria should not rely on EventSource log streaming as approved behavior.
- Tests: SSE log-stream coverage remains useful as observed legacy evidence but not as approval-backed browser transport proof.
- Defects: follow-up implied — browser/API log streaming helpers should migrate toward approved Nostr-native or encrypted log flows.
- Confidence / Release: reduces ambiguity by classifying a major transport outlier as legacy drift rather than coexistence-by-default.

**Required Follow-Up Actions:**
- [ ] Track deprecation or removal of EventSource-based browser log streaming from the intended product surface.
- [ ] Keep encrypted run-log retrieval and other approved event-driven log flows distinct from removed SSE behavior in downstream PSTF artifacts.

---

### Decision HITL-TRANSPORT_POLICY_GOVERNANCE-004 — Acceptance criteria approval

**Stage:** acceptance_criteria
**Agent:** PSTF Acceptance Criteria Agent
**Decision Type:** spec_approval
**Status:** active

**Context Summary:**
A draft AC set was prepared for the shared transport-policy slice covering public signer-first relay boundaries, encrypted browser relay boundaries, fail-closed encrypted capability gating, the approved absence of route-level REST compatibility gating, encrypted-only browser payment policy, EventSource log-stream deprecation, explicit pre-acceptance operator HTTP fallback, compatibility-only 311xx handling, operator relay visibility via NIP-51 kind `30002`, event-driven completion rules, and end-to-end mixed-transport relay separation.

**Question Asked:**
Do you approve the acceptance-criteria direction for `TRANSPORT_POLICY_GOVERNANCE` so I can finalize `acceptance_criteria.json`?

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
- Acceptance Criteria: approved — `acceptance_criteria.json` may be finalized with the drafted AC set
- Tests: test design may proceed from the approved ACs
- Defects: none created by this approval decision
- Confidence / Release: raises confidence that subsequent test generation is grounded in explicitly approved transport-policy criteria

**Required Follow-Up Actions:**
- [x] Write `pstf/features/TRANSPORT_POLICY_GOVERNANCE/acceptance_criteria.json` with the approved AC set.
- [ ] Use the approved AC ids as the basis for PSTF test-matrix generation.

---

## Summary

- Final Status: active decisions captured
- Open Questions: 0 for this reconstruction pass
- Accepted Risks: 0
- Deferred Items: 0
- Blocking Issues: 0 newly created by this ledger
- Superseded Decisions: 0

## Traceability

| Decision ID | Affects ACs | Affects Tests | Affects Defects | Affects Artifacts | Notes |
| --- | --- | --- | --- | --- | --- |
| HITL-TRANSPORT_POLICY_GOVERNANCE-001 | future transport-policy ACs around browser compatibility | old route-gating assertions become stale | stale-doc cleanup implied | feature_spec.json, hitl_decisions.md | Confirms empty route-level REST compatibility map is intended now. |
| HITL-TRANSPORT_POLICY_GOVERNANCE-002 | future payment transport ACs | dashboard/browser REST payment tests become legacy evidence | browser payment migration implied | feature_spec.json, hitl_decisions.md | Browser payment surfaces should move fully to encrypted transport. |
| HITL-TRANSPORT_POLICY_GOVERNANCE-003 | future browser log-transport ACs | SSE log-stream tests become legacy evidence | EventSource deprecation/removal implied | feature_spec.json, hitl_decisions.md | Removes EventSource streaming from the intended browser contract. |
| HITL-TRANSPORT_POLICY_GOVERNANCE-004 | TPG-AC-001..012 | future test matrix for transport-policy slice | none | acceptance_criteria.json, hitl_decisions.md | Human approved the AC direction as drafted. |
