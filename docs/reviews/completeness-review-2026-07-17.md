# Completeness Review — Production-Readiness Closures (2026-07-17)

**Reviewer:** automated read-only audit (Claude + verification sub-agents)
**Scope:** the 72 Beads issues referenced by commits in `3a148200..HEAD`, all reported as closed during the production-readiness push.
**Method:** for each issue, read `bd show` (problem + acceptance/recommended fix + close reason + NOTES), located the referencing commit(s), read the actual diff, opened the current source and tests, and checked four things: (1) root cause fixed in behavior, not cosmetics; (2) a real behavior-exercising test that would fail without the fix; (3) fail-closed/security fixes genuinely fail closed; (4) no test weakened or assertion deleted to pass.
**Nature:** read-only. No source files were modified. Only this report was written.

---

## Bottom line

- **57 VERIFIED** — real root-cause fix backed by a real, behavior-exercising test.
- **14 WEAK** — the code fix is real, but the test is missing, shallow, or gated so it does not actually run (details below). None are fake closes; each needs a follow-up test, not a reopen for lack of a fix.
- **1 not actually closed** — `bahia-w9s9x` is `IN_PROGRESS`, honestly left open by the author (see below). It was included in the audit list but is not a closed issue.
- **0 SUSPECT / fake closes.** Nothing in scope satisfies the wording while faking the behavior. The three items an automated pass initially flagged as suspect (`dyn1k`, `qio6f`, `kc4p8`) were each confirmed on manual read to be genuine fixes.

**Reopen these: (none.)** No issue is a superficial/fake close. Two items deserve a follow-up issue rather than a reopen (`bahia-1u8du` live-migration test; `bahia-w9s9x` is already open). See "Follow-ups" at the end.

The overall quality of this closure batch is high and unusually honest: fail-closed patterns are real (cosign really verifies, MCP really authorizes, the Cashu wallet really refuses instead of faking balances, the rollout really verifies the runtime before claiming rollback), deferred/partial work is documented in issue NOTES rather than hidden behind a success claim, and the trailing "fixture" commits *strengthen* tests to match the hardened code rather than weaken them.

---

## P0 / P1 security & fake-completion closes (deep review)

| Issue | Verdict | Commit | Evidence / what was verified |
|---|---|---|---|
| bahia-2r1z5 | **VERIFIED** | b8e84bb2, b9505d1a | MCP `CallTool` now calls `authorizeToolCall` (deny-by-default: unauthenticated → "authentication required"; authenticated-but-not-on-operator-allowlist → "access denied"; empty allowlist denies all). Secret mutate/delete gained tenant-ownership RBAC. New `server_authorization_test.go` proves cross-tenant secret update/delete are rejected **and** the stored secret is left unmutated. Existing tests adapted to a legit system principal — not weakened. |
| bahia-m6yiv | **VERIFIED** | cd630f5d | Cosign verifier now shells `cosign verify` with `--key`/keyless identity + `--insecure-ignore-tlog=false --insecure-ignore-sct=false`, and validates digest==artifact, repo match, identity/issuer match before ever setting `Verified:true`. Registry error, missing policy, verify error, wrong digest, wrong repo, identity mismatch all fail closed. The old fail-open `ReferrersError_ReturnsNil` test was **replaced** with `ReferrersError_FailsClosed` (strengthening). |
| bahia-soktt | **VERIFIED** | cd630f5d | Loom `SubmitJob` errors before build/publish when `len(Secrets)>0 && workerPubkey==""`, and validates the pubkey. Publish-spy tests prove missing/invalid worker → 0 publishes; valid worker → secret tag is encrypted (≠ plaintext). |
| bahia-1u8du | **WEAK** | d54cce0e | Corrective migration `000045` backfills `org_id` after `organizations` exists (fixes the 21/22 ordering). Root cause addressed in SQL, but the only test (`TestCorrectiveOrgBackfillPopulatesOrgID`) is `DATABASE_URL`-gated and, per the issue's own NOTES, was never executed against a live DB. Real fix, unproven by an executable test. |
| bahia-eclow | **VERIFIED** | d54cce0e | State/deployment-log/SBOM routes gained `coreRBAC` resolvers + `requireMember`; global state list filtered to caller org. `tenant_isolation_routes_test.go` (httptest + real NIP-98) asserts cross-tenant → 403 and list filtering. |
| bahia-xldxv | **VERIFIED** | d54cce0e | Notification channels gained `OrgID` + org-qualified CRUD/log methods + channel resolver RBAC; migration `000046`. Same httptest suite asserts foreign-org create/get/update/delete/test/list all return 403 and lists exclude foreign channels/logs. |
| bahia-8hpg0 | **VERIFIED** | 0e673bd1 | Soul Factory provisioning returns error + records Failed on sub-action failure; activation only after configured actions succeed. Regressions assert error, nil soul, failed step, stopped workflow. Prior permissive workspace test was strengthened, not deleted. |
| bahia-1ghi1 | **VERIFIED** | 46b67d01 | Secure defaults (loopback bind, empty DB password, TLS-required); startup `Load` fail-closed validation rejects wildcard/unauthenticated external binds, bundled password, non-TLS DB. Tests assert each rejection. |
| bahia-ly4dv | **VERIFIED** | 1528fecd | Fabricated CI enrichment/state/timestamps removed from the repository load path; UI reports "unavailable" explicitly. Test fails under the old fabrication by asserting loaded repos carry no `ci` field. |
| bahia-nh89d | **VERIFIED** | 16d11d4c | Web auth bootstrap/login fail closed and clear stale identities; mismatched NIP-07 and failed NIP-07/NIP-46 login rejected; backend rejection keeps NIP-98 unready. Behavior tests cover each. |
| bahia-ylg8g | **VERIFIED** | 57c2c372 | `NewEncryptor` now requires a valid 32-byte hex secp256k1 key (rejects non-hex/short/all-zeros) and derives the AES key via **HKDF-SHA256 with domain separation** instead of raw `SHA256(privkey)`. Test asserts `aesKey != SHA256(identity)` and rejects invalid keys — i.e. really validates + salts. |
| bahia-nzsze | **VERIFIED** | 6d06c25e, 9ed41545 | Avatar/Blossom fetches enforce same-origin/redirect limits, safe destinations, body-size caps, and validated decoded images (SSRF hardening). Cross-origin and private-target/size-limit tests present. |

