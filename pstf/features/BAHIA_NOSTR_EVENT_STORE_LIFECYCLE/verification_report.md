# Verification report: Bahia Nostr event-store lifecycle

Status: repository verification in progress. Live rollout evidence must be
appended only after a supported backup, canary archive protection, prune,
restore, restart, relay-failure recovery, and rollback proof have completed.

## Production baseline (read-only, 2026-09-12)

- PostgreSQL 16 database size: 19 GB.
- `nostr_events`: 19 GB total, 13 GB heap, 1.8 GB indexes, approximately
  12.0 million live rows and 836,000 dead rows.
- Observed growth: approximately 285,000 rows and 450 MB per day.
- Pending outbox queries use the existing partial outbox index.
- No production rows or schema objects were changed during this baseline.

## Repository verification

- Production-shaped PostgreSQL 16 lifecycle test covers migration, concurrent
  indexes, deterministic bounded claim, crash/reconnect, export, protection,
  bounded prune, pending/new/unclaimed preservation, and idempotent restore.
- Unit tests cover atomic mode-0600 artifacts, digest mismatch rejection,
  explicit kind scope, metadata-only startup migration, and metrics wiring.
- Focused repository, archive, migration, telemetry, application, and CLI tests
  pass against PostgreSQL 16.
- The complete Go suite passes; the existing read-only policy-seed permission
  fixture is additionally proven under an unprivileged UID because container
  root can bypass the permission condition it tests.
- `go vet ./...` passes.
- The repository multi-stage Dockerfile builds successfully and includes the
  `bahia-event-archive` operator binary.

Live rollout and independent acceptance remain pending.
