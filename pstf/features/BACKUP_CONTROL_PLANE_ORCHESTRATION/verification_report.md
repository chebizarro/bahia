# Verification report — BACKUP_CONTROL_PLANE_ORCHESTRATION

Date: 2026-05-21
Status: First Kopia-backed backup-run orchestration slice verified for JW-4/JW-5/JW-6.

## Evidence gathered

- Implemented backup reactor integration for `BackupRunRequest` kind `38400` using the existing reactor path for inbound Nostr event validation, event ID dedupe, scoped authorized-author filters, EOSE catch-up, CLOSED handling, and NIP-42 AUTH retry.
- Implemented `BackupRunResponder` publication for `BackupRunStatus` `6981`, `BackupRunResult` `38410`, `BackupRunAttestation` `31310`, and `BackupVerificationAttestation` `31311` when verification runs.
- Responder publication uses per-relay `PublishWithResults` outcomes and persists accepted/rejected relay summaries and rejection reasons into backup run / verification publish summaries.
- Implemented backup read-model projection for recipe, policy, repository, run, and verification records using replaceable kinds `31992`, `31993`, `31995`, `31996`, and `31997` for the first slice. Implemented mutable registry d-tags use immutable IDs to avoid stale live coordinates after renames.
- `BackupRunState.restore_eligible` is derived as true only when the run succeeded and `verification_status == succeeded`; skipped, failed, unsupported, and pending verification remain false.
- App composition now constructs the backup repository, registry service, Kopia backend, coordinator, responder, projector source, reactor dependencies, and background recovery runner.

## Verification performed

- Focused tests:
  - `go test ./internal/controlplane ./internal/adapters/nostr ./internal/app`
  - `go test ./internal/domain ./internal/repository ./internal/service ./internal/adapters/backup ./internal/controlplane ./internal/adapters/nostr ./internal/app`
- Full suite:
  - `go test ./...`
- PSTF JSON validation after evidence update:
  - `feature_spec.json`
  - `acceptance_criteria.json`
  - `test_matrix.json`

## Test evidence

- `internal/controlplane/reactor_backup_requests_test.go`
  - backup run kind `38400` is included in the default scoped control-plane subscription filter
  - authorized backup request creates one durable run keyed by requester/kind/d-tag and invokes the coordinator executor
  - duplicate requester/kind/d-tag requests do not create a second run or backend execution
  - responder publishes signed result and attestation events with request correlation tags and persisted relay outcome summaries
- `internal/adapters/nostr/projector_backup_test.go`
  - snapshot projection emits backup recipe/policy/repository/run/verification read models
  - mutation projection republishes changed verification and run state
  - `restore_eligible=false` for skipped verification and true only after successful verification
- Existing prerequisite tests retained coverage for:
  - backup domain validation and restore eligibility (`internal/domain/backup_test.go`)
  - backup registry idempotency and verification-first fail-closed completion (`internal/service/backup_registry_test.go`)
  - Kopia coordinator snapshot/verify/cancellation behavior (`internal/service/backup_run_coordinator_test.go`)
  - real Kopia CLI adapter command/config/output behavior (`internal/adapters/backup/kopia_backend_test.go`)
  - relay publish outcomes, AUTH/CLOSED reason helpers, duplicate-as-success, and zero-accepted failure (`internal/adapters/nostr/protocol_frames_test.go`)

## Current result

The first Kopia-backed backup-run vertical slice is verified: Bahia accepts authorized Nostr backup run requests, creates idempotent durable runs, hands execution to the Kopia coordinator, publishes signed status/result/attestation events with relay OK outcome summaries, and projects verification-aware read models without treating unverified snapshots as restore-eligible.

## Remaining work tracked outside this first slice

Restore request/approval/result workflows, retention execution, UI flows, Velero support, and native snapshot adapters are outside JW-4/JW-5/JW-6 and must remain tracked in Beads before the broader backup feature is complete.
