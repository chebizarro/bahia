# Security OSV SBOM — Final Review Report

**Date**: 2026-06-14  
**Reviewer**: Claude (automated review gate)  
**Plan**: `docs/plans/security-osv-sbom-2026-06-14.md`  
**PSTF**: `pstf/features/SECURITY_OSV_SBOM/`  
**Beads epic**: `bahia-65q8.6`

## Summary

All five implementation epics **PASS** the final review gate. The implementation conforms to the original plan's Nostr contract, PSTF acceptance criteria, production-readiness constraints, and documentation requirements. One small evidence gap (test matrix status labels) was fixed during this review.

---

## Epic 1 — Protocol, PSTF, and Beads Foundation

**Verdict: PASS**

| Checkpoint | Status | Evidence |
|---|---|---|
| PSTF artifacts present | ✅ | `feature_spec.json`, `acceptance_criteria.json`, `test_matrix.json`, `verification_report.md`, `hitl_decisions.md`, `defects.json` — all valid |
| Nostr contract documented | ✅ | `docs/event-spec.md` §Security OSV/SBOM Events; kinds 25910, 30315, 30900, 30078, 4903 |
| ContextVM methods specified | ✅ | `security/scan`, `security/rescan`, `security/findings-list`, `security/schedules-list` in event-spec, control-planes, nostr-commands |
| EOSE/CLOSED/AUTH/OK expectations | ✅ | Documented in event-spec, control-planes, nostr-integration |
| Protocol compatibility | ✅ | `docs/protocol-compatibility.md` lists Security OSV/SBOM contract |
| No new Nostr kind allocated | ✅ | Uses existing canonical kinds only |
| 17/17 acceptance criteria mapped to tests | ✅ | `test_matrix.json` coverage_summary confirms |
| HITL decisions recorded | ✅ | SECURITY-HITL-001 (OSV cache retention), SECURITY-HITL-002 (scan volume) |
| Beads issues exist for implementation | ✅ | bahia-65q8.1–bahia-65q8.5 epics with child tasks |

**Gaps found**: None.

---

## Epic 2 — Domain Model, Persistence, Target Identity, OSV Adapter

**Verdict: PASS**

| Checkpoint | Status | Evidence |
|---|---|---|
| Domain types defined | ✅ | `internal/domain/security.go` (383 lines): SecurityScanTarget, SecurityScanRun, SecurityFinding, SecurityScanSchedule, SecurityPolicyBreach, SecurityOSVCache, SecurityObservablePublication, severity/status/publication enums |
| Target identity helpers | ✅ | `CanonicalTargetHash()` with SHA-256 lower-hex; deterministic keys for SBOM, package, PURL (via `packageurl-go`), commit targets matching plan format |
| Additive migration | ✅ | `000044_security_osv.up.sql` (179 lines): 8 tables — `security_scan_targets`, `security_scan_runs`, `security_target_latest`, `security_findings`, `security_scan_schedules`, `security_policy_breaches`, `security_osv_vulnerability_cache`, `security_observable_publications` |
| Rollback migration | ✅ | `000044_security_osv.down.sql`: drops 8 tables in reverse dependency order |
| Rollback safety | ✅ | All tables are additive; old code ignores Security rows/events |
| Repository APIs | ✅ | `internal/repository/pg_security.go` (43KB): target upsert, run create/transition, finding upsert, due schedule claiming, breach lifecycle, OSV cache, publication retry |
| SecurityRepository interface | ✅ | `internal/repository/interfaces.go` includes SecurityRepository; `TxRepos` includes Security |
| OSV adapter expanded | ✅ | `internal/adapters/security/osv.go` (20KB): `QueryBatch`, `QueryPURL`, `QueryCommit`, `HydrateVulnerability`, chunking, dedupe, retryable/non-retryable `OSVError`, `Retry-After` parsing, backward-compatible `QueryPackage` |
| Tests pass | ✅ | `security_test.go`, `osv_test.go`, `pg_security_test.go` — all pass |
| No stubs/placeholders | ✅ | grep clean |

**Plan conformance notes**:
- Target key formats match plan: `sbom:<...>`, `package:<ecosystem>:<name>:<version>`, `purl:<normalized>`, `commit:<repo_hash>:<commit_hash>`
- OSV normalization rules followed: PURL preferred, version-in-PURL not duplicated, CPE-only skipped with reason, dedupe before batch, query-result index preserved
- Active-run dedupe via partial unique index on `(target_key_hash) WHERE status IN ('accepted','running')`
- `withdrawn_at` on findings preserves withdrawn advisories per plan

**Gaps found**: None.

---

## Epic 3 — Scanner Runtime, SBOM Observable Ingestion, ContextVM Surfaces

**Verdict: PASS**

