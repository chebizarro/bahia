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
