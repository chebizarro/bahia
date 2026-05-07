# Adversarial Review – TRANSPORT_POLICY_GOVERNANCE

## Recommendation
block_until_major_findings_resolved

## Overall Risk
high

## Findings
### CRIT-001 – Core public-flow acceptance criterion is still unproven because accepted relay OK is not asserted anywhere
- Severity: major
- Category: test_gap
- Evidence:
  - `TPG-AC-002` requires at least one accepted relay OK response before completion.
  - `TPG-T-002` is still `not_implemented` because the current public smoke does not record or assert accepted OK responses.
  - `verification_report.md` marks `TPG-AC-002` only `partial`.
  - `web/tests/e2e/service-deployment-public-smoke.spec.js` checks request kinds, relay URLs, and resulting UI state, but never inspects relay OK frames.
- Affected ACs: `TPG-AC-002`
- Affected tests: `TPG-T-002`
- Suggested action: Extend the public harness and smoke test to record publish OK frames and assert that completion does not count without at least one accepted relay OK.
- Requires HitL decision: no

### CRIT-002 – Verification overclaims encrypted completion coverage: relay-close failure path is claimed but not exercised
- Severity: major
- Category: correctness
- Evidence:
  - `TPG-T-014` says the encrypted completion proof should exercise decrypt-failure and relay-close paths.
  - `verification_report.md` cites `TPG-T-014` as part of the basis for passing `TPG-AC-011`.
  - `web/src/lib/nostr/encrypted-controlplane.js` contains an `onClosed` failure branch in `awaitEncryptedResult(...)`.
  - `web/tests/unit/encrypted-controlplane.test.js` contains no `onClosed` coverage and no test that invokes subscription close handling.
- Affected ACs: `TPG-AC-011`
- Affected tests: `TPG-T-014`
- Suggested action: Add explicit unit coverage for relay-close failure handling or stop claiming that relay-close behavior is already verified.
- Requires HitL decision: no

### CRIT-003 – Browser log-transport contract is underspecified, so the planned EventSource-exclusion proof can be gamed by scanning too little
- Severity: major
- Category: spec_gap
- Evidence:
  - `TPG-AC-007` refers to "a first-party browser log experience covered by this transport-policy slice" without enumerating the in-scope surfaces.
  - `TPG-T-009` proposes an "approved browser slice module allowlist", but no such artifact exists and the target contract test file does not exist.
  - `verification_report.md` keeps `TPG-AC-007` at `not_verified`.
  - Without a stable in-scope list, a future contract test could pass by checking only a narrow module subset.
- Affected ACs: `TPG-AC-007`
- Affected tests: `TPG-T-009`
- Suggested action: Define the exact browser log surfaces that count for this feature slice before implementing the contract test.
- Requires HitL decision: yes

### CRIT-004 – Settings relay-visibility path is still proving the wrong transport boundary and remains a live blocker
- Severity: major
- Category: regression
- Evidence:
  - `TPG-AC-010` requires a service-authored kind `30002` relay-list event as the approved source.
  - `web/src/routes/settings/+page.svelte` still renders `systemInfo.nostr.relays` directly in the "Server Relays" row.
  - `defects.json` keeps `TPG-D-002` open at severity `major`.
  - `HITL-TRANSPORT_POLICY_GOVERNANCE-005` classifies the defect as `BLOCKER`, and `verification_report.md` keeps `TPG-AC-010` at `fail` with `TPG-T-012` not implemented.
- Affected ACs: `TPG-AC-010`
- Affected tests: `TPG-T-012`
- Suggested action: Keep this path as a release blocker until the settings page is rewired to the service-authored relay-list event and covered by deterministic e2e proof.
- Requires HitL decision: no

### CRIT-005 – Confidence scoring masks unverified must-level ACs by awarding full AC coverage for linked-but-unproven tests
- Severity: minor
- Category: other
- Evidence:
  - `confidence_report.json` assigns `acceptance_criteria_coverage.score = 1.0` because all 12 ACs have linked tests.
  - `verification_report.md` still marks `TPG-AC-002` and `TPG-AC-004` partial, `TPG-AC-007` and `TPG-AC-012` not verified, and `TPG-AC-010` fail.
  - Five linked required tests remain `not_implemented`.
- Affected ACs: `TPG-AC-002`, `TPG-AC-004`, `TPG-AC-007`, `TPG-AC-010`, `TPG-AC-012`
- Affected tests: `TPG-T-002`, `TPG-T-005`, `TPG-T-009`, `TPG-T-012`, `TPG-T-015`
- Suggested action: Add a supplemental AC-evidence cap or explicit warning so future readers do not confuse full mapping with full proof.
- Requires HitL decision: no

## Suggested HitL Questions
- Which browser log surfaces are in scope for `TPG-AC-007` in this transport-policy slice?
  - Only deployment run-log retrieval surfaces already governed by encrypted log flows
  - All first-party browser log viewers, including any service log pages that still use `streamLogs` / EventSource
  - Only settings/admin log surfaces for this release
  - Defer browser log-surface classification and remove `TPG-AC-007` from this release gate

## Next Recommended Stage
hitl_review_or_test_design_revision