| Checkpoint | Status | Evidence |
|---|---|---|
| Scanner orchestration | ✅ | `internal/service/security_scanner.go` (51KB): durable scan submission, active-run dedupe, SBOM payload SHA-256 verification, package/PURL normalization, OSV batch calls, finding persistence, latest target updates, failure/cancellation terminal states |
| Observable publication | ✅ | Publishes 30315 status, 30900 summaries, 30078 findings, 4903 audit with relay OK verification and publication retry state |
| SBOM observable ingestion | ✅ | Subscribes to 30078 (`#domain=sbom`) and 30004 with EOSE processing, CLOSED/AUTH handling via `AuthenticateRelay`, reconnect with backoff |
| No polling/sleep | ✅ | No `time.Sleep` or timer-based event delivery polling found; `SubscribeAllWithEOSE` pattern used |
| ContextVM handlers | ✅ | `internal/controlplane/security_handlers.go`: 4 methods registered — `security/scan`, `security/rescan`, `security/findings-list`, `security/schedules-list`; mutations return acknowledgment-only; reads expose persisted state |
| App wiring | ✅ | `internal/app/app.go`: PgSecurityRepository, SecurityScanner with background registration (Tier3), SecurityScheduler, ContextVM handler registration |
| Target validation | ✅ | Handlers validate target type, required fields (subject, format, storage, SHA-256, reference d-tag for SBOM; ecosystem+name for package; PURL string; commit hash) |
| Tests pass | ✅ | `security_scanner_test.go` (30KB), `security_handlers_test.go` (5KB) — all pass |

**Plan conformance notes**:
- Scanner does not run inline in `SBOMOrchestrator.run` — separate event-driven service
- Scan execution never blocks SBOM import
- docs/nostr-commands.md and docs/control-planes.md updated for Security surfaces
- REST compatibility explicitly not added per Epic 3 decision, documented

**Gaps found**: None.

---

## Epic 4 — Policy Config/Gates, Schedules, Breach Notifications, Compatibility Aggregates

**Verdict: PASS**

| Checkpoint | Status | Evidence |
|---|---|---|
| Policy rule surface | ✅ | `RuleSecurityOSVScan` in `domain/policy.go`; `checkSecurityOSVScan` in `service/policy.go` with enabled, freshness_seconds, no_scan/stale/failed block/warn/pass behavior |
| No synchronous scan-and-wait | ✅ | Policy gates read latest completed Security scan; do not trigger or wait for new scans |
| Existing threshold preference | ✅ | `max_critical_vulns`/`max_high_vulns`/`require_scan_status` prefer Security counts, fall back to SBOM aggregates |
| Schedule derivation | ✅ | `enabledSecurityOSVScanRules` extracts enabled rules; `DeriveSecurityScanSchedules` creates/updates/disables schedules from policy |
| Scheduler runtime | ✅ | `internal/service/security_scheduler.go` (4KB): startup tick, cadence ticks, due schedule claiming, active duplicate suppression, next-due recording, no event-delivery timers |
| Breach fingerprinting | ✅ | Deterministic fingerprints in `security_scanner.go`; new/changed breaches dispatch `security.policy_breached`; unchanged recurring breaches suppressed; resolved breaches marked without notification |
| Event type wired | ✅ | `EventSecurityPolicyBreached` in `internal/events/events.go`; wired in `dispatcher.go` subscription list |
| Notification dispatcher test | ✅ | `TestDispatcher_SetupSubscriptionsDispatchesSecurityPolicyBreached` in `dispatcher_test.go` |
| Compatibility aggregates | ✅ | `UpdateCompatibilityVulnerabilityCounts` in `pg_sbom.go` updates `artifact_sboms` and `sbom_manifests` counts without mutating canonical SBOM events |
| Documentation | ✅ | `policies.md` (security_osv_scan rule, schedule, freshness), `notifications.md` (security.policy_breached), `artifacts.md` (compatibility projection note) |
| Tests pass | ✅ | policy_test.go, security_scanner_test.go, pg_security_test.go, dispatcher_test.go — all pass |

**Plan conformance notes**:
- Breach fingerprint inputs match plan: `policy_id + target_key_hash + enforcement + violated_rules + severity_counts + sorted_osv_ids`
- Breach comparison record stores: current fingerprint, previous fingerprint, enforcement, violated_rules, severity counts, osv_ids, first/last-seen, resolved_at, notification_status — matches plan spec
- Channel filters can target `security.policy_breached` without new channel types
- Validation covers interval_seconds ≥ 0, freshness_seconds ≥ 0, no_scan/stale/failed ∈ {block,warn,pass}

**Gaps found**: None.

---

## Epic 5 — Documentation, Quality Gates, Migration/Rollback, Release Evidence

**Verdict: PASS**

