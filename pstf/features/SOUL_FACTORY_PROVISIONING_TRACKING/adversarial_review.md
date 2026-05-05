# Adversarial Review – SOUL_FACTORY_PROVISIONING_TRACKING

## Recommendation
requires_human_risk_acceptance

## Overall Risk
medium

## Findings
### ADV-001 – The feature does not clear the confidence threshold because touched-module coverage evidence is still missing
- Severity: major
- Category: test_gap
- Evidence:
  - `confidence_report.json` scores the slice at `0.83`, below the default `0.90` threshold.
  - The penalty comes from `code_coverage = 0.0`; no touched-module coverage artifact exists for this feature slice.
  - `verification_report.md` already shows all 14 ACs verified and all 18 mapped tests passing, so this is an evidence-quality gap rather than a known behavior failure.
- Affected ACs: none directly
- Affected tests: none directly
- Suggested action: generate coverage artifacts or accept release explicitly as a documented risk exception.
- Requires HitL decision: yes

### ADV-002 – Soul Gallery stat cards hide zero values, which weakens operator visibility after live updates
- Severity: minor
- Category: ux
- Evidence:
  - The live-update browser journey exposed that the Suspended stat disappears when the count falls to zero.
  - `web/src/lib/components/Card.svelte` renders the value block only when the value is truthy, so `0` is omitted.
  - This follow-up is tracked as `bahia-ijo8`.
- Affected ACs: `SFTP-AC-001`
- Affected tests: `SFTP-T-002`
- Suggested action: render zero-valued stats explicitly.
- Requires HitL decision: no

### ADV-003 – Schema-backed validation for PSTF confidence and adversarial artifacts is still absent
- Severity: minor
- Category: operability
- Evidence:
  - `schemas/confidence_report.schema.json` and `schemas/adversarial_review.schema.json` are not present in the repository.
  - The gate artifacts therefore cannot be validated mechanically against repo-backed schemas.
- Affected ACs: none
- Affected tests: none
- Suggested action: add the missing schema files or canonical JSON definitions in-repo.
- Requires HitL decision: no

## Suggested HitL Questions
- The Soul Factory slice is behaviorally verified but still scores 0.83 because no touched-module coverage artifact exists. How should release proceed?
  - APPROVED_WITH_RISK — ship this slice and accept the missing coverage evidence as a documented release risk
  - NEEDS_WORK — require coverage artifacts before release approval
  - DEFERRED — do not decide release now; revisit after broader release planning
  - REJECTED — do not release this slice in its current state

## Next Recommended Stage
hitl_release_review
