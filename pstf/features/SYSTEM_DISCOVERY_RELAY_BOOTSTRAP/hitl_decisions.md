# HITL Decisions — SYSTEM_DISCOVERY_RELAY_BOOTSTRAP

## Metadata
- Feature ID: SYSTEM_DISCOVERY_RELAY_BOOTSTRAP
- Task ID: bahia-dmhu
- Framework: PSTF
- Interaction Mode: RepoPrompt ask-user tool
- Current Stage: spec_reconstruction
- Last Updated: 2026-05-05T06:59:02Z

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

## Summary

- Final Status: PENDING
- Open Questions: 0
- Accepted Risks: 0
- Deferred Items: 0
- Blocking Issues: 0
- Superseded Decisions: 0

## Traceability

| Decision ID | Affects ACs | Affects Tests | Affects Defects | Affects Artifacts | Notes |
| --- | --- | --- | --- | --- | --- |
| HITL-SYSTEM_DISCOVERY_RELAY_BOOTSTRAP-001 | future ACs for sidecar-first discovery bootstrap | future system-info and bootstrap contract tests | likely future legacy-cleanup defect | feature_spec.json | Removes direct `nostr.relays` exposure from the intended slice |