| Checkpoint | Status | Evidence |
|---|---|---|
| Security feature docs | ✅ | `docs/user-guide/features/security.md` (468 lines): SBOM-triggered scans, explicit scans, schedules, policy integration, breach notifications, Nostr observables, failure handling, migration/rollback, troubleshooting |
| Troubleshooting docs | ✅ | `docs/user-guide/troubleshooting.md` §Security OSV Scanning Issues |
| MCP tools docs | ✅ | `docs/user-guide/mcp-tools.md` §Security Tools — documents ContextVM-only approach |
| Go tests pass | ✅ | domain, adapters/security, repository, service, controlplane, notifications, events, app — all pass |
| go vet clean | ✅ | No issues |
| No stubs/placeholders | ✅ | grep scan of all 7 production files clean (only legitimate SQL `placeholders` variable) |
| Migration forward | ✅ | 000044_security_osv.up.sql: 8 additive tables, no existing table modifications |
| Migration rollback | ✅ | 000044_security_osv.down.sql: drops 8 tables in reverse dependency order |
| Rollback safety | ✅ | Old code ignores Security rows/events; new code falls back to SBOM aggregates |
| HITL decisions | ✅ | SECURITY-HITL-001 and SECURITY-HITL-002 documented, no blockers |
| Defects | ✅ | `defects.json` empty — no defects found |
| Test matrix coverage | ✅ | 11/11 entries `implemented_passed`; 17/17 acceptance criteria mapped |

**Evidence gap fixed during review**: Test matrix entries T-001 through T-006 were `planned` despite all underlying tests existing and passing. Updated to `implemented_passed`. Verification report count corrected from "7/11" to "11/11".

---

## Cross-Cutting Verification

### Nostr/PSTF Constraints

| Constraint | Status |
|---|---|
| No new Bahia-specific Nostr kind | ✅ Uses 25910, 30315, 30900, 30078, 4903 |
| ContextVM responses are acknowledgments only | ✅ Handlers return intent ack; terminal truth from observables |
| Relay OK verification required | ✅ Publication retry state tracked per observable |
| EOSE/CLOSED/AUTH explicit handling | ✅ Scanner subscribes with EOSE, handles CLOSED/AUTH |
| No polling or sleep-based completion | ✅ No time.Sleep in scanner; cadence timers for scheduler only |
| Canonical SBOM events never mutated | ✅ Compatibility updates go to projection tables only |
| Security does not block SBOM import | ✅ Separate event-driven service, not inline in SBOMOrchestrator |

### Production-Readiness Constraints

| Constraint | Status |
|---|---|
| No fake/stub/hardcoded/placeholder behavior | ✅ Clean grep across all production files |
| All Go test packages pass | ✅ domain, adapters, repository, service, controlplane, notifications, events, app |
| go vet clean | ✅ No issues |
| Additive-only database migration | ✅ No existing tables modified |
| Rollback-safe schema | ✅ Old code ignores new tables; fallback to SBOM counts preserved |

### Quality Gates Run

```
go test ./internal/domain/...                   → ok (cached)
go test ./internal/adapters/security/...        → ok (cached)
go test ./internal/repository/...               → ok (cached)
go test ./internal/service/...                  → ok (cached)
go test ./internal/controlplane/...             → ok (cached)
go test ./internal/notifications/...            → ok (cached)
go test ./internal/events/...                   → ok (cached)
go test ./internal/app/...                      → ok (cached)
go vet ./internal/...                           → clean
```

---

## Defects Found

**None.** No new Beads defects created during this review.

---

## Evidence Changes Made During Review

1. **Test matrix status update**: `pstf/features/SECURITY_OSV_SBOM/test_matrix.json` — T-001 through T-006 updated from `planned` to `implemented_passed`
2. **Verification report count fix**: `pstf/features/SECURITY_OSV_SBOM/verification_report.md` — corrected "7/11" to "11/11" and updated T-001–T-006 explanation

---

## Beads Status

| Issue | Description | Verdict |
|---|---|---|
| `bahia-65q8.6.1` | Review Epic 1 | **PASS** → closed |
| `bahia-65q8.6.2` | Review Epic 2 | **PASS** → closed |
| `bahia-65q8.6.3` | Review Epic 3 | **PASS** → closed |
| `bahia-65q8.6.4` | Review Epic 4 | **PASS** → closed |
| `bahia-65q8.6.5` | Review Epic 5 | **PASS** → closed |
| `bahia-65q8.6` | Final Review Epic | **PASS** → closed |

---

## Conclusion

The Security OSV SBOM feature implementation is complete and conforms to the original plan across all dimensions: Nostr contract, domain model, persistence, OSV adapter, scanner runtime, SBOM observable ingestion, ContextVM surfaces, policy integration, scheduled rescans, breach notifications, compatibility aggregates, documentation, quality gates, and migration/rollback safety. No behavioral risks or implementation gaps were identified. The feature is ready for operator deployment.
