# Review: Secrets Hardening — Task 7 (bahia-j08v)

**Review ID**: bahia-ho9a
**Date**: 2026-05-30
**Reviewer**: Claude Agent
**Status**: ✅ APPROVED with minor findings

---

## Context / Scope

Task 7 (bahia-j08v: *Introduce Secret Versioning and Access Audit*) adds:

1. A `SecretVersion` domain type with immutable encrypted version rows
2. Database tables `secret_versions` and `secret_access_audit` (migration 000040)
3. Backfill of existing `service_secrets` rows to version 1
4. Resolver audit manifests (`SecretAccessManifest`) returned alongside resolved plaintext
5. Access audit rows written on resolve and runtime-apply attempts
6. No plaintext in persistence, logs, read models, or API payloads

### Files Reviewed

| File | Purpose |
|---|---|
| `internal/domain/secret_version.go` | Domain types: `SecretVersion`, `SecretAccessAudit`, `SecretAccessManifest`, `SecretResolveOptions` |
| `internal/domain/secret.go` | Existing `ServiceSecret` + `SecretRef` (context) |
| `internal/domain/secret_test.go` | `ToRef` and encryption method tests |
| `internal/repository/interfaces.go` | `SecretRepository` interface |
| `internal/repository/pg_secret.go` | PostgreSQL implementation |
| `internal/repository/pg_secret_test.go` | pgxmock unit tests for create, update, audit |
| `internal/adapters/secrets/resolver.go` | `ResolveSecretWithAudit` |
| `internal/adapters/secrets/resolver_test.go` | Resolver audit tests (success + failure) |
| `internal/adapters/secrets/nip44.go` | `Encryptor` (AES-256-GCM + NIP-44) |
| `internal/service/runtime_lifecycle.go` | `mergeEffectiveSecrets`, `recordRuntimeApplySecretAudit` |
| `internal/db/migrations/000040_secret_versions_audit.up.sql` | Schema + backfill |
| `internal/db/migrations/000040_secret_versions_audit.down.sql` | Rollback |
| `pstf/features/bahia-j08v/*` | Acceptance criteria, feature spec, test matrix, verification report |

---

## Acceptance Criteria Verdict

| ID | Criterion | Verdict | Evidence |
|---|---|---|---|
| AC1 | `SecretVersion` domain type exists and omits encrypted payload from JSON | ✅ Pass | `EncryptedValue []byte \`json:"-"\`` in `secret_version.go:15` |
| AC2 | `secret_versions` and `secret_access_audit` tables created; existing secrets backfilled to v1 | ✅ Pass | Migration `000040_secret_versions_audit.up.sql` creates both tables with `INSERT ... ON CONFLICT DO NOTHING` backfill |
| AC3 | Creating or updating a secret writes immutable version rows | ✅ Pass | `Create()` and `Update()` use CTE + `INSERT INTO secret_versions` in `pg_secret.go` |
| AC4 | Resolver returns safe audit manifest alongside resolved plaintext | ✅ Pass | `ResolveSecretWithAudit()` returns `(string, domain.SecretAccessManifest, error)` |
| AC5 | Resolve and runtime-apply attempts write audit rows without plaintext | ✅ Pass | Audit rows contain only metadata (IDs, version, operation, outcome, actor, reason, error text). No plaintext or ciphertext fields. Confirmed in both resolver and `runtime_lifecycle.go` |
| AC6 | No plaintext in database schema, logs, read models, or status/result payloads | ✅ Pass | `json:"-"` on `EncryptedValue` in both `ServiceSecret` and `SecretVersion`. Audit table stores no payload fields. `SecretAccessManifest` contains only metadata. |

**All 6 acceptance criteria are met.**

---

## Code Quality Findings

### ✅ Strengths

1. **Clean domain modeling**: `SecretVersion`, `SecretAccessAudit`, and `SecretAccessManifest` are well-separated with clear responsibilities. The `json:"-"` tag on encrypted fields is a simple, effective guard against accidental serialization.

2. **Atomic versioning via CTE**: Both `Create()` and `Update()` insert the version row atomically in the same SQL statement using a CTE. This prevents orphaned secrets without a version row.

3. **Audit-on-failure pattern**: The resolver records audit rows even on decryption failure (with `outcome: failure` and safe error text), providing a complete access trail. The manifest outcome is also updated, so callers get accurate metadata.

4. **Dual-layer audit in runtime lifecycle**: `mergeEffectiveSecrets` audits the resolve phase, and `recordRuntimeApplySecretAudit` audits the apply phase — giving two audit entries per secret per deployment (resolve + apply).

5. **Migration is additive and idempotent**: `CREATE TABLE IF NOT EXISTS`, `ON CONFLICT DO NOTHING` for backfill, `CREATE INDEX IF NOT EXISTS`. Safe for reruns.

