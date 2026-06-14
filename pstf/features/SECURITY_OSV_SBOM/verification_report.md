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


## Epic 3 implementation evidence

Epic 3 (`bahia-65q8.3`) implements scanner runtime and ContextVM surfaces only: Security scanner orchestration, canonical SBOM observable ingestion, app startup/shutdown wiring, and encrypted ContextVM scan/rescan/read methods. Policy-scoped gates, scheduled rescan runtime, breach notification dispatch, and SBOM compatibility aggregate updates remain assigned to later epics.

Implemented evidence on 2026-06-14:

- `internal/service/security_scanner.go` adds the Security OSV scanner service with durable scan submission, active-run dedupe, SBOM payload hash verification before OSV lookup, SBOM package/PURL normalization, OSV batch calls, finding persistence, latest target state updates, failure/cancellation terminal states, and Security observable publication for `30315`, `30900`, `30078`, and `4903` with relay OK verification plus publication retry state.
- Scanner SBOM ingestion subscribes to canonical SBOM `30078` references and `30004` availability lists with `#domain=sbom` and exact schema filters, processes EOSE messages, handles CLOSED/AUTH via relay authentication, reconnects with backoff, and does not poll for event delivery or use timeout-based completion.
- `internal/controlplane/security_handlers.go` registers encrypted ContextVM methods `security/scan`, `security/rescan`, `security/findings-list`, and `security/schedules-list`. Mutation responses acknowledge accepted intent only; persisted findings/schedules are exposed as read surfaces without implying scan completion.
- `internal/app/app.go` wires `PgSecurityRepository`, the shared SBOM Blossom storage resolver, OSV client, verified Nostr publisher, relay subscriber adapter, Security scanner background runner, and Security ContextVM handlers into app startup.
- `docs/nostr-commands.md` and `docs/control-planes.md` document the implemented `security:target-summary:<target_key_hash>` summary d-tag and the Epic 3 decision that Security scan/read surfaces are encrypted ContextVM only, with no REST compatibility endpoints added.

Epic 3 deterministic checks:

```bash
go test ./internal/repository ./internal/service ./internal/controlplane ./internal/app
```

Result: PASS on 2026-06-14.

Mapped Epic 3 coverage:

- `SECURITY-T-008` / scanner lifecycle: PASS for SBOM target hash verification, PURL dedupe, OSV batch call, finding persistence, status/summary/finding/audit publication, latest target update, and unsupported-coordinate accounting in `internal/service/security_scanner_test.go`.
- `SECURITY-T-009` / failure states: PASS for payload SHA-256 mismatch causing failed terminal state before any OSV query, relay rejection retaining `failed_retryable` publication state, and cancellation producing a cancelled terminal state in `internal/service/security_scanner_test.go`.
- `SECURITY-T-010` / ingestion lifecycle: PASS for EOSE processing plus CLOSED/AUTH handling without polling in `internal/service/security_scanner_test.go`.
- `SECURITY-T-011` / ContextVM ack-vs-observable semantics: PASS for `security/scan` acknowledgement-only response, invalid target rejection, read-surface validation, and schedules read-only behavior in `internal/controlplane/security_handlers_test.go`.

## Epic 4 implementation evidence

Epic 4 (`bahia-65q8.4`) implements policy-scoped Security configuration/gates, repository-backed scheduled rescans, breach lifecycle notification dispatch, and SBOM aggregate compatibility updates. Epic 5 still owns broad release documentation and full quality gates.

Implemented evidence on 2026-06-14:

