# Fixer S review remediation — 2026-09-22

Task: `bahia-yrt7g.6`, branch `feat/vm-deployment-provisioning`. All nine Fixer S
items in the orchestrator-owned review list were verified against source and
addressed. No false positives or deferred Fixer S findings. Provider files are
owned by Fixer P and were not edited here. No Oracle or push was performed.

## Finding-to-regression mapping

| Finding | Change and proof |
|---|---|
| Data disposition contract | First standalone commit `a7eb26b5`: additive `VMDataDisposition`, retain default, request/persistence/provider propagation and two-person approval. `TestVMDeleteDataDispositionApprovalAndProviderPropagation`, `TestVMDeleteDispositionBoundToApprovalAndIdempotency`, and invalid-target tests. Omitted/explicit retain preserve pre-disposition request hashes. |
| Clone networking | Server derives destructive tier from target bridge/passthrough settings, hashes the exact target generation and rechecks it under admission/execution fences. `TestVMCloneNetworkApprovalBindsExactTargetRevision` covers approval, self-denial, changed target before admission/execution. |
| Public desired-change approval | `vm-operation/approve-plan` returns an approval ID without inventing an operation or advancing desired state. Extended `TestVirtualizationPostgresIntentRecoveryAndPlane` drives a second operator through public handler, real service, PostgreSQL approval consumption and desired-update execution; self-approval is rejected. |
| CLOSED auth-required eligibility | Observation callback retracts synchronously before AUTH, including auth failure. `TestPlaneObserveRetractsBeforeAuthentication`. |
| Recurring/disabled drift | Matching current-session state clears the in-flight apply latch; each new drift episode has a distinct idempotency key; disabled desired state also converges. `TestPlaneRecurringDriftAndDisabledConvergence`. |
| Transient probe timeout | App supervises transient watcher failures with cancellable capped reconnect backoff; each failed run retracts and joins before restart. Supervision tests use virtual time, probe test verifies retraction, and PostgreSQL app test injects the first probe timeout and recovers without another desired write. |
| Lifetime pool exhaustion | Session advisory locks use hijacked dedicated connections and close on exit, leaving query pool slots available. `TestVMControlPlaneLocksDoNotStarveQueryPool` uses MaxConns=1, overlapping distinct lock sessions and a query, plus same-resource exclusion. Dedicated sessions still consume PostgreSQL server connections. |
| Release replay | Missing tenant-scoped reservation is already released; verified-release checks remain for existing rows. Extended PostgreSQL quota/retention test proves replay, one release journal entry and cross-tenant isolation. |
| Migration rollback race | Single DO statement locks all ten guarded tables before emptiness checks and drops them without releasing the locks. PostgreSQL down/up and `TestVMControlPlaneRollbackFencesAdmissionBeforeEmptinessCheck` prove rollback refusal, lock coverage and blocked concurrent journal admission. |

## Final gates

- `go build ./...`: PASS.
- `go vet` and `go test -count=1` for `./internal/domain ./internal/service ./internal/controlplane ./internal/app/... ./internal/reconcile ./internal/adapters/loom ./internal/repository ./internal/db`: PASS.
- Same package set with `go test -race -gcflags=fiatjaf.com/nostr=-d=checkptr=0`: PASS.
- PostgreSQL integration tests for app/repository/service, selected by `^(TestVirtualizationPostgres|TestVMControlPlane|TestPersistentVMPostgres)`, pass; final integration race results: app 10.671s, repository 22.783s, service 10.085s.
- `git diff --check`: PASS.

PostgreSQL was a disposable loopback-only PostgreSQL 16 Alpine container with
fresh per-test schemas, not an existing service database. A concurrent in-flight
Fixer P edit temporarily caused `newColdFileWatch` to be undefined during one
integration race build; the unmodified provider files subsequently compiled and
the complete final gate passed. Plain repo-wide race remains blocked by the
pre-existing `bahia-4fz4z`; only the Nostr dependency's checkptr is disabled.

Counterfactual Go overlays restored pre-fix production implementations without
changing the working tree. Regressions failed with clone tier `expected: 2,
actual: 1`, missing pre-AUTH retraction, recurring drift having one apply instead
of two, disabled drift having zero instead of one, repeated capacity release
returning `resource not found`, and a lock holding one query-pool connection.
All pass with the fixes. No live provider, deployed Loom endpoint, desktop pilot
or soak acceptance is claimed.

# Integration verification — 2026-09-22

Task: `bahia-yrt7g.3`, branch `feat/vm-deployment-provisioning`. This is **code-only** verification of Items A–E together. It does not establish live-host, desktop pilot, deployed administrative endpoint or soak acceptance.

## Integration changes and exercised behavior

