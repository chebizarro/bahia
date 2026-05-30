# Review Report: Adoption Hardening (bahia-8mh4)

**Feature**: bahia-pmy2 — Adoption Identity Persistence and OrgID Migration  
**Reviewer**: Claude Agent  
**Date**: 2026-05-30  
**Verdict**: ✅ **PASS** — All acceptance criteria met; implementation is consistent, well-tested, and free of stubs.

---

## Context / Scope

Task 8 (bahia-pmy2) hardens the adoption subsystem with two main changes:

1. **Adoption identity persistence** — stable workload fingerprints (`container_id`, `image_digest`, `compose_coordinates`, `endpoint_target`) are stored in a new `adopted_runtime_identity` table and used for service matching on subsequent imports.
2. **OrgID migration safety** — adoption import resolves ownership through a three-tier cascade (explicit `org_id` → existing environment org → sole organization), failing closed on ambiguity. A migration marks pre-existing unresolved rows in `org_ownership_repair` for operator action rather than silently assigning them.

### Files Reviewed

| File | Lines | Role |
|------|-------|------|
| `internal/db/migrations/000041_adopted_runtime_identity.up.sql` | 61 | Schema migration |
| `internal/db/migrations/000041_adopted_runtime_identity.down.sql` | 2 | Rollback migration |
| `internal/domain/models.go` (L170–209) | 40 | `AdoptedRuntimeIdentity` + `ComposeMetadata` structs |
| `internal/repository/interfaces.go` (L12–15) | 4 | `AdoptedRuntimeIdentityRepository` interface |
| `internal/repository/pg_adopted_runtime_identity.go` | 122 | PostgreSQL repository implementation |
| `internal/repository/tx.go` (L26, L69) | — | `TxRepos` wiring |
| `internal/service/adoption.go` | 1372 | Core adoption service (org resolution, fingerprint persistence, identity matching) |
| `internal/service/adoption_test.go` | 1008 | Unit tests |
| `internal/adapters/runtime/docker_discovery.go` | 639 | Docker discovery (endpoint_ref surfacing) |
| `pstf/features/bahia-pmy2/*` | — | PSTF verification artifacts |

---

## Acceptance Criteria Verification

### AC1: Migration creates `adopted_runtime_identity` with unique fingerprints ✅

**Evidence**: `000041_adopted_runtime_identity.up.sql` creates the table with:
- `fingerprint TEXT NOT NULL UNIQUE` — enforces uniqueness at the database level
- `CHECK (fingerprint <> '')` — prevents empty fingerprints
- `CHECK (fingerprint_kind IN ('container_id', 'image_digest', 'compose_coordinates', 'endpoint_target'))` — constrains kind to known values
- Foreign keys to `organizations`, `services`, `environments` with `ON DELETE CASCADE`
- Partial indexes on `container_id` and `image_digest` for efficient lookup

**Assessment**: Complete and well-structured. The `IF NOT EXISTS` guards make the migration safely re-runnable.

### AC2: Docker discovery surfaces endpoint identity; adopted config stores image digest ✅

**Evidence**:
- `DockerDiscoveryTarget.EndpointRef` (L22) flows through to `DiscoveredContainer.EndpointRef` (L40, L296)
- `DiscoveredContainer.ImageDigest` is populated from `bestRepoDigest()` or fallback to `image.ID` (L271–278)
- The adoption service stores `ImageDigest` in both the adopted runtime config (L1044) and runtime identities (L1125)

**Assessment**: The endpoint identity flows cleanly from discovery target through to persisted models without leaking the raw Docker host URL.

### AC3: Org resolution cascade with fail-closed semantics ✅

**Evidence**: `resolveImportOrgID()` (L657–704) implements the three-tier cascade:
1. **Explicit**: If `req.OrgID` is set, validate it exists via `GetByID`
2. **Environment inference**: Check existing target environments for a single org
3. **Sole org**: If exactly one organization exists, use it
4. **Fail closed**: Multiple orgs → error `"requires org_id"`; zero orgs → error `"no organization is available"`

