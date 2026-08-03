# Verification Report — bahia-eajyr

## Scope

Backend durability core for fleet task `fp-bahia-relay-policy-durability`: PostgreSQL last-known-good relay policy projection, validated multi-relay hydration, fail-safe promotion, and signer-first ContextVM provenance. UI states and upgrade/backup workflows are outside this item.

## Verification

- `go test ./internal/controlplane ./internal/adapters/nostr ./internal/repository ./internal/app` — passed.
- 2026-08-03 blocker verification: `go test -count=1 ./internal/adapters/nostr ./internal/controlplane ./internal/repository` and `go build ./...` — passed.
- `go build ./...` — passed. The sandbox denied a non-fatal Go module stat-cache temp write after the successful build; the command exited 0.
- Oracle review completed. Its blocking startup-order finding was fixed by synchronously loading, validating, and applying the PostgreSQL head before ContextVM/control-plane activation. Relay-policy apply is fail-closed when the durable store is unavailable.
- The stored policy relay set is merged back into the hydration pool on restart so image/config changes do not narrow refresh to a single bootstrap relay.

## Acceptance Mapping

- AC1: durable preload and stored-relay recovery tests cover restart/image replacement with canonical bootstrap unavailable.
- AC2: eligible-relay union, source provenance, per-relay EOSE, terminal accounting for pre-EOSE relay death, outage resubscription/recovery, and post-EOSE drain tests cover secondary delivery.
- AC3: zero-event, executable ordering predicate, restored-cached relay confirmation, invalid event, corrupt projection, AUTH/outage/timeout, and PostgreSQL promotion tests prove last-known-good retention.
- AC4: explicit signed empty is accepted; unavailable projection returns no inferred empty/default state.
- AC5: signer-first ContextVM tests cover projection availability, provenance, hash, source relay, last-sync, freshness, and fail-closed mutation without PostgreSQL.

See `acceptance_criteria.json` and `test_matrix.json` for the test-level mapping.

## Security

The implementation stores and exposes only sanitized source relay provenance and does not log or record signer secrets, private keys, bunker URLs, relay credentials, or relay administration credentials. No unsigned REST path was added.