### Fail-closed spot-checks explicitly requested

- **cosign really verifies** — yes (m6yiv): cryptographic `cosign verify` + digest/repo/identity binding; no code path marks `Verified:true` without it.
- **mv3dh really errors instead of silent-passing** — yes: `NostrVerifier.VerifySignatures` returns `ErrNostrPullVerificationUnavailable` instead of `nil,nil`; test asserts `errors.Is`.
- **filesystem_mock unreachable in prod** — yes (is2kz): production factory returns `filesystem_mock.ErrProductionSelection`; direct `New` still allowed for tests, and the test proves both.
- **secrets Encryptor really validates + salts** — yes (ylg8g): key validation + HKDF domain separation, asserted against the legacy SHA-256 derivation.
- **NIP-98 replay durable** — yes (phnr4): Postgres `nip98_replay_claims` with atomic `INSERT ... ON CONFLICT DO NOTHING` (RowsAffected==1), payload SHA-256 body binding, app fails closed without durable storage. Shared-replay, body-bind, oversize, and pgxmock atomic-insert tests present.
- **tenant routes really reject cross-tenant** — yes (eclow/xldxv/1u8du route layer): 13-route httptest suite asserts 403 with real NIP-98 auth.

---

## Partial / handoff closes (integrity check)

| Issue | Verdict | Commit(s) | Closed portion real? Deferred portion honest? |
|---|---|---|---|
| bahia-fzvm3 | **WEAK** | 46b67d01, 8375cc01, e8db0afe | Provisioner fail-closed + explicit NIP-05 relay wiring is real and unit-tested; **no app-composition test** proves `cfg.SoulFactory.NIP05Relays` actually reaches the provisioner. Nothing hidden — this is a shallow-test gap, not a fake close. |
| bahia-cgnkb | **VERIFIED** | 93adae16, 2a6e3dac | Nexus independently reads backend SHA-256 and fails on ambiguous/paginated/invalid observations (tested with a deliberately different expected hash); shared TLS/auth validation real + tested; Pulp honestly fails closed and never echoes expected checksums. Deferred Nexus factory fields / standard Pulp mutations / live compat tests are explicitly documented in the issue NOTES. |
| bahia-940z7 | **VERIFIED** | 9625e9aa | Fake in-memory balances/proofs removed entirely; `GetBalance`/`GetAllBalances`/`CreatePaymentToken`/`ReceiveToken`/`Initialize` return `ErrMintBackedFlowUnsupported`; `Capabilities()` reports honest availability. Tests assert fail-closed. The "partial" (no real mint-backed flow) is surfaced, not hidden. |
| bahia-mv3dh | **VERIFIED** | 5f568cf3 | Silent no-op replaced with `ErrNostrPullVerificationUnavailable`; `errors.Is` test. |
| bahia-phnr4 | **VERIFIED** | 5806234e | Durable PG replay store + payload binding, tests as above; app fails closed without DB (documented in NOTES). |
| bahia-qio6f | **VERIFIED (caveat)** | d3611bd3 | Adapter fix real + well-tested: durable 0600 JSON task-id store (atomic temp+rename), restart reuse (no duplicate `memory_task_start`), npub/metadata preserved as an identity event, **fails closed** (`ErrTaskIDStoreNotConfigured`) when unwired. Caveat: production wiring at `soulfactory.go:131` passes an empty `agentmemory.Config{}` (no `TaskIDFile`), so in production registration will fail closed until a path is wired — this deferred wiring is explicitly documented in the issue NOTES. Not a fake close; flagged as a production follow-up. |
| bahia-dyn1k | **VERIFIED (caveat)** | f8430e9e | All six responder families' terminal publish paths now return typed errors (`ErrResponderNotConfigured` / `…CorrelationMissing` / `…NoRelayAccepted`) instead of silent `nil`; `published==0` rejected. Table-driven `responder_contract_test.go` asserts them. Caveat: `MLResponder.PublishStatus` / `PublishRecipeRunStatus` remain bare `return nil` progress no-ops and the issue's recommendation to *assert* the read-model coupling was not implemented — a residual gap, but the fake-completion core is genuinely fixed and tested. |