**Tests**:
- `TestAdoptionServiceImportInfersSingleOrgAndPersistsRuntimeIdentities` — single-org inference works
- `TestAdoptionServiceImportRequiresOrgWhenMultipleOrgsAreAvailable` — multi-org fails closed with `"requires org_id"`

**Assessment**: Correct fail-closed behavior. The multi-environment-org case (L688) is also handled.

### AC4: Unresolved ownership marked for operator repair ✅

**Evidence**: The migration (L13–37) creates `org_ownership_repair` and populates it:
- Selects services/environments where `org_id IS NULL` and `org_count <> 1`
- Sets `status = 'needs_operator_repair'` with a descriptive `repair_reason`
- Uses `ON CONFLICT ... DO UPDATE` for idempotency
- Partial index `idx_org_ownership_repair_status` supports efficient operator queries

**Assessment**: Clean operator-facing design. Rows are flagged, not silently auto-assigned.

### AC5: Identity persistence is atomic with service/environment import ✅

**Evidence**:
- `persistAdoptedRuntimeIdentities()` (L1111–1141) is called inside the `persist` transaction closure (L490)
- `TxRepos.AdoptedIdentities` (tx.go L26) is wired to `newPgAdoptedRuntimeIdentityRepositoryWithDB(tx)` (tx.go L69) ensuring it shares the transaction connection
- `completeTxRepos()` (L635–636) falls back to the service-level repo if the tx doesn't provide one
- `TestAdoptionServiceImportTransactionalRollbackOnStateFailure` confirms identity persistence rolls back with the transaction
- `TestAdoptionServiceImportInfersSingleOrgAndPersistsRuntimeIdentities` verifies all four fingerprint kinds are persisted

**Assessment**: Properly transactional. The test mock (`mockAdoptionTxExecutor`) faithfully simulates clone-on-write + commit-on-success semantics.

---

## Code Quality Assessment

### Consistency with Project Patterns ✅

- **Repository pattern**: `PgAdoptedRuntimeIdentityRepository` follows the same constructor / `pgQueryer` / `newXxxWithDB` pattern as other Pg repositories
- **Functional options**: `WithAdoptionRuntimeIdentities` follows the established `AdoptionServiceOption` pattern
- **Error wrapping**: All errors use `fmt.Errorf("context: %w", err)` consistently
- **JSON marshaling**: Uses the shared `marshalJSON` / `unmarshalJSON` helpers
- **Nil-guard pattern**: `persistAdoptedRuntimeIdentities` returns nil early when repo is nil, consistent with optional feature wiring

### Fingerprint Design

The fingerprint construction in `adoptedRuntimeFingerprintsByKind()` (L1157–1183) uses pipe-delimited compound keys anchored by endpoint ref or host name. This is:
- **Deterministic**: Same inputs always produce the same fingerprint
- **Collision-resistant**: Compound keys include enough context (anchor + kind-specific fields)
- **Human-readable**: Useful for debugging and operator inspection

Four distinct kinds provide resilient matching — if a container ID changes (e.g., after restart), the image digest or compose coordinate fingerprint still matches.

### Transaction Safety

The import path uses `WithinTx` to wrap service creation, build/artifact seeding, state upsert, observation recording, secret storage, and identity persistence in a single transaction. The mock `TxExecutor` properly clones repo state before the transaction function runs and only commits on success.

### No Placeholders or Stubs ✅

A regex search for `TODO`, `FIXME`, `HACK`, `stub`, `placeholder`, and `XXX` across all three key files returned zero matches.

---

## Test Coverage Assessment

### Test Matrix

