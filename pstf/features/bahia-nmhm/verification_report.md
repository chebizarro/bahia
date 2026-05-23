# Verification Report: bahia-nmhm

## Observed behavior

- `internal/domain/replication.go` defines `ReplicationPolicy` keyed by service.
- `ReplicationTarget` carries worker pubkey, strategy string, max staleness duration, and continuity modes that require the target.
- Tests cover the planned strategies `snapshot`, `incremental`, `event_mirror`, `secret_backup`, and `scb_sync` as string values.

## Verification evidence

- `go test ./internal/domain` passed.

## Known boundaries

- Nostr serialization and reactor handling for kind 31403 are owned by the concurrent hot-file integration work and were not changed here.