---

## Rollout / rollback (deepest scrutiny) — bahia-cg3vb, bahia-s4y11, bahia-3elai

**VERIFIED** (commit 6466c56e). This was a real no-op-to-real conversion, not cosmetics:

- **Rollback (remediation):** previously just `Undeploy(canary/green)` + `nil`. Now resolves the previous successfully-deployed artifact from intent history, redeploys it, **observes the runtime and verifies the observed artifact matches + health is Healthy**, updates state, marks the replaced intent `RolledBack`, and fails closed if deps/history/artifact/verification are missing. Tests: `RollbackFailsClosedWithoutPriorArtifactHistory` (error, no `remediation.completed`) and `RollbackRestoresPriorDeployedArtifactAndState`.
- **Traffic (rollout executor):** `ShiftTraffic`/`Switch`/`Promote` were log-only no-ops returning nil. Now they call a `TrafficController` and **verify the runtime reports the target slot at the expected weight**, erroring if the runtime doesn't implement the interface. `autoRollback` restores traffic + primary and publishes `rollout.rollback_failed` (returning an error) instead of faking success. Tests prove traffic actions fail when the runtime can't apply traffic, propagate controller errors, restore+verify the prior artifact, and **do not** mark `RolledBack` / publish `rolled_back` when cleanup fails.
- **3elai (health gate):** observer errors now count toward the failure threshold and return a fast failure result; test asserts.

---

## Remaining issues (batch verdicts)

All **VERIFIED** unless marked. Evidence file:line captured during review; representative note given.

