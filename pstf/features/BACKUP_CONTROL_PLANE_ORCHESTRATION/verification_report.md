# Verification report — BACKUP_CONTROL_PLANE_ORCHESTRATION

Date: 2026-05-21
Status: Restore/retention Nostr integration and evidence verified for `bahia-s0ef`.

## Evidence gathered

- Backup, restore, approval, and retention command intake now use the existing Nostr reactor path for inbound event validation, event ID dedupe, scoped authorized-author filters, EOSE catch-up, CLOSED handling, and NIP-42 AUTH retry.
- Restore integration handles `BackupRestoreRequest` `38402` and `BackupRestoreApproval` `38403`; required approval leaves the restore queued/pending and prevents coordinator execution until approval is recorded.
- Restore responder publication covers `BackupRestoreStatus` `6982`, `BackupRestoreApprovalResult` `38413`, and `BackupRestoreResult` `38412`, with per-relay `PublishWithResults` summaries persisted into restore records.
- Retention integration handles `BackupRetentionEnforceRequest` `38404`; progress uses `BackupObservation` `6984` and terminal outcome uses `BackupRetentionResult` `38414`, with per-relay publish summaries persisted into retention records.
- Backup read-model projection now includes `BackupRestoreState` `31998`. No retention execution read-model kind was added; retention execution state is surfaced only by `6984` observations and `38414` results.
- App composition constructs the backend resolver, backup/restore/retention coordinators, responders, projector source, reactor dependencies, and background recovery runners.
- `docs/control-planes.md` records the first-slice restore source rule (`backup_run_id`), approval gating, retention backend-native boundary, per-relay publish summaries, and absence of restore/retention attestation kinds.

## Verification performed

- Focused tests:
  - `go test ./internal/controlplane ./internal/adapters/nostr ./internal/app`
- PSTF JSON validation after evidence update:
  - `feature_spec.json`
  - `acceptance_criteria.json`
  - `test_matrix.json`

## Test evidence

- `internal/controlplane/reactor_backup_requests_test.go`
  - backup run, restore request, restore approval, and retention request kinds are included in the default scoped control-plane subscription filter
  - authorized restore request creates durable restore state and does not invoke the restore executor before required approval
  - signed restore approval records approval and invokes the restore executor for the approved restore id
  - authorized retention request creates durable retention state, preserves `dry_run`, and invokes the retention executor
  - backup run idempotency and responder publish-summary assertions remain covered
- `internal/adapters/nostr/projector_backup_test.go`
  - snapshot projection emits backup recipe/policy/repository/run/restore/verification read models
  - mutation projection republishes changed restore state as `31998 BackupRestoreState`
  - `restore_eligible=false` for skipped verification and true only after successful verification
- Existing prerequisite tests retained coverage for:
  - backup/restore/retention domain validation and restore eligibility (`internal/domain/backup_test.go`)
  - restore registry approval/rejection and fail-closed completion (`internal/service/backup_registry_restore_test.go`)
  - retention registry backend-native metadata validation and terminal evidence (`internal/service/backup_registry_retention_test.go`)
  - restore and retention coordinator execution/recovery/capability failures (`internal/service/backup_restore_coordinator_test.go`, `internal/service/backup_retention_coordinator_test.go`)
  - real Kopia and Velero adapter command/API boundaries or explicit unsupported/configuration errors (`internal/adapters/backup/*_test.go`)
  - relay publish outcomes, AUTH/CLOSED reason helpers, duplicate-as-success, and zero-accepted failure (`internal/adapters/nostr/protocol_frames_test.go`)

## Current result

The restore/retention integration slice is verified: Bahia accepts authorized Nostr restore and retention commands through the existing reactor semantics, gates restore execution on approval, publishes signed restore/retention status/result events with relay OK outcome summaries, projects restore state, wires runtime background runners, and documents the retention no-read-model decision.

## Remaining work tracked outside this slice

No remaining restore/retention integration gaps are tracked for `bahia-s0ef`. Parent issue closure depends on the full suite and Beads parent verification.

---

## 2026-05-23 — BackupDefinition domain/repository slice (`bahia-gbo2`)

### Evidence gathered

- `internal/domain/backup.go` defines `BackupDefinition` as the operator-facing registry object composing repository, policy, recipe, schedule metadata, tenant/environment/owner scope, approval policy, restore target rules, executor targeting fields, labels/grouping, metadata, and audit fields.
- `internal/db/migrations/000030_backup_definitions.up.sql` creates the `backup_definitions` table with foreign keys to backup repositories, policies, recipes, organizations, and environments plus lookup indexes for registry, scope, scheduling, labels, and executor targeting queries.
- `internal/repository/pg_backup_controlplane.go` adds BackupDefinition CRUD methods and JSON/nullable UUID scanning without introducing handlers, MCP tools, UI, scheduler dispatch, or executor placement logic.

### Verification performed

- `go test ./internal/domain ./internal/repository ./internal/service`

### Test evidence

- `internal/domain/backup_test.go`
  - validates required composed references and `created_by` audit provenance
  - validates string trimming for operator-facing fields
- `internal/repository/pg_backup_controlplane_test.go`
  - validates BackupDefinition upsert arguments
  - validates scan of tenant/environment UUIDs and JSON fields for restore rules, executor labels, capability requirements, labels, and metadata
  - validates missing delete returns `repository.ErrNotFound`

### Current result

`bahia-gbo2` is verified for the requested domain model, migration, repository interface, and PostgreSQL repository CRUD scope. Command handlers, MCP tools, UI, scheduler execution, and executor placement behavior remain outside this bead and are tracked by dependent Beads.

### Review follow-up

Oracle review findings were addressed before closeout:

- nil executor label/capability slices now marshal as JSON arrays (`[]`) instead of JSON `null`
- `created_by` is immutable across `ON CONFLICT` updates
- validation rejects enabled schedules without a schedule expression and approval-required definitions without an approval policy

### Full-suite note

- `go test ./...` was run on 2026-05-23 after the targeted verification. The BackupDefinition-touched packages passed, but the full suite failed in unrelated packages:
  - `internal/api/handlers`: `continuity_test.go` still calls `NewContinuityHandler` with the old one-argument constructor; tracked as `bahia-b6h3`.
  - `internal/config`: `TestDNSValidationEnabled/unsupported_backend_type` expects an unsupported backend error, but current validation returns a missing coredns `etcd_endpoints` error first; tracked as `bahia-za2w`.