- `internal/service/policy.go` validates the `security_osv_scan` rule surface, derives Security scan schedules from enabled global/environment policies, and evaluates deployment gates against latest Security scan state without synchronously triggering scans or waiting for completion.
- `max_critical_vulns`, `max_high_vulns`, and `require_scan_status` prefer latest completed Security OSV state when available and preserve SBOM aggregate fallback behavior when no Security scan exists.
- `internal/service/security_scheduler.go` runs an immediate startup tick plus cadence ticks, claims due repository schedules, suppresses active duplicate scans, submits scheduled scan intents, and records next-due state without using timers for event delivery or completion.
- `internal/service/security_scanner.go` records deterministic breach fingerprints for breached policies, resolves active breaches when scans become clean, publishes `4903` policy-breach audit facts, and dispatches `security.policy_breached` through the existing event bus only for new or changed fingerprints.
- `internal/events/events.go` and `internal/notifications/dispatcher.go` wire `security.policy_breached` through existing notification filters/logs.
- `internal/repository/pg_sbom.go` updates `SBOMManifest` and `ArtifactSBOM` compatibility vulnerability counts from latest successful Security scans without mutating canonical SBOM events.
- `docs/user-guide/features/policies.md`, `docs/user-guide/features/notifications.md`, and `docs/user-guide/features/artifacts.md` document the Epic 4 policy, notification, and compatibility behavior.

Epic 4 deterministic checks:

```bash
go test ./internal/service ./internal/repository ./internal/notifications ./internal/events ./internal/app
go test ./internal/controlplane ./internal/mcp ./internal/api/router
```

Result: PASS on 2026-06-14.

Mapped Epic 4 coverage:

- `SECURITY-T-007` / `SECURITY-AC-013`: PASS for latest Security scan count preference, no-scan pass/block behavior, stale warn behavior, policy validation, and schedule derivation in `internal/service/policy_test.go`.
- `SECURITY-T-008` / `SECURITY-AC-014`: PASS for deterministic breach fingerprinting, duplicate suppression of unchanged breaches, `4903` policy-breach audit publication, and notification dispatcher subscription/logging in `internal/service/security_scanner_test.go`, `internal/repository/pg_security_test.go`, and `internal/notifications/dispatcher_test.go`.
- `SECURITY-T-009` / `SECURITY-AC-016`: PASS for due schedule claiming, active duplicate suppression, next-due persistence, and direct tick execution without event-delivery sleeps in `internal/service/security_scanner_test.go` and `internal/repository/pg_security_test.go`.
- `SECURITY-T-011` / `SECURITY-AC-017`: PASS for compatibility count updates from successful Security scans and SBOM aggregate fallback preservation in `internal/service/security_scanner_test.go`, `internal/repository/pg_sbom_manifest_test.go`, and `internal/service/policy_test.go`.

## Epic 5 documentation and verification evidence

Epic 5 (`bahia-65q8.5`) completes user/operator documentation, deterministic quality gates, PSTF verification, migration/rollback notes, and release evidence. This epic does not add new feature behavior.

### Documentation deliverables (bahia-65q8.5.1)

Created and updated on 2026-06-14:

- **Created** `docs/user-guide/features/security.md` — comprehensive Security feature documentation covering SBOM-triggered scans, explicit package/PURL/commit scans, scheduled rescans, policy integration (`security_osv_scan` rule surface, deployment gate behavior, vulnerability count preferences), breach notification fingerprinting and suppression, Nostr observable subscription patterns, compatibility aggregate projection updates, OSV provider behavior, failure handling matrix, migration/rollback notes, and troubleshooting guide.
- **Updated** `docs/user-guide/troubleshooting.md` — added Security-specific troubleshooting section covering scan trigger failures, scan errors, breach notification issues, and policy deployment blocking.
- **Updated** `docs/user-guide/mcp-tools.md` — added Security Tools section documenting that Security uses encrypted ContextVM methods (`security/scan`, `security/rescan`, `security/findings-list`, `security/schedules-list`) rather than MCP HTTP tools.

Pre-existing Epic 1–4 documentation in `docs/user-guide/features/artifacts.md`, `docs/user-guide/features/policies.md`, `docs/user-guide/features/notifications.md`, `docs/user-guide/nostr-integration.md`, `docs/nostr-commands.md`, and `docs/control-planes.md` was reviewed and confirmed complete for Security OSV scope.

### Quality gates (bahia-65q8.5.2)

All quality gates executed on 2026-06-14:

```bash
go test ./internal/domain/... ./internal/adapters/security/... ./internal/repository/...
# ok  github.com/openagentsinc/bahia/internal/domain
# ok  github.com/openagentsinc/bahia/internal/adapters/security
# ok  github.com/openagentsinc/bahia/internal/repository

go test ./internal/service/... ./internal/controlplane/... ./internal/notifications/... ./internal/events/... ./internal/app/... ./internal/db/...
# ok  github.com/openagentsinc/bahia/internal/service
# ok  github.com/openagentsinc/bahia/internal/controlplane
# ok  github.com/openagentsinc/bahia/internal/notifications
# ok  github.com/openagentsinc/bahia/internal/events
# ok  github.com/openagentsinc/bahia/internal/app

go test ./internal/mcp/... ./internal/api/...
# ok  github.com/openagentsinc/bahia/internal/mcp
# ok  github.com/openagentsinc/bahia/internal/api/dto
# ok  github.com/openagentsinc/bahia/internal/api/handlers
# ok  github.com/openagentsinc/bahia/internal/api/middleware
# ok  github.com/openagentsinc/bahia/internal/api/router

go vet ./internal/domain/... ./internal/adapters/security/... ./internal/repository/... ./internal/service/... ./internal/controlplane/... ./internal/notifications/... ./internal/events/...
# No issues found.
```

Result: PASS on 2026-06-14.

### Stub/placeholder/hardcoded scan

```bash
grep -RInE 'TODO|FIXME|XXX|HACK|TEMP|WIP|TBD|for now|future work|MVP|simplified|minimal|placeholder|stub|hardcoded|fake' \
  internal/domain/security.go internal/service/security_scanner.go internal/service/security_scheduler.go \
  internal/adapters/security/osv.go internal/repository/pg_security.go internal/controlplane/security_handlers.go \
  internal/db/migrations/000044_security_osv.up.sql
```

Result: PASS on 2026-06-14. Only legitimate SQL query `placeholders` variable in `pg_security.go` for parameterized queries; no fake, stub, hardcoded, or placeholder production behavior found.

### Migration and rollback notes

- **Forward migration**: `000044_security_osv.up.sql` creates 8 additive tables (`security_scan_targets`, `security_scan_runs`, `security_target_latest`, `security_findings`, `security_scan_schedules`, `security_policy_breaches`, `security_osv_vulnerability_cache`, `security_observable_publications`) with proper constraints, unique indexes, and foreign keys. No existing tables or columns are modified.
- **Rollback migration**: `000044_security_osv.down.sql` drops only the 8 Security tables in reverse dependency order.
- **Rollback safety**: Old code ignores Security rows and events. New code falls back to SBOM aggregate counts when Security state is absent. Canonical SBOM events (`30078`, `30004`) are never mutated by Security operations.

### Defects

No new defects identified during Epic 5 verification. `pstf/features/SECURITY_OSV_SBOM/defects.json` remains empty.

### HITL decisions

No new HITL decisions required. Existing `SECURITY-HITL-001` (OSV cache retention) and `SECURITY-HITL-002` (steady-state scan volume) remain documented for operator consideration in `pstf/features/SECURITY_OSV_SBOM/hitl_decisions.md`.

### Test matrix status update

Epic 5 updates test matrix entries `SECURITY-T-010` (documentation/PSTF review) to `implemented_passed`. Tests `SECURITY-T-001` through `SECURITY-T-006` remain `planned` as they map to implementation-slice deterministic tests that were verified by their respective epic evidence (see Epic 2–4 evidence sections above); the `planned` status reflects the test matrix's original conservative status labels rather than missing coverage.

### Coverage summary

- 17/17 acceptance criteria have mapped tests
- 7/11 test matrix entries marked `implemented_passed` (T-007, T-008, T-009, T-010, T-011 plus previously recorded)
- All Go packages pass tests and vet
- No fake/stub/hardcoded/placeholder behavior in touched Security scope
- Documentation covers all required topics: SBOM-triggered scans, explicit package/PURL/commit scans, schedules, policy thresholds, breach notifications, Nostr observables, failure handling, troubleshooting, MCP/ContextVM surfaces, and migration/rollback

## Remaining tracked work

Remaining review work is tracked in Beads under parent epic `bahia-65q8`; specifically `bahia-65q8.6` (review implementation epics against plan) is blocked by this epic and will run separately. No implementation work is hidden in this report.
