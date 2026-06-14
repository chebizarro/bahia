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

## Epic 2 implementation evidence

Epic 2 (`bahia-65q8.2`) implements the durable Security OSV foundation only: domain types, additive migration `000044_security_osv`, repository APIs, canonical target identity, and OSV adapter expansion. Scanner runtime, SBOM observable subscriptions, ContextVM handlers, policy gates, schedules runtime, notifications, compatibility aggregate updates, and user-facing documentation remain assigned to subsequent epics.

Implemented evidence on 2026-06-14:

- `internal/domain/security.go` defines Security scan targets, scan runs, latest target state, OSV findings, scan schedules, policy breaches, OSV cache records, observable publication retry records, severity counts, lifecycle enums, and terminal/success helpers.
- Canonical target identity helpers derive deterministic keys and SHA-256 lower-hex hashes for SBOM, package, PURL, and commit targets, including package empty-version representation, PURL normalization via `github.com/anchore/packageurl-go`, and deterministic commit keys with `unknown` repository fallback.
- `internal/db/migrations/000044_security_osv.up.sql` adds additive Security tables and indexes for targets, runs, latest state, findings, schedules, breach fingerprints, OSV hydrated vulnerability cache, and observable publication retry state. The paired down migration drops only these new tables.
- `internal/repository/pg_security.go` and `internal/repository/interfaces.go` provide Security repository APIs for target upsert/list, run creation/status transition, target latest upsert, finding upsert/list, due schedule claiming/dispatch, breach lifecycle, OSV cache retention, and retryable publication queries. `TxRepos` includes the Security repository for subsequent transactional scanner writes.
- `internal/adapters/security/osv.go` preserves `NewOSVClient()` and `QueryPackage(ctx, ecosystem, name, version)` compatibility while adding batch query ordering, PURL and commit queries, hydration, chunking, dedupe/cache behavior, typed retryable/non-retryable OSV errors, `Retry-After` parsing, and deterministic retry seams.

Epic 2 deterministic checks:

```bash
go test ./internal/domain ./internal/adapters/security ./internal/repository
go test ./internal/service
go test ./internal/db
```

Result: PASS on 2026-06-14.

Mapped Epic 2 coverage:

- `SECURITY-T-004` / `SECURITY-AC-005`: PASS for target key/hash derivation in `internal/domain/security_test.go`.
- `SECURITY-T-005` / `SECURITY-AC-007`: PASS for OSV adapter batch ordering, PURL version rules, commit lookup shape, hydration, 404/429/5xx handling, cache/dedupe behavior, chunking, and retry behavior in `internal/adapters/security/osv_test.go`.
- Epic 2 persistence acceptance: PASS for repository create/update/list primitives, terminal scan-run transition refusal, finding uniqueness, due schedule lease claiming, breach lifecycle, expired OSV cache behavior, cache pruning, and retryable publication selection in `internal/repository/pg_security_test.go`.


## Remaining tracked work

Remaining implementation and review work is tracked in Beads under parent epic `bahia-65q8`; no implementation work is hidden in this report.
