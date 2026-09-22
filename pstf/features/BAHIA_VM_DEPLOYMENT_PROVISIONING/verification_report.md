# VM deployment and provisioning — verification report

The latest merged-tree evidence supersedes the historical gate snapshots below.
All verification here is code/fixture evidence, not live-host, relay, pilot or
soak acceptance.

## Merged-tree lint and adoption re-verification — 2026-09-22

Task: `bahia-yrt7g.4` (epic `bahia-yrt7g`). Main checkout, branch
`feat/vm-deployment-provisioning`, starting at `c70ec771`, including measured
adoption `76bddf3e` and Nostr upgrade `4cef973c`, plus this lint cleanup.
Environment: Go 1.26.3, darwin/arm64, golangci-lint 2.11.4, empty `GOFLAGS` and
`GOEXPERIMENT`. No race command used `-gcflags` or disabled checkptr.

### Changes and regression evidence

- The first rerun reported **47 capped diagnostics** (39 errcheck, 2 ineffassign,
  6 staticcheck), not the earlier 31. Removing both linter output caps exposed
  additional inherited findings; the final uncapped delta is zero.
- File, socket, watcher, cold-guard and staging cleanup errors are checked.
  `JoinCleanupError` preserves primary provider classification, retry and
  confirmation flags, retains both causes and keeps private diagnostic text
  behind the existing provider error boundary. Test teardown failures fail tests.
  HTTP response-body teardown is explicitly best-effort after the response/status
  has been read; it is not readiness or completion evidence.
- Metrics rendering returns write failures and the HTTP handler stops the failed
  scrape rather than attempting to repair a partially written response. The
  libvirt constructor uses the same already-connected, instrumented transport.
  Boolean rewrites preserve the same accepted values and identity guards. The
  unused checkpoint directory assignment and ineffective immutable-string reset
  are removed; mutable bootstrap payload clearing and audit handling remain.
- `TestJoinCleanupErrorPreservesClassificationAndPrivateCauses` and
  `TestCheckpointReportsColdGuardCleanupFailure` pass. A temporary Go overlay of
  the original `c70ec771` checkpoint implementation makes the latter fail for
  both committed and conflict paths with `cold guard cleanup failure was
  swallowed`; the normal tree passes. The overlay was not used for race gates.
- `TestVirtualizationRenderStopsOnWriteFailure` covers header, gauge and summary
  writes; `TestMetricsHandlerStopsAfterVirtualizationWriteFailure` proves the
  endpoint stops writing. Existing ownership, cold-state, registration/readiness,
  measured-adoption and secret-redaction regressions pass unchanged.

### Final gates

| Gate | Result |
|---|---|
| `golangci-lint run --new-from-rev=042e881b --max-issues-per-linter=0 --max-same-issues=0 ./...` | PASS: `0 issues.` |
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `go test ./...` | PASS: 80 test-bearing packages |
| `go test -race ./...` | PASS: 80 test-bearing packages, plain race |
| `make race` | PASS: `CGO_ENABLED=1 go test -race ./... -count=1`, 80 test-bearing packages |
| Adoption package-set race command below | PASS: all nine packages, uncached |
| PostgreSQL VM/adoption suites, serialized `-p 1`, uncached | PASS: app 10.454s; repository 42.129s; service 4.266s |
| Same PostgreSQL suites with plain `-race` | PASS: app 16.633s; repository 53.084s; service 4.513s |
| `git diff --check`, feature PSTF JSON parsing | PASS |

Exact adoption package-set race command:

```sh
go test -race ./internal/adapters/runtime/vm/... ./internal/domain \
  ./internal/repository ./internal/service ./internal/db \
  ./internal/controlplane ./internal/app/... -count=1
```

Exact PostgreSQL commands (the environment variable points only at the disposable
fixture database):

