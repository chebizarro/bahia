# Defects — DESIRED_STATE_RUNTIME

## Status: draft

No defects recorded yet. This feature is in pre-implementation specification phase.

---

## Defect Template

When defects are discovered during implementation or verification, record them using this structure:

### DSR-D-NNN — Title

| Field | Value |
|-------|-------|
| **Severity** | critical / major / minor |
| **Status** | open / verified / fixed / wontfix |
| **Related ACs** | DSR-AC-NNN |
| **Related Tests** | DSR-T-NNN |

**Evidence:**
- File references and line numbers showing the defective behavior.

**Suspected root cause:**
Description of why the defect exists.

**Recommended fix:**
Description of the fix approach.

**Requires human decision:** yes / no

---

## Known Pre-Implementation Concerns

These are not defects yet, but areas where the plan identifies elevated risk that should be tracked as implementation begins:

### Concern 1 — Compose directory ownership validation

The current Compose adapter does not validate whether a `compose_dir` is Bahia-owned before writing files. If the ownership gate (DSR-AC-014) is not implemented before any rendering work, existing non-Bahia-owned directories could be overwritten. This should be the first safety check implemented in DSR-WI-05.

**Related ACs:** DSR-AC-014  
**Related risks:** DSR-RISK-001

### Concern 2 — Legacy sibling services without desired-state snapshots

Current environments may have managed services that have never had a desired-state snapshot persisted. The hydration path (DSR-AC-006) must handle this gracefully on the very first full-project render, or services will be silently dropped from generated Compose output.

**Related ACs:** DSR-AC-006  
**Related risks:** DSR-RISK-002

### Concern 3 — Secret exposure in generated Compose env files

Generated `.bahia/env/<service-key>.env` files may contain resolved secret values needed by Docker Compose. Ownership, permissions, cleanup lifecycle, and redaction policy must be tested and documented before production use.

**Related ACs:** DSR-AC-005, DSR-AC-008  
**Related risks:** DSR-RISK-005

### Concern 4 — Deploy request routing to RuntimeLifecycleService

The plan notes an open question about whether `5961` deploy events currently reach `RuntimeLifecycleService` directly or through an intermediate orchestration layer. If an intermediate layer exists, the shared deploy helper (DSR-AC-003) may require additional refactoring.

**Related ACs:** DSR-AC-003  
**Related open questions:** feature_spec.json open_questions[0]
