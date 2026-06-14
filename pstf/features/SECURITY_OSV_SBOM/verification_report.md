# SECURITY_OSV_SBOM Verification Report

## Status

Epic 1 contract foundation is complete when Beads `bahia-65q8.1.1`, `bahia-65q8.1.2`, and `bahia-65q8.1.3` are closed. Implementation verification remains pending under the child epics `bahia-65q8.2` through `bahia-65q8.6`.

## Contract evidence

- Security uses existing canonical Nostr kinds: ContextVM `25910` for mutation/read intent acknowledgments, `30315` for operational scan status, `30900` for scan and target summaries, `30078` for normalized finding app data, `4903` for audit facts, and existing SBOM `30004` availability lists as scan triggers.
- ContextVM responses are acknowledgments only; clients subscribe to scoped Security observables and process `EOSE`, `CLOSED`, and `AUTH` outcomes explicitly.
- Relay `OK` verification is required before Security observables are considered published.
- Policy breaches are represented as audit facts plus the internal notification event type `security.policy_breached`, dispatched only for new or materially changed breach fingerprints.

## Epic 1 checks

```bash
python3 -m json.tool pstf/features/SECURITY_OSV_SBOM/feature_spec.json >/tmp/security_feature_spec.json.ok
python3 -m json.tool pstf/features/SECURITY_OSV_SBOM/acceptance_criteria.json >/tmp/security_acceptance_criteria.json.ok
python3 -m json.tool pstf/features/SECURITY_OSV_SBOM/test_matrix.json >/tmp/security_test_matrix.json.ok
python3 -m json.tool pstf/features/SECURITY_OSV_SBOM/defects.json >/tmp/security_defects.json.ok
test -f pstf/features/SECURITY_OSV_SBOM/verification_report.md
test -f pstf/features/SECURITY_OSV_SBOM/hitl_decisions.md
```

Result: PASS on 2026-06-13.

```bash
grep -RInE 'target[_]hash|TODO|FIXME|XXX|HACK|TEMP|WIP|for now|future work|MVP|simplified|minimal|TBD' docs/event-spec.md docs/control-planes.md docs/nostr-commands.md docs/nostr-event-implementation-guide.md docs/user-guide/nostr-integration.md docs/protocol-compatibility.md pstf/features/SECURITY_OSV_SBOM || true
```

Result: PASS for Epic 1 changes on 2026-06-13. The only hit is the pre-existing implementation-guide checklist line that instructs contributors to create Beads instead of leaving TODOs.

```bash
git diff --check
! grep -RIn 'target[_]hash' docs/event-spec.md docs/control-planes.md docs/nostr-commands.md docs/nostr-event-implementation-guide.md docs/user-guide/nostr-integration.md docs/protocol-compatibility.md pstf/features/SECURITY_OSV_SBOM
```

Result: PASS on 2026-06-13.

Oracle review: completed for the uncommitted Epic 1 diff. Blocking findings were addressed by adding the `op=scan` intent tag convention, standardizing on `target_key_hash`, and adding explicit PSTF coverage for scheduled rescans plus compatibility aggregate projections.

## Implementation verification plan

- Epic 2 must prove domain persistence, target-key determinism, OSV adapter behavior, and repository APIs.
- Epic 3 must prove scanner orchestration, SBOM observable ingestion, ContextVM handlers, EOSE/CLOSED/AUTH handling, OK verification, and absence of timeout-based completion.
- Epic 4 must prove policy-scoped configuration, scheduled rescans, breach fingerprints, notifications, and compatibility aggregate updates.
- Epic 5 must record full Go/web/integration quality gates, user/operator documentation, migration/rollback notes, and release evidence.

## Remaining tracked work

Remaining implementation and review work is tracked in Beads under parent epic `bahia-65q8`; no implementation work is hidden in this report.