```sh
go test -p 1 -tags=integration \
  ./internal/app ./internal/repository ./internal/service \
  -run '^(TestVirtualizationPostgres|TestVMControlPlane|TestPersistentVMPostgres|TestVMAdoption)' \
  -count=1 -timeout=240s
go test -p 1 -race -tags=integration \
  ./internal/app ./internal/repository ./internal/service \
  -run '^(TestVirtualizationPostgres|TestVMControlPlane|TestPersistentVMPostgres|TestVMAdoption)' \
  -count=1 -timeout=240s
```

Both commands ran with `BAHIA_VM_TEST_DATABASE_URL` set to a dedicated
loopback-only `postgres:16-alpine` container. Each test created and removed a fresh
schema using the real migrations; the container was removed after the gates.
The `-p 1` serialization retains the fixture's `pgcrypto` collision protection.
No existing service database, live provider or live relay was used.

Local logs: `/tmp/bahia-yrt7g4-lint-final.log`,
`/tmp/bahia-yrt7g4-final-{build,vet,test,race,postgres,postgres-race}.log`,
`/tmp/bahia-yrt7g4-make-race.log`, `/tmp/bahia-yrt7g4-adoption-race.log` and
`/tmp/bahia-yrt7g4-counterfactual.log`.

`go.mod`, `go.sum`, `Makefile` and source plans are unchanged by this cleanup.
The Signet startup report now identifies its serializer crash as historical and
references the fix; no other feature reports were changed. Full-repository lint
debt remains separately tracked in `bahia-ipnlr`; only the whole-feature delta is
claimed clean. Live administrative compatibility (`bahia-yrt7g.2`), host/pilot/
soak acceptance and independent review of live evidence remain outside this task.
No fake production implementation or fallback was introduced. Oracle review was
skipped as requested; no push is authorized.

## Nostr dependency repair — 2026-09-22

Task: `bahia-4fz4z`, implemented on `fu/nostr-race-unblock` from `906e19d8`
in `4cef973c`, merged by `221ee716`.
Environment: Go 1.26.3, darwin/arm64, empty `GOFLAGS`.

Before the upgrade, `go test -race ./internal/soulfactory/` exited 1 with
`fatal error: checkptr: pointer arithmetic result points to invalid allocation`
in `fiatjaf.com/nostr` `writeJSONString`, `event.go:245`, reached by
`TestCanonicalProvisioningProjectionForContextVMRequest` while signing an event.

Upgrading `fiatjaf.com/nostr` from `v0.0.0-20260902034142-316ef6591fa2` to
`v0.0.0-20260916040958-27e395a0f6e7` fixes both `writeJSONString` and
`appendJSONString`: upstream preserves the string data as an `unsafe.Pointer`
and uses `unsafe.Add` instead of storing and incrementing a `uintptr`.
The dependency's module requirements are unchanged. No vendored patch,
application call-site change, test skip or checkptr exemption is needed.

That dependency-only task passed `go build ./...`, `go vet ./...`,
`go test ./...`, plain `go test -race ./...` (80 test-bearing packages),
`make race`, `go test -race ./internal/soulfactory/` and the uncached
`TestCanonicalProvisioningProjectionForContextVMRequest` race regression.
`make race` now runs `CGO_ENABLED=1 go test -race ./... -count=1` without
suppressing checkptr. It resolved the dependency limitation on closed Item C
(`bahia-kw93u`); it did not rerun tagged PostgreSQL tests or lint. Those are
covered by the later merged-tree verification in this report.

## Measured adoption — 2026-09-22

Task: `bahia-yrt7g.8`, implemented in the `bahia-adoption` worktree on
`fu/vm-measured-adoption` in `76bddf3e`, merged by `c70ec771`. The orchestrator-owned source
plans were unchanged. The implementation agent left issue lifecycle and merging
to the integrator; no push or Oracle review was performed in that task.

### Implemented boundary

- Concrete libvirt and Firecracker measurements bind the complete normalized
  provider configuration, exact modeled allocation/network/firmware/autostart,
  trusted image pin, writable component contents and opaque host file identity.
  Libvirt checks the qcow2 backing graph and compares full guest-visible contents
  against the trusted snapshot; a backing filename alone is never lineage proof.
  Firecracker requires matching kernel and private raw-rootfs snapshot bytes.
