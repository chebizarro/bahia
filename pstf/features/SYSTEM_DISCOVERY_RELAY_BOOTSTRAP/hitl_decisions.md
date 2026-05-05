# HITL Decisions — SYSTEM_DISCOVERY_RELAY_BOOTSTRAP

## Metadata
- Feature ID: SYSTEM_DISCOVERY_RELAY_BOOTSTRAP
- Task ID: bahia-vfee
- Framework: PSTF
- Interaction Mode: RepoPrompt ask-user tool
- Current Stage: acceptance_criteria
- Last Updated: 2026-05-05T07:10:07Z

---

## Decision Ledger

### Decision HITL-SYSTEM_DISCOVERY_RELAY_BOOTSTRAP-001 — Sidecar-disabled relay exposure classification

**Stage:** spec_reconstruction
**Agent:** PSTF Spec Reconstruction Agent
**Decision Type:** legacy_behavior_classification
**Status:** active

**Context Summary:**
`/api/v1/system/info` is the shared discovery contract for browser public bootstrap, encrypted browser gating, and operator relay fallback. Current code still exposes `nostr.relays` when `relay_sidecar=false`, and the browser helper can normalize that field. At the same time, the documented/current product shape is sidecar-first, the browser bootstrap requires `features.relay_read_models=true`, and operator CLI fallback only consumes `browser_relays` plus `sidecar_url`. The spec could not safely infer whether direct `nostr.relays` exposure remained intended product behavior.

**Question Asked:**
How should sidecar-disabled `/api/v1/system/info` relay exposure be treated in the `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` spec?

**Options Presented:**
- A) KEEP as intended contract: direct `nostr.relays` discovery remains supported behavior for this feature
- B) DEFER from this slice: approve only the sidecar-first relay bootstrap contract; treat `nostr.relays` exposure as compatibility behavior outside the approved slice
- C) FIX/REMOVE: direct `nostr.relays` exposure should not remain part of system discovery behavior

**User Selection:**
C

**User Notes:**
None.

**Decision:**
FIX_OR_REMOVE_DIRECT_NOSTR_RELAYS_EXPOSURE

**Impact:**
- Feature Spec: updated — the intended discovery contract is sidecar-first and explicitly excludes direct `nostr.relays` bootstrap behavior.
- Acceptance Criteria: future criteria should fail if the approved slice still depends on sidecar-disabled direct-relay discovery.
- Tests: current handler/helper behavior around `nostr.relays` should be treated as legacy/cleanup evidence, not proof of approved intended behavior.
- Defects: likely follow-up needed — observed handler/helper behavior still exposes or normalizes direct `nostr.relays`.
- Confidence / Release: adjusted — observed implementation and approved intended contract diverge here.

**Required Follow-Up Actions:**
- [ ] Track removal or narrowing of sidecar-disabled direct `nostr.relays` exposure from the discovery contract.
- [ ] Ensure future acceptance criteria and tests target the sidecar-first browser discovery path, not raw relay exposure fallback.

---

### Decision HITL-SYSTEM_DISCOVERY_RELAY_BOOTSTRAP-002 — Acceptance-criteria approval

**Stage:** acceptance_criteria
**Agent:** PSTF Acceptance Criteria Agent
**Decision Type:** spec_approval
**Status:** active

**Context Summary:**
The draft AC set for `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` covered sidecar-first public bootstrap advertisement, conditional control-plane kind/feature advertisement, shared system-info caching, EOSE-bounded relay bootstrap, fail-closed bootstrap errors, canonical-author filtering, operator CLI relay discovery precedence, and an encrypted-capability-related criterion. PSTF requires explicit human approval before moving on to test design.

**Question Asked:**
Do you approve the acceptance criteria for SYSTEM_DISCOVERY_RELAY_BOOTSTRAP?

**Options Presented:**
- A) APPROVE_AS_IS
- B) APPROVE_WITH_EDITS
- C) REJECT_AND_REVISE
- D) DEFER_FEATURE

**User Selection:**
B