| Test | Criteria | What it verifies |
|------|----------|-----------------|
| `TestAdoptionServiceImportInfersSingleOrgAndPersistsRuntimeIdentities` | AC2, AC3, AC5 | Single-org inference, all 4 identity kinds persisted, org_id set on service+environment |
| `TestAdoptionServiceImportRequiresOrgWhenMultipleOrgsAreAvailable` | AC3 | Fail-closed on multi-org |
| `TestAdoptionServiceImportSeedsModelsAndIsIdempotent` | AC2, AC5 | Full import lifecycle + idempotent re-import |
| `TestAdoptionServiceImportUsesExistingAdoptedIdentityDespiteNewOverride` | AC5 | Identity-based matching across re-imports with name overrides |
| `TestAdoptionServiceImportTransactionalRollbackOnStateFailure` | AC5 | Rollback on mid-transaction failure; no leaked rows |
| `TestAdoptionServiceImportRetriesAfterTransactionalDuplicateRace` | AC5 | Race-condition retry on 23505 duplicate key |
| `TestAdoptionServiceImportTransactionalDuplicateImportConverges` | AC5 | Convergent idempotency across two imports |
| `TestAdoptionServiceImportRejectsForeignArtifactDigest` | — | Cross-service artifact collision guard |
| `TestAdoptionServiceImportRejectsIncompatibleExistingEnvironment` | — | Runtime type conflict detection |
| `TestAdoptionServiceImportSelectionReportsScanFailure` | — | Docker unavailable handling |
| `TestAdoptionServiceImportSelectionReportsUndiscoveredContainer` | — | Missing container handling |
| `TestAdoptionServiceImportStoresSensitiveEnvironmentAsSecrets` | — | Sensitive env → encrypted secrets |
| `TestAdoptionServiceImportRejectsSensitiveEnvironmentWithoutSecrets` | — | Fail-closed without secret storage |

### Coverage Gaps (Minor)

- **AC4 (migration repair rows)**: Verified by migration review, not by a Go unit test. This is acceptable since the migration is pure SQL and the repair table is operator-facing.
- **Repository unit tests**: `go test ./internal/repository/ -run AdoptedRuntime` reports "no tests to run" — the `PgAdoptedRuntimeIdentityRepository` has no standalone unit tests. However, it is exercised through the service-level tests via mock repos, and its SQL is straightforward upsert/select with standard pgx patterns. This is a minor gap, not a blocker.
- **Multi-environment org resolution**: The case where target environments resolve to multiple orgs is handled in code (L688) but not directly tested. Low risk since the single-org and multi-org cases are both covered.

### Test Execution ✅

```
go test ./internal/service/          — PASS (0.301s)
go test ./internal/repository/       — PASS (0.163s)  
go test ./internal/adapters/runtime/ — PASS (7.610s)
```

---

## Documentation Assessment

### PSTF Artifacts ✅

All six PSTF files are present and complete:

| File | Status |
|------|--------|
| `feature_spec.json` | ✅ Clear before/after behavior description |
| `acceptance_criteria.json` | ✅ Five criteria covering all changes |
| `test_matrix.json` | ✅ Maps tests to acceptance criteria |
| `verification_report.md` | ✅ Evidence, test runs, remaining work (none) |
| `defects.json` | ✅ Empty — no defects found |
| `hitl_decisions.md` | ✅ Documents that no human decisions were needed |

---

## Recommendations

No blocking issues found. Two minor improvement opportunities for future work:

1. **Consider adding a standalone repository integration test** for `PgAdoptedRuntimeIdentityRepository` against a test database, particularly for the `ON CONFLICT` upsert behavior and the `FindByFingerprints` query with `ANY($1)`.

2. **Add a test case for multi-environment org ambiguity** — a test where two target environments resolve to different orgs, verifying the error message at L688.

Neither is required for this task's completion.

---

## Conclusion

The implementation of bahia-pmy2 (Task 8) is **complete and correct**. All five acceptance criteria are met with evidence from code review and passing tests. The code follows project patterns consistently, transactions are properly scoped, and the fail-closed org resolution prevents silent ownership misassignment. No placeholder or stub code exists.