- UEFI code, NVRAM and coordinated TPM state are measured where supported.
  Foreign owners, unrecognized controller metadata/record versions, mismatched
  legacy identity, unsupported graph/device layouts and lost cold-state watches
  refuse enrollment. Direct driver calls cannot acquire unmarked ownership
  without private measurement proof and a cold-state barrier.
- Two-person, bounded, single-use approvals bind the full measurement separately
  from the request hash and provider fingerprint. Admission, execution and
  interrupted-operation verification remeasure; recovery never replays mutation.
- Approved matching enrollment writes the exact baseline plus local component
  inventory. Migration 000067 adds transactional storage reservations/registration
  and measurement-bound approvals. Writable file claims are exclusive per host;
  registration requires authoritative matching applied pins. The down path locks
  guarded tables before checking for evidence and refuses evidence loss.
- The real ContextVM `persistent-vm/register-adoption` path calls the service,
  deriving the config digest rather than trusting a caller pin. Its registration
  acknowledgment has a zero-UUID operation ID and no operation coordinate; it
  neither installs ownership nor fabricates provider completion. A separate
  approved `adopt` operation is required. Legacy v1/pre-inventory enrollment uses
  exactly this evidence-producing path, not names or directory authority.

### Regression mapping and test boundaries

`acceptance_criteria.json` AD-01 through AD-05 map to exact test names;
`test_matrix.json` lists source files and external boundaries. Provider
conformance reuses `testdata/conformance/owned-domain.xml` and
`firecracker-config.json`, exercising real files, hashing, release resolution,
concrete drivers and core records. Virsh/qemu-img/process-event boundaries are
test doubles, not live host evidence. App integration exercises the actual
ContextVM handler/admission/service/worker/PostgreSQL path with a fake VM provider.
Existing trust-policy tests separately exercise signed artifact verification.

Negative tests cover mismatched desired hardware, configuration/image/writable
bytes, same-path inode substitution, foreign ownership, unknown controller
metadata/records, unsafe lineage, guest-content differences, external image data,
missing proof, arbitrary caller pins and cold-watch invalidation before new
ownership/applied-record writes. Positive tests cover unmarked, v1, pre-inventory,
standalone snapshot and coordinated UEFI/TPM enrollment, arbitrary display names,
idempotent replay, explicit new-image revisions and recovery without mutation
replay. PostgreSQL tests prove approval rollback, exclusive inventory claims,
tenant isolation, registration only after applied evidence, and guarded migration
round trips.

### Verification history

The implementation agent passed vet, uncached tests and race for
`./internal/adapters/runtime/vm/... ./internal/domain ./internal/repository
./internal/service ./internal/db ./internal/controlplane ./internal/app/...`,
plus PostgreSQL app/repository/service VM suites, `git diff --check` and PSTF
JSON parsing. Those race runs predated `4cef973c` and used the former
Nostr-only checkptr workaround.

The merged-tree gates in this report supersede that race evidence: the full
package set and tagged PostgreSQL suites now run with plain `-race`, without
`-gcflags` or another checkptr exemption. The PostgreSQL selector includes
`TestVirtualizationPostgresMeasuredAdoptionIntent`, measured-inventory and
migration tests under `TestVMControlPlane`, and `TestVMAdoption` regressions.

Keep `-p 1` for the integration packages. An earlier package-parallel run failed
during migration 000001 because concurrent fixtures created `pgcrypto` in
disposable schemas (`pg_extension_name_index` duplicate key). Serialization
avoids that fixture bootstrap collision; it is not a production regression.

### Explicit limits

Live libvirt/Firecracker host, relay, pilot and soak evidence is not claimed.
Enrollment does not relocate legacy directories or silently flatten/rebase disks:
operators must explicitly prepare UUID-contained private storage and a trusted
current snapshot. Multi-layer/external-data qcow2 graphs, unsupported passthrough
and ambiguous ownership remain fail-closed. Failed/interrupted inventory claims
remain for explicit operator review, not automatic reassignment. These are
documented acceptance boundaries, not inferred ownership or placeholder paths.