- Opt-in installation configuration constructs the real persistent provider, bootstrap service, lifecycle service/worker/reconciler, Loom plane client/service/reconciler and PostgreSQL repository. Missing configuration/dependencies retain unavailable mutations; no host fallback is introduced.
- Provenance resolves an exact persisted artifact attestation and verifies event ID/signature, host-bound trusted signer and digest rather than trusting catalog flags. Operator allowlists and tenant RBAC are enforced; plane network/device/secret references are installation-bound. Plane bridging/passthrough remains denied rather than bypassing destructive approval.
- Post-commit callbacks wake journal projection and aggregate telemetry. Admission uses a nonblocking coalesced worker queue, startup calls `Recover`, provider events trigger exact-resource observation, and plane generations own cancellable subscriptions. The live reconciler supplies Loom's verified capability source. Shutdown cancels and joins workers/reconcilers.
- `TestVirtualizationPostgresIntentRecoveryAndPlane` uses a fresh schema, real migrations and `NewPgVirtualizationRepository`, with fake provider and administrative plane boundaries. It drives ContextVM intent through real services, reservations/operations, signed canonical projection and metrics; exercises provider-event guest-health changes and a package-drift apply/live-probe flow; interrupts a start after the external effect, restarts composition and proves recovery succeeds without another provider call or operation row.
- Config YAML loading and rejection tests, provenance tampering/wrong-digest/wrong-host/wrong-tenant tests, and nonblocking queue coalescing tests are ordinary package tests.

## Integration gate results

| Gate | Result |
|---|---|
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `go test ./...` | PASS |
| `make lint` | FAIL: 158 diagnostics in the capped full-repository output; inherited repository/Item B–E findings, tracked in `bahia-ipnlr` and `bahia-yrt7g.4` |
| `golangci-lint run --new-from-rev=89e3fc50 ./...` | PASS: zero integration-new findings |
| `golangci-lint run --new-from-rev=042e881b ./...` | FAIL: 31 inherited feature diagnostics (25 errcheck, 2 ineffassign, 4 staticcheck); `bahia-yrt7g.4` |
| Focused race: app, config, reconcile, readmodel, runtime/vm and both drivers, service, controlplane, telemetry | PASS with dependency-only checkptr workaround |
| PostgreSQL integration race: app, repository, service, selected VM tests | PASS on final rerun: app 9.283s, repository 28.037s, service 4.028s |

Race commands use `-race -gcflags=fiatjaf.com/nostr=-d=checkptr=0`; only the upstream Nostr package's checkptr instrumentation is disabled. Race instrumentation and checkptr for Bahia remain enabled. Plain repo-wide race is the known `bahia-4fz4z` upstream limitation, not fixed or claimed passing here. The same dependency-only workaround is already in `make race`.

PostgreSQL command: `BAHIA_VM_TEST_DATABASE_URL=... go test -race -gcflags=fiatjaf.com/nostr=-d=checkptr=0 -tags=integration ./internal/app ./internal/repository ./internal/service -run '^(TestVirtualizationPostgres|TestVMControlPlane|TestPersistentVMPostgres)' -count=1 -timeout=180s`. It uses a dedicated loopback-only disposable PostgreSQL 16 Alpine container; each test creates and removes its own schema. The projection test signs and records events in the real in-memory Nostr outbox implementation; no live relay acceptance is claimed.

## Item defects and review

- Fixed Item D initial-session handling: a new PostgreSQL execution plane has a nil observation cursor. The reconciler previously rejected it before any apply. It now CAS-rotates from the nil session while retaining generation and restart fences. The first app test failed waiting for apply before this fix.
- Fixed Item E empty shutdown critical sections: the projector now records closed state while joining in-flight recovery; the test reads its publication count while holding that lock.
- Early test failures also exposed invalid test setup (an offline worker and reboot of a stopped VM) and an inappropriate concurrent manual `Recover` call. Fixtures now represent an eligible worker, interrupt a start, and repeat recovery only after joined shutdown. Production admission was not weakened.
- **Oracle review BLOCKED, not passed:** `ask_oracle(mode:"review")` was attempted against the committed whole-feature `git diff 042e881b..f302eff0`. The provider rejected every request before producing analysis: `Input exceeds the maximum length of 1048576 characters`. Fresh-chat retries with only all production patches, a production-only snapshot and finally a 7,064-token MAP-only selection still failed identically. No Oracle findings were generated, fixed or deferred as findings. Completing the required review after repairing the RepoPrompt payload/routing failure is tracked in `bahia-yrt7g.5`; integration task `bahia-yrt7g.3` remains blocked rather than falsely closed. The complete diff snapshot is `_git_data/repos/bahia-13b4155d/2026-09-22/0051`; production-only snapshot is `.../0058`.

- **Review outcome (orchestrator, 2026-09-22):** the whole-feature Oracle review could not run because of the provider input limit, so the review was performed instead as three independent, read-only, sliced code reviews of `git diff 042e881b..d54f1bf6` — (1) secrets, destructive approvals and public surfaces; (2) provider ownership, host-fallback, event-confirmed transitions and checkpoint coordination; (3) lifecycle-class integrity, Loom job-ownership boundary, reconciliation and migration safety. They produced 20 confirmed findings (9 P1, 11 P2), all recorded in the orchestrator plan checklist and remediated with regression/conformance tests in `a7eb26b5`, `631cf271` (service/plane/repository/migration) and `14a19137` (provider). One item was fixed fail-closed with its measured-adoption follow-up deferred to `bahia-yrt7g.8`. Final gates on `14a19137`: `go build ./...`, `go vet ./...`, `go test ./...` (80 packages, exit 0). `bahia-yrt7g.5` is superseded by this sliced review and `bahia-yrt7g.3` is no longer blocked. Independent infrastructure/security review of live evidence (canonical acceptance item 12) is still required before pilot autostart or wider access and is not claimed here.