6. **Interface consistency**: `SecretRepository` interface in `interfaces.go` includes both `GetCurrentVersion` and `RecordSecretAccessAudit`. All mock implementations across `api/handlers`, `controlplane`, `mcp`, and `service` packages implement the updated interface.

### ⚠️ Minor Findings

#### 1. Pre-existing bug in `DeleteByName` — wrong placeholder (`$3` should be `$2`)

**File**: `internal/repository/pg_secret.go:193`
**Severity**: Low (pre-existing, not introduced by this task)

```go
// When envID is nil, only 2 args are passed but query uses $3:
_, err = r.pool.Exec(ctx, `
    DELETE FROM service_secrets WHERE service_id = $1 AND environment_id IS NULL AND name = $3
`, serviceID, name)
```

`$3` should be `$2`. With only two positional arguments, `$3` will cause a runtime error from pgx when this code path is exercised. This predates Task 7 but is worth filing separately.

#### 2. Verification report references migration `000039` but actual file is `000040`

**File**: `pstf/features/bahia-j08v/verification_report.md:8`
**Severity**: Cosmetic

The report says "Added migration `000039_secret_versions_audit`" but the actual file is `000040_secret_versions_audit.up.sql`. Minor documentation drift.

#### 3. No test for `GetCurrentVersion` repository method

**Severity**: Low

The `GetCurrentVersion` method on `PgSecretRepository` is tested indirectly through the resolver tests (which use a mock repo), but there is no pgxmock unit test verifying the SQL itself (unlike `Create`, `Update`, and `RecordSecretAccessAudit` which all have dedicated pgxmock tests).

#### 4. `recordRuntimeApplySecretAudit` silently logs audit write failures

**File**: `internal/service/runtime_lifecycle.go:630`
**Severity**: Acceptable / by design

Apply-phase audit failures are logged at `Warn` level but don't fail the deployment. This is a reasonable tradeoff (auditing shouldn't block deployments), but worth noting for operators monitoring audit completeness.

---

## Test Coverage Assessment

| Test | Criteria Covered | Quality |
|---|---|---|
| `TestResolverResolveSecretWithAuditRecordsVersionedAccess` | AC4, AC5, AC6 | Thorough — validates manifest fields, audit row content, ensures no plaintext in audit |
| `TestResolverResolveSecretWithAuditAuditsDecryptFailureWithoutPlaintext` | AC4, AC5, AC6 | Good — confirms failure path still audits and returns failure outcome |
| `TestPgSecretRepositoryCreateWritesVersionRow` | AC2, AC3 | Good — verifies CTE SQL executes with expected args |
| `TestPgSecretRepositoryUpdateWritesNextVersionRow` | AC3 | Good — verifies update CTE |
| `TestPgSecretRepositoryRecordSecretAccessAudit` | AC5, AC6 | Good — verifies all 13 audit columns are passed correctly |
| `TestDesiredStateSecretRedaction` / `_DetectsPlaintext` | AC6 | Existing redaction tests still pass |

**Coverage is adequate.** The mock-based approach verifies SQL shape and argument binding. The resolver tests use a functional `Encryptor` with real encrypt/decrypt cycles, providing integration-level confidence.

**Gap**: No dedicated test for `GetCurrentVersion` SQL (finding #3 above).

---

## Documentation Assessment

| Artifact | Status |
|---|---|
| `pstf/features/bahia-j08v/feature_spec.json` | ✅ Complete — clear intent, observed vs intended behavior |
| `pstf/features/bahia-j08v/acceptance_criteria.json` | ✅ Complete — 6 criteria, all verifiable |
| `pstf/features/bahia-j08v/test_matrix.json` | ✅ Complete — maps tests to criteria |
| `pstf/features/bahia-j08v/verification_report.md` | ⚠️ Minor drift — migration number is `000040` not `000039` |
| `pstf/features/bahia-j08v/defects.json` | ✅ Empty (no defects found) |
| `pstf/features/bahia-j08v/hitl_decisions.md` | ✅ Documented — no human decisions required |

---

## Placeholder / Stub Check

No placeholder or stub code found. All domain types are fully defined. All repository methods contain real SQL. The resolver performs actual encryption/decryption. The runtime lifecycle integration is complete with both resolve-phase and apply-phase auditing.

---

## Recommendations

1. **File a bug** for the `DeleteByName` `$3` → `$2` placeholder issue (pre-existing, not a blocker for this review).
2. **Fix** the migration number in `verification_report.md` from `000039` to `000040` (cosmetic).
3. **Consider adding** a pgxmock test for `GetCurrentVersion` to match the coverage pattern of other repository methods (low priority).

---

## Conclusion

Task 7 is **well-implemented and complete**. The secret versioning model is clean, the audit trail is comprehensive (covering both success and failure paths at both resolve and apply phases), and plaintext is properly excluded from all persistence and serialization surfaces. The PSTF documentation bundle is thorough. All tests pass.

**Verdict: APPROVED** — ready to close.