---

## Fixer S review remediation — 2026-09-22

Task: `bahia-yrt7g.6`, branch `feat/vm-deployment-provisioning`. All nine Fixer S
items in the orchestrator-owned review list were verified against source and
addressed. No false positives or deferred Fixer S findings. Provider files were
owned by Fixer P and were not edited by Fixer S. No Oracle or push was performed.

### Finding-to-regression mapping

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

### Historical gates

- `go build ./...`: PASS.
- `go vet` and `go test -count=1` for `./internal/domain ./internal/service ./internal/controlplane ./internal/app/... ./internal/reconcile ./internal/adapters/loom ./internal/repository ./internal/db`: PASS.
- Same package set with plain `go test -race`: PASS in the subsequent full-project `bahia-4fz4z` re-verification.
- PostgreSQL integration tests for app/repository/service, selected by `^(TestVirtualizationPostgres|TestVMControlPlane|TestPersistentVMPostgres)`, pass; final integration race results: app 10.671s, repository 22.783s, service 10.085s.
- `git diff --check`: PASS.

PostgreSQL was a disposable loopback-only PostgreSQL 16 Alpine container with
fresh per-test schemas, not an existing service database. A concurrent in-flight
Fixer P edit temporarily caused `newColdFileWatch` to be undefined during one
integration race build; the unmodified provider files subsequently compiled and
the complete final gate passed. The former plain-race blocker `bahia-4fz4z`
is resolved by the dependency upgrade and full-project re-verification above.
These timing results predate that upgrade. Tagged integration tests were not
rerun for `bahia-4fz4z`; they were rerun with plain race for `bahia-yrt7g.4`
as recorded in the merged-tree gates.

Counterfactual Go overlays restored pre-fix production implementations without
changing the working tree. Regressions failed with clone tier `expected: 2,
actual: 1`, missing pre-AUTH retraction, recurring drift having one apply instead
of two, disabled drift having zero instead of one, repeated capacity release
returning `resource not found`, and a lock holding one query-pool connection.
All pass with the fixes. No live provider, deployed Loom endpoint, desktop pilot
or soak acceptance is claimed.

## Integration verification — 2026-09-22

Task: `bahia-yrt7g.3`, branch `feat/vm-deployment-provisioning`. This is **code-only** verification of Items A–E together. It does not establish live-host, desktop pilot, deployed administrative endpoint or soak acceptance.

### Integration changes and exercised behavior

- Opt-in installation configuration constructs the real persistent provider, bootstrap service, lifecycle service/worker/reconciler, Loom plane client/service/reconciler and PostgreSQL repository. Missing configuration/dependencies retain unavailable mutations; no host fallback is introduced.
- Provenance resolves an exact persisted artifact attestation and verifies event ID/signature, host-bound trusted signer and digest rather than trusting catalog flags. Operator allowlists and tenant RBAC are enforced; plane network/device/secret references are installation-bound. Plane bridging/passthrough remains denied rather than bypassing destructive approval.
- Post-commit callbacks wake journal projection and aggregate telemetry. Admission uses a nonblocking coalesced worker queue, startup calls `Recover`, provider events trigger exact-resource observation, and plane generations own cancellable subscriptions. The live reconciler supplies Loom's verified capability source. Shutdown cancels and joins workers/reconcilers.
- `TestVirtualizationPostgresIntentRecoveryAndPlane` uses a fresh schema, real migrations and `NewPgVirtualizationRepository`, with fake provider and administrative plane boundaries. It drives ContextVM intent through real services, reservations/operations, signed canonical projection and metrics; exercises provider-event guest-health changes and a package-drift apply/live-probe flow; interrupts a start after the external effect, restarts composition and proves recovery succeeds without another provider call or operation row.
- Config YAML loading and rejection tests, provenance tampering/wrong-digest/wrong-host/wrong-tenant tests, and nonblocking queue coalescing tests are ordinary package tests.

### Historical integration gate results

