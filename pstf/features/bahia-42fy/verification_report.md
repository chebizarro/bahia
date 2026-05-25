# Verification Report

## Evidence

- `go test ./internal/repository` — passing.
- `go test ./internal/...` — passing.
- `internal/domain/worker.go` defines telemetry, pressure assessment, pressure enums, and per-signal detail.
- `internal/db/migrations/000035_worker_telemetry_pressure.up.sql` adds `workers.telemetry` and `workers.pressure` as `JSONB NOT NULL DEFAULT '{}'::jsonb`.
- `internal/repository/pg_worker.go` marshals/unmarshals telemetry and pressure and applies `WHERE EXCLUDED.last_advertisement_at >= workers.last_advertisement_at` to the conflict update.

## Scope Confirmation

No Nostr processor, event bus, placement service, or loom-worker files were changed.