**User Notes:**
The initial edit request was: “Remove the encrypted relay AC, this doesn't make sense, there is no such thing as an encrypted relay”.

**Decision:**
APPROVE_WITH_EDITS

**Impact:**
- Feature Spec: no immediate rewrite applied here, but the AC set must avoid inventing relay-level encrypted semantics not approved by the user.
- Acceptance Criteria: updated — the encrypted-discovery criterion must be narrowed and reframed before final approval.
- Tests: future tests should not assert a relay-class taxonomy that the user rejected.
- Defects: none created at this stage.
- Confidence / Release: enables AC finalization once the requested edit is applied.

**Required Follow-Up Actions:**
- [ ] Reframe or remove the encrypted-relay AC so it does not describe encrypted behavior as a relay category.
- [ ] Capture the final encrypted-capability scope decision before writing `acceptance_criteria.json`.

---

### Decision HITL-SYSTEM_DISCOVERY_RELAY_BOOTSTRAP-003 — Encrypted capability scope inside discovery ACs

**Stage:** acceptance_criteria
**Agent:** PSTF Acceptance Criteria Agent
**Decision Type:** ambiguity_resolution
**Status:** active

**Context Summary:**
Current code and docs advertise `features.encrypted_nostr_requests` and browser-facing encrypted discovery fields in `/api/v1/system/info`, but the user rejected describing this as an “encrypted relay” requirement. The AC set needed a product-correct way to classify whether encrypted-request discovery remains part of this slice at all.

**Question Asked:**
For SYSTEM_DISCOVERY_RELAY_BOOTSTRAP, should encrypted-request discovery stay out of scope?

**Options Presented:**
- A) Yes — defer encrypted-request discovery from this slice; keep these ACs public-bootstrap only
- B) No — keep a minimal encrypted capability-gating requirement in this slice, but do not phrase it as relay behavior

**User Selection:**
B

**User Notes:**
“Encrypted messaging is dealt with at the individual event level using NIP-44/NIP-27.”

**Decision:**
KEEP_MINIMAL_ENCRYPTED_CAPABILITY_GATING_BUT_NOT_AS_RELAY_BEHAVIOR

**Impact:**
- Feature Spec: update may eventually be needed so wording stays aligned with this narrower AC framing.
- Acceptance Criteria: updated — the encrypted-related AC is now limited to explicit capability gating and separation from public bootstrap, not relay-class semantics.
- Tests: future tests should prove that public bootstrap metadata alone does not imply encrypted capability.
- Defects: none created at this stage.
- Confidence / Release: keeps the AC set aligned with current implementation evidence while respecting the product-correct wording constraint.

**Required Follow-Up Actions:**
- [ ] Keep only a minimal encrypted capability-gating AC in `acceptance_criteria.json`.
- [ ] Avoid phrasing encrypted messaging support as a relay category in downstream PSTF artifacts.

---

## Summary

- Final Status: APPROVED
- Open Questions: 0
- Accepted Risks: 0
- Deferred Items: 0
- Blocking Issues: 0
- Superseded Decisions: 0

## Traceability

| Decision ID | Affects ACs | Affects Tests | Affects Defects | Affects Artifacts | Notes |
| --- | --- | --- | --- | --- | --- |
| HITL-SYSTEM_DISCOVERY_RELAY_BOOTSTRAP-001 | future ACs for sidecar-first discovery bootstrap | future system-info and bootstrap contract tests | likely future legacy-cleanup defect | feature_spec.json | Removes direct `nostr.relays` exposure from the intended slice |
| HITL-SYSTEM_DISCOVERY_RELAY_BOOTSTRAP-002 | full AC set | future discovery/bootstrap tests | none | acceptance_criteria.json | AC set approved only after applying requested wording edit |
| HITL-SYSTEM_DISCOVERY_RELAY_BOOTSTRAP-003 | SDRB-AC-009 | future encrypted-capability discovery tests | none | acceptance_criteria.json | Keeps only minimal encrypted capability gating, not relay-class semantics |