| Gate | Result |
|---|---|
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `go test ./...` | PASS |
| `make lint` | FAIL: 158 diagnostics in the capped full-repository output; inherited repository/Item B–E findings, tracked in `bahia-ipnlr` and `bahia-yrt7g.4` |
| `golangci-lint run --new-from-rev=89e3fc50 ./...` | PASS: zero integration-new findings |
| `golangci-lint run --new-from-rev=042e881b ./...` | Historical FAIL: 31 reported feature diagnostics; superseded by the zero-diagnostic merged-tree gate for `bahia-yrt7g.4` |
| Focused race: app, config, reconcile, readmodel, runtime/vm and both drivers, service, controlplane, telemetry | PASS with plain race in the full-project `bahia-4fz4z` re-verification |
| PostgreSQL integration race: app, repository, service, selected VM tests | PASS on final rerun: app 9.283s, repository 28.037s, service 4.028s |

The PostgreSQL timings above are historical, obtained before the Nostr upgrade
with the then-required dependency exemption. The merged-tree results supersede
them using plain `-race`, serialized packages and the expanded adoption selector.
The projection test signs and records events in the real in-memory Nostr outbox;
no live relay acceptance is claimed.

### Item defects and review

- Fixed Item D initial-session handling: a new PostgreSQL execution plane has a nil observation cursor. The reconciler previously rejected it before any apply. It now CAS-rotates from the nil session while retaining generation and restart fences. The first app test failed waiting for apply before this fix.
- Fixed Item E empty shutdown critical sections: the projector now records closed state while joining in-flight recovery; the test reads its publication count while holding that lock.
- Early test failures also exposed invalid test setup (an offline worker and reboot of a stopped VM) and an inappropriate concurrent manual `Recover` call. Fixtures now represent an eligible worker, interrupt a start, and repeat recovery only after joined shutdown. Production admission was not weakened.
- **Initial review attempt (historical):** whole-feature Oracle review of `042e881b..f302eff0` was rejected before analysis with `Input exceeds the maximum length of 1048576 characters`, including reduced-payload retries. No Oracle findings were generated. This initially blocked `bahia-yrt7g.3` under follow-up `bahia-yrt7g.5`; the sliced review below superseded that blocker. Diff snapshots: `_git_data/repos/bahia-13b4155d/2026-09-22/0051` (complete) and `.../0058` (production-only).

- **Subsequent review outcome (orchestrator, 2026-09-22):** the whole-feature Oracle review could not run because of the provider input limit, so the review was performed instead as three independent, read-only, sliced code reviews of `git diff 042e881b..d54f1bf6` — (1) secrets, destructive approvals and public surfaces; (2) provider ownership, host-fallback, event-confirmed transitions and checkpoint coordination; (3) lifecycle-class integrity, Loom job-ownership boundary, reconciliation and migration safety. They produced 20 confirmed findings (9 P1, 11 P2), all recorded in the orchestrator plan checklist and remediated with regression/conformance tests in `a7eb26b5`, `631cf271` (service/plane/repository/migration) and `14a19137` (provider). One item was initially fixed fail-closed with measured adoption deferred to `bahia-yrt7g.8`, subsequently implemented in `76bddf3e` as documented above. Final gates on `14a19137`: `go build ./...`, `go vet ./...`, `go test ./...` (80 packages, exit 0). `bahia-yrt7g.5` is superseded by this sliced review and `bahia-yrt7g.3` is no longer blocked. Independent infrastructure/security review of live evidence (canonical acceptance item 12) is still required before pilot autostart or wider access and is not claimed here.

Live administrative compatibility remains `bahia-yrt7g.2`. Installation capacity observations and signed image/package evidence are prerequisites, never synthesized from configuration. Host installation/image construction, pilot and soak remain outside this integration task. No fake production provider/plane implementation or implicit fallback was added. No push is authorized.

---

## Item A verification

Historical scope: `bahia-qw3qm`, branch `feat/vm-deployment-provisioning`. This was the Item A contract checkpoint only; it did not claim B–E or live acceptance. Source plans were read and left unchanged. Migration directory inspection established `000066` as the next unused number.

