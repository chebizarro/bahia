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

---

## 2026-05-23 — Backup operational posture slices (`bahia-bc66`, `bahia-gzjs`, `bahia-j46o`)

### Evidence gathered

- `internal/domain/backup.go` now models verification mode, evidence details, explicit `RestoreEligibility` state/reason, verification policy failures, structured failure categories, restore approval requirements, structured approval reasons, and retention failure categories.
- `internal/db/migrations/000031_backup_operational_read_models.up.sql` persists the new operational state on backup runs, verification records, restore runs, and retention runs with a reversible down migration.
- `internal/service/backup_registry.go` and `backup_run_coordinator.go` persist verification records before terminal backup completion, surface policy-blocked verification failures, recompute restore eligibility, and publish run/verification changes.
- `internal/controlplane/backup_restore_handlers.go` and `internal/service/backup_registry_restore.go` capture approval reason tags/content, snapshot policy-driven approval requirements, record who/when/why approval provenance, and preserve restore verification policy at request time.
- `internal/service` backup/restore/retention coordinators categorize failure causes for load input, backend resolution/health, snapshot, verification, restore, retention, policy, cancellation, and timeout paths.
- `internal/adapters/nostr/projector.go` exposes enriched `BackupRunState`, `BackupVerificationState`, `BackupRestoreState`, retention outcome state via `31994`, and fleet `BackupRuntimeObservationState` `31999` with last outcomes, pending approvals, queued/running/stale counts, backend health failures, and failure category summaries.

### Verification performed

- `go test ./internal/domain ./internal/service ./internal/controlplane ./internal/adapters/nostr ./internal/repository`

### Test evidence

- `internal/repository/pg_backup_controlplane_test.go` validates persistence/scanning for new backup run, restore, retention, and verification operational columns.
- `internal/service/backup_registry_test.go`, `backup_registry_restore_test.go`, `backup_registry_retention_test.go`, and coordinator tests validate fail-closed verification/restore/retention transitions and explicit failure categories.
- `internal/controlplane/reactor_backup_requests_test.go` validates pending approval still gates execution and signed approval triggers execution through the event-driven reactor path.
- `internal/adapters/nostr/projector_backup_test.go` validates backup projection behavior; projector implementation now includes enriched verification/approval/provenance fields plus fleet posture publication.

### Current result

The three operational posture beads are verified for the requested cross-cutting scope: verification is first-class, restore approval state is operator/audit friendly, and backup observability/provenance is projected through durable read models without adding polling or fake backend behavior.

### Review follow-up

Oracle review findings were addressed before closeout:

- operational read-model migrations were renumbered from duplicate `000030` to `000031` to avoid colliding with the BackupDefinition migration
- fleet `last_restore` and `last_retention` outcomes now ignore queued/running rows so pending work cannot overwrite the last terminal outcome
- backup runtime observation staleness uses a projector option (`WithBackupProjectionStaleTimeout`) with the 15-minute default reflected in `stale_after_seconds`

### Full-suite note

- `go test ./...` was run on 2026-05-23 after the operational posture changes. The backup/control-plane touched packages passed, but the full suite failed in unrelated packages already tracked from the earlier BackupDefinition slice:
  - `internal/api/handlers`: `continuity_test.go` still calls `NewContinuityHandler` with the old one-argument constructor; tracked as `bahia-b6h3`.
  - `internal/config`: `TestDNSValidationEnabled/unsupported_backend_type` expects an unsupported backend error, but current validation returns a missing coredns `etcd_endpoints` error first; tracked as `bahia-za2w`.

---

## 2026-07-02 — Wave-2 backup ContextVM mutation UI slice

### Intended behavior

The web Backup area must be able to create and operate backup control-plane objects without inventing request/response semantics over Nostr. Browser actions publish encrypted ContextVM requests; Bahia bridges those requests to canonical backup command events, verifies relay publish acceptance through the existing publisher path, and leaves durable progress/terminal truth to scoped backup read-model projections.

### Evidence gathered

- `internal/controlplane/backup_contextvm_handlers.go` registers encrypted ContextVM method names for repository registration, policy/recipe/definition apply, run, verification, restore, retention, repository probe, and restore approval.
- Each ContextVM handler publishes the existing canonical backup command action and event kind consumed by the Nostr reactor (`38400`-`38409`) rather than calling backup services directly or polling for completion.
- `web/src/lib/stores/public-controlplane.svelte.js` exposes matching browser helpers for the registered method strings and includes idempotency keys plus narrow correlation tags.
- `web/src/routes/backup/BackupMutationPanel.svelte`, list pages, and detail pages add operator controls for repository registration, policy/recipe/definition apply, run now, verification, restore request, retention enforcement, repository probe, and restore approval/rejection.
- `docs/user-guide/features/backup.md` documents the mutation controls and clarifies that command acceptance is separate from durable backup progress/result projections.

### Verification performed

- `go test ./internal/controlplane ./internal/app` — passed
- `go build ./...` — passed
- `cd web && npm run lint && npm run build && npm run test:unit -- --run` — passed after `npm ci` restored missing web dependencies

### Test evidence mapping

- `internal/controlplane/contextvm_handlers_registration_test.go` validates every backup web ContextVM method dispatches, publishes the expected canonical command kind, carries the canonical `command` tag/action, and returns a submitted ACK with request event id.
- `web/tests/unit/public-controlplane.test.js` validates the web helpers use the exact registered ContextVM method strings for backup repository/policy/recipe/definition mutation plus run, verification, restore, and retention operations.

### Current result

The Wave-2 backup mutation UI bridge is verified: all requested backup creation/operation controls publish encrypted ContextVM methods that bridge to canonical backup Nostr command events, and web acknowledgement is limited to command submission while durable workflow truth remains in backup read-model projections.
