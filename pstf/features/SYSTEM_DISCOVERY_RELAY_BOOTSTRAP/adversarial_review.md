# Adversarial Review – SYSTEM_DISCOVERY_RELAY_BOOTSTRAP

## Recommendation
block_until_major_findings_resolved

## Overall Risk
high

## Findings
### ADV-001 – Confidence gate is still blocked by missing touched-module coverage evidence
- Severity: major
- Category: test_gap
- Evidence:
  - `pstf/features/SYSTEM_DISCOVERY_RELAY_BOOTSTRAP/confidence_report.json` sets status to `needs_more_tests`, raw/final confidence to `0.85`, and threshold to `0.90`.
  - The same report scores `code_coverage` at `0.0` and records the explicit evidence gap: no Go or web coverage report exists under `pstf/features/SYSTEM_DISCOVERY_RELAY_BOOTSTRAP/`.
  - The feature folder contains no `coverage/` directory or equivalent stored coverage artifact.
- Affected ACs: `SDRB-AC-001`–`SDRB-AC-010`
- Affected tests: none directly; this is a release-evidence gap
- Suggested action: generate touched-module Go/web coverage artifacts and rerun confidence scoring before final human review
- Requires HitL decision: no

### ADV-002 – Operator settings still depend on a removed `systemInfo.nostr.relays` field with no replacement contract or regression proof
- Severity: major
- Category: regression
- Evidence:
  - `internal/adapters/nostr/projector.go` no longer populates `nostr.relays`, and the approved slice removed raw `nostr.relays` from the intended discovery contract.
  - `web/src/routes/settings/+page.svelte` still renders `systemInfo.nostr.relays` as the operator-visible “Server Relays” setting.
  - No settings-page regression test was found covering the disappearance or replacement of that field after the discovery cleanup.
  - `HITL-SYSTEM_DISCOVERY_RELAY_BOOTSTRAP-001` removed `nostr.relays` from the bootstrap contract, but did not decide whether operator-facing relay visibility should still exist via another field.
- Affected ACs: `SDRB-AC-001`, `SDRB-AC-010`
- Affected tests: none
- Suggested action: get a product decision on operator relay visibility, then either remove the stale UI dependency or add a replacement contract plus regression coverage
- Requires HitL decision: yes

### ADV-003 – Several PSTF artifacts still describe superseded behavior, which weakens traceability for human review
- Severity: minor
- Category: spec_gap
- Evidence:
  - `feature_spec.json` still describes the removed raw-`nostr.relays` behavior as current observed behavior.
  - `feature_spec.json` still lists the missing multi-consumer contract test as a gap even though `SDRB-T-011` now passes.
  - `test_matrix.json` marks `SDRB-T-012` as pass, but its descriptive text still says it is a blocked manual gate waiting on `bahia-u6e1`.
  - `hitl_decisions.md` metadata still says `Current Stage: acceptance_criteria`.
- Affected ACs: `SDRB-AC-001`, `SDRB-AC-009`, `SDRB-AC-010`
- Affected tests: `SDRB-T-011`, `SDRB-T-012`
- Suggested action: refresh stale PSTF artifact text before final human review
- Requires HitL decision: no

## Suggested HitL Questions
- How should the operator settings page handle server relay visibility after raw `nostr.relays` removal?
  - Expose backend relay configuration through a new explicit operator-only field and test it
  - Remove server relay visibility from the settings UI and treat it as out of scope for this release
  - Keep current settings behavior by restoring `nostr.relays` for operator surfaces only
  - Defer the settings-page decision into a dedicated follow-up slice before release

## Next Recommended Stage
resolve_major_findings_then_rerun_confidence_or_hitl_review