### Historical executed gates

| Gate | Result |
|---|---|
| `go build ./...` | PASS |
| `go vet ./internal/domain/... ./internal/repository/...` | PASS |
| `go test ./internal/domain ./internal/repository -count=1` | PASS |
| `BAHIA_VM_TEST_DATABASE_URL=... go test -race -tags=integration ./internal/domain ./internal/repository -run '^TestVMControlPlane' -count=1` | PASS: domain 1.231s; repository 11.228s |
| PSTF JSON parsing | PASS: feature specification, acceptance criteria, test matrix, defects |

PostgreSQL proof used a disposable local `postgres:16-alpine` container, bound only to loopback. Each integration test created a fresh schema, applied the repository's real migration sequence with `db.Migrate`, exercised the actual pgx repository and removed its schema. Database tests are selected by the `integration` build tag and fail if `BAHIA_VM_TEST_DATABASE_URL` is missing; no new database test silently skips. The dedicated container is removed after verification.

The final race gate includes seven domain tests and ten PostgreSQL tests. `test_matrix.json` maps every Item A criterion to executable tests. No provider or relay call-count substitutes were used for persistence proof.

### Verified behavior

- Explicit lifecycle classes, provider/OS/firmware compatibility, exact UUID ownership, coordinated checkpoint requirements, strict JSON/SecretRef boundaries, safe connections, measured-usage validation and serialization round trips.
- All five runtime states crossed with drift and guest-health axes. Provider unavailability preserves the last actual runtime state and timestamp; stale generation/session/sequence and invalid session replacement are rejected.
- Tenant-scoped CRUD/list, generation CAS, immutable images/creation identity, collection normalization, SQL envelope/enum constraints and transactional change history.
- Concurrent capacity reservations cannot exceed the governed ceiling. Replayed reservations do not double-count, stopped deployments retain reservations, and verified absence allows release.
- Idempotent concurrent operation admission creates exactly one operation. Changed content conflicts; overlapping and unconfirmed operations retain exclusivity. Provider execution locks exclude admission. Transport admission cannot jump directly to success.
- Two-person approval validation, atomic single-use consumption, request/fingerprint binding, phase/revision CAS and rollback of failed admission (desired records, operations, approval consumption and journal).
- Approved recreation can retain the stable deployment UUID while rebinding provider ownership only after verified old absence and reservation release. Plain updates and approval without absence fail. Old provider observations are cleared on rebind.
- Checkpoint/export persistence, immutable ready manifests, copied component integrity, plane persistence and probe-derived contribution. Failed probes remain failed across non-probe observations; delayed success and changed duplicate probe sequences cannot restore capability. Windows probe claims are rejected.
- Migration `000066` down then up both applied. A pre-existing deployment-unit row was compared byte-for-byte before/after and remained unchanged. Down migration with authoritative inventory/history correctly refused data loss.

### Test correction

The first migration fixture omitted legacy `deployment_units.reconcile_mode` and `ownership_mode`; PostgreSQL rejected that fixture before testing rollback. The fixture was corrected to use the existing required columns. Final migration and race gates pass; no production schema was relaxed to accommodate the test.

### Historical handoff boundaries

See `feature_spec.json` for exported contracts and `hitl_decisions.md` for exact repository/admission/session semantics and B–E hooks. B–E already have separate tracked work: `bahia-xv6vl`, `bahia-kw93u`, `bahia-1wh79`, `bahia-8f30n`.

No changes were made to provider drivers, the existing Hypervisor, service/reconcile, Loom transport, worker normalization, API/telemetry, app wiring, existing migrations or unrelated validators. No fake provider/plane implementation was introduced. Item A contains implemented persistence and real domain validation; provider and service behavior remains explicitly owned by the subsequent tracked items.

No full-project `go test ./...`, production database migration, live host operation, deployed Loom endpoint compatibility, image verification against a live signer, pilot or soak was claimed. Oracle review and push were omitted as explicitly requested. Commit is restricted to the Item A paths; pre-existing staged `.beads/issues.jsonl` changes are excluded.