Live administrative compatibility remains `bahia-yrt7g.2`. Installation capacity observations and signed image/package evidence are prerequisites, never synthesized from configuration. Host installation/image construction, pilot and soak remain outside this integration task. No fake production provider/plane implementation or implicit fallback was added. No push is authorized.

---

# Item A verification

Scope: `bahia-qw3qm`, branch `feat/vm-deployment-provisioning`. Contract checkpoint only; B–E and live acceptance are not claimed complete. Source plans were read and left unchanged. Migration directory inspection established `000066` as the next unused number.

## Executed gates

| Gate | Result |
|---|---|
| `go build ./...` | PASS |
| `go vet ./internal/domain/... ./internal/repository/...` | PASS |
| `go test ./internal/domain ./internal/repository -count=1` | PASS |
| `BAHIA_VM_TEST_DATABASE_URL=... go test -race -tags=integration ./internal/domain ./internal/repository -run '^TestVMControlPlane' -count=1` | PASS: domain 1.231s; repository 11.228s |
| PSTF JSON parsing | PASS: feature specification, acceptance criteria, test matrix, defects |

PostgreSQL proof used a disposable local `postgres:16-alpine` container, bound only to loopback. Each integration test created a fresh schema, applied the repository's real migration sequence with `db.Migrate`, exercised the actual pgx repository and removed its schema. Database tests are selected by the `integration` build tag and fail if `BAHIA_VM_TEST_DATABASE_URL` is missing; no new database test silently skips. The dedicated container is removed after verification.

The final race gate includes seven domain tests and ten PostgreSQL tests. `test_matrix.json` maps every Item A criterion to executable tests. No provider or relay call-count substitutes were used for persistence proof.

## Verified behavior

- Explicit lifecycle classes, provider/OS/firmware compatibility, exact UUID ownership, coordinated checkpoint requirements, strict JSON/SecretRef boundaries, safe connections, measured-usage validation and serialization round trips.
- All five runtime states crossed with drift and guest-health axes. Provider unavailability preserves the last actual runtime state and timestamp; stale generation/session/sequence and invalid session replacement are rejected.
- Tenant-scoped CRUD/list, generation CAS, immutable images/creation identity, collection normalization, SQL envelope/enum constraints and transactional change history.
- Concurrent capacity reservations cannot exceed the governed ceiling. Replayed reservations do not double-count, stopped deployments retain reservations, and verified absence allows release.
- Idempotent concurrent operation admission creates exactly one operation. Changed content conflicts; overlapping and unconfirmed operations retain exclusivity. Provider execution locks exclude admission. Transport admission cannot jump directly to success.
- Two-person approval validation, atomic single-use consumption, request/fingerprint binding, phase/revision CAS and rollback of failed admission (desired records, operations, approval consumption and journal).
- Approved recreation can retain the stable deployment UUID while rebinding provider ownership only after verified old absence and reservation release. Plain updates and approval without absence fail. Old provider observations are cleared on rebind.
- Checkpoint/export persistence, immutable ready manifests, copied component integrity, plane persistence and probe-derived contribution. Failed probes remain failed across non-probe observations; delayed success and changed duplicate probe sequences cannot restore capability. Windows probe claims are rejected.
- Migration `000066` down then up both applied. A pre-existing deployment-unit row was compared byte-for-byte before/after and remained unchanged. Down migration with authoritative inventory/history correctly refused data loss.

## Test correction

The first migration fixture omitted legacy `deployment_units.reconcile_mode` and `ownership_mode`; PostgreSQL rejected that fixture before testing rollback. The fixture was corrected to use the existing required columns. Final migration and race gates pass; no production schema was relaxed to accommodate the test.

## Handoff boundaries

See `feature_spec.json` for exported contracts and `hitl_decisions.md` for exact repository/admission/session semantics and B–E hooks. B–E already have separate tracked work: `bahia-xv6vl`, `bahia-kw93u`, `bahia-1wh79`, `bahia-8f30n`.

No changes were made to provider drivers, the existing Hypervisor, service/reconcile, Loom transport, worker normalization, API/telemetry, app wiring, existing migrations or unrelated validators. No fake provider/plane implementation was introduced. Item A contains implemented persistence and real domain validation; provider and service behavior remains explicitly owned by the subsequent tracked items.

No full-project `go test ./...`, production database migration, live host operation, deployed Loom endpoint compatibility, image verification against a live signer, pilot or soak was claimed. Oracle review and push were omitted as explicitly requested. Commit is restricted to the Item A paths; pre-existing staged `.beads/issues.jsonl` changes are excluded.