| Issue | Verdict | Commit | Note |
|---|---|---|---|
| bahia-f8e27 | VERIFIED | 99809937 | Unsupported DNS records error before write; test asserts original file unchanged. |
| bahia-5gjs1 | VERIFIED | 99809937 | Snapshot-only SyncZone fails closed; activation errors propagate. |
| bahia-8r9hf | VERIFIED | 99809937 | Reload failure restores + reloads prior config; test asserts prior bytes + 2 reloads. |
| bahia-h3x92 | VERIFIED | 5fd48fdd | Signet Connect logs only redacted pubkey/relay hosts; leakage regression. |
| bahia-673s5 | VERIFIED | 5fd48fdd | Authorization encoding returns JSON marshal errors; no header on error. |
| bahia-987az | **WEAK** | 5fd48fdd | Bounded contexts wired, but no behavior test proving stalled Connect/Sign actually time out — only stored-duration fields inspected. |
| bahia-ubspz | VERIFIED | 5fd48fdd | Authoritative `ListAgents` returns unsupported sentinel; cache path separated + tested. |
| bahia-ue1j8 | VERIFIED | 1528fecd | Web repo completion semantics corrected + unit test. |
| bahia-6wegq | **WEAK** | 1528fecd | Fix real, but test only asserts export absence — no behavior exercise. |
| bahia-jcllm | **WEAK** | 1528fecd | Duplicate helpers removed, but no test would fail if they returned. |
| bahia-pnme5 | VERIFIED | 16d11d4c | Auth store fail-closed + unit test. |
| bahia-kfqp1 | VERIFIED | 16d11d4c | NIP-46 session hardening + test. |
| bahia-6o1tj | VERIFIED | 16d11d4c | Route-access guard + test. |
| bahia-hznpt | VERIFIED | eca2a586 | Security-rescan surfaces failures + test. |
| bahia-f3mlo | **WEAK** | eca2a586 | Cache regression only exercises open failure, not read/write/delete/close. |
| bahia-a5stv | VERIFIED | 9ed41545 | LLM gateway HTTP fail-closed + test. |
| bahia-v8hrg | VERIFIED | 9ed41545 | Avatar path hardening + test. |
| bahia-bypyx | VERIFIED | 9ed41545 | Generator fail-closed + test. |
| bahia-iu3mr | VERIFIED | 9ed41545 | External provisioner fail-closed + test. |
| bahia-cia8i | VERIFIED | a3dff6b0 | SBOM structural validation rejects malformed SPDX/CycloneDX; regression. |
| bahia-yol96 | VERIFIED | a3dff6b0 | SBOM upload rejects nil/malformed/mismatched descriptors; per-failure tests. |
| bahia-nh63w | VERIFIED | d9e8240e | Registry 401/403 → typed `RegistryAuthError` (only 404 → Exists:false); errors.As test. |
| bahia-qzwfj | VERIFIED | d9e8240e | Factory tests assert concrete wiring, not just non-nil. |
| bahia-3ka2n | VERIFIED | 006dd566 | PullImage decodes progress stream, surfaces embedded errors; httptest. |
| bahia-4a4jt | VERIFIED | 006dd566 | Deploy returns contextual errors for label/env/rollout failures; injected-command tests. |
| bahia-xuav6 | VERIFIED | fbb84137 | Raw Nostr key CLI flags removed; main_test asserts. |
| bahia-qtceo | **WEAK** | 3f7889a8 | Go CI workflow added, but no validation test; race/linter omitted. |
| bahia-l3s1p | **WEAK** | ed1bde38 | Retryability optional / InProcessPublisher-only; base contract still can't surface errors. |
| bahia-7lzih | VERIFIED | ed1bde38 | Dispatcher surfaces failures + test (no direct HTTP TestChannel failure regression). |
| bahia-y5tng | VERIFIED | 81059b19 | Coordinators fail closed on missing deps; test covers registry absence. |
| bahia-j651y | **WEAK** | fa058a58 | Composite tenant key + scoped upsert + sqlmock tests, but no real-Postgres cross-tenant/rollback/migration proof. |
| bahia-udtj1 | VERIFIED | fa058a58 | Repo predicates `org_id+id`; both callers supply org; regressions fail on old query. |
| bahia-y157d | **WEAK** | 6480376b | All six continuity handlers fail closed via `isAuthorized`, but only the profile handler is behavior-tested. |
| bahia-yg971 | VERIFIED | cfe80031 | Payment metadata marshal failure returns before mutation/SQL; unchanged-record regression. |
| bahia-75oly | **WEAK** | 2c6e91ac | Projection writes wrapped in a tx w/ rollback; result-path rollback tested, run-path not; no real-DB assertion first write is absent. |
| bahia-fjsgr | **WEAK** | 2c6e91ac | SBOM compat+canonical share one tx w/ zero-row checks; canonical-failure rollback tested; no unchanged-count / run-path assertion. |
| bahia-0otyn | VERIFIED | aed199cc | Zero-row upsert → `ErrStaleWrite`; pgxmock exercises rejection. |
| bahia-f0or0 | VERIFIED | aed199cc | Version CAS + typed conflict; stale-version test asserts `ErrConflict`. |
| bahia-0mx9w | VERIFIED | 6d06c25e | Blossom upload requires 2xx + validated descriptor; only 404 = absence; malformed/redirect/503 tests. |
| bahia-s3qdz | VERIFIED | 8d2e4fec | OTLP providers created when configured; Shutdown closes both; real HTTP receiver asserts exports. |
| bahia-sq20c | **WEAK** | cf05c5ec | Restore coordinator fences + reuses executed result; reconciliation test asserts backend not called, but no test reproduces the completion-write-failure-then-retry crash window. |
| bahia-amfso | VERIFIED | 623d65c4 | LLM route promotion serialized via advisory lock; test (no real cross-process lock integration test). |
| bahia-ln07n | **WEAK** | 93adae16 | Nexus policy/backend drift verification real + tested, but production config/factory wiring for blob store + policy not present. |
| bahia-kok8m | VERIFIED | 5f568cf3 | Catalog test asserts every kind's decoder returns a populated projection (kind/family/dtag/timestamp), replacing the "not yet implemented" check. |
| bahia-kc4p8 | VERIFIED | a64343d2, fac65681 | Durable keyset-cursor pagination (migration 000049, `(created_at,id) > cursor`), real content translation into canonical params (test asserts raw `content` is gone), and v1→v2 schema-version idempotency with a regeneration test. Minor residual: relay backfill `oldest.Add(-1s)` can skip events sharing a second beyond `BackfillLimit` (inherent Nostr relay time-pagination limit) — noted, not a fake close. |
| bahia-hlhv5 | VERIFIED | 9ac18177 | Loom rejects unaccepted publishes; publish-spy test. |
| bahia-3elai | VERIFIED | 6466c56e | Health gate observer-error threshold (see Rollout section). |
| bahia-3w3mb | VERIFIED | 57c2c372 | Empty NIP-44 plaintext rejected (`ErrEmptyNIP44Plaintext`) + shipped with the ylg8g key-hardening tests. |

---

## Not actually closed

- **bahia-w9s9x — `IN_PROGRESS`, not closed.** The web-side page objects were genuinely strengthened (commit 5ec1db27: `data-testid="dashboard-root"` + nav/main/heading/`Create Service` assertions), but the actual bead target `test/e2e-agent/drivers/playwright.ts` is outside the web subtree and was left unchanged. The author honestly left the bead open and documented why in the NOTES. This is an accurate status, not a fake close; it was simply included in the audit list. No action required beyond noting that it is not a completed item.

---

## Test-weakening check (batch)

The three trailing "fixture" commits (`c2f5fc3d`, `156b8cc4`, `ba89f65a`) change **zero** assertions or `t.Skip`s. They only adapt fixture *data* to the newly-hardened code — swapping invalid encryptor keys (e.g. `"test-secret-key"`) for valid 32-byte hex keys now required by the ylg8g validation, and adding NIP-98 `payload` tags to test helpers now required by the phnr4 body binding (which *strengthens* those tests). No assertion was deleted to make anything pass, in these or in the reviewed feature commits (several old fail-open tests were explicitly rewritten to fail-closed).

---

## Reopen these

**(none)** — no in-scope issue is a superficial or fake close.

## Follow-ups (new work, not reopens)

1. **bahia-1u8du** — add an executable (containerized) Postgres test that actually runs migration `000045` and asserts `org_id` is populated; the current test is `DATABASE_URL`-gated and, per its own NOTES, was never run live.
2. **bahia-qio6f** — wire a real `Config.TaskIDFile` (durable path) into `internal/app/soulfactory.go:131`; today the empty config makes agent-memory registration fail closed in production. (Documented deferred work; track it explicitly.)
3. **Shallow-test hardening (WEAK items)** — add the missing behavior tests noted above for `987az` (timeout), `6wegq`/`jcllm`/`f3mlo` (behavior vs. presence), `qtceo` (CI validation + race/lint), `l3s1p` (publisher error contract), `j651y`/`75oly`/`fjsgr` (real-DB rollback/coexistence), `y157d` (remaining continuity handlers), `sq20c` (completion-write crash window), `ln07n` (prod wiring), `fzvm3` (app-composition test).
4. **bahia-dyn1k** — either implement or explicitly test the ML `PublishStatus`/`PublishRecipeRunStatus` read-model coupling the no-op relies on.
