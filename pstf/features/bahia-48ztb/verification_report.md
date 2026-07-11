# Verification Report: bahia-48ztb

Date: 2026-07-11

## Result

Passed.

## Evidence

- Added `RequireReal` to the Signet client configuration and made `RequireReal=true` fail closed when no bunker URI is configured.
- Wired `config.DevMode` and `BAHIA_DEV_MODE=true` so app initialization can explicitly opt into dev/mock signing.
- Wired Loom canonical projection, SoulFactory, and operator assistant Signet construction to require real Signet outside dev mode.

## Tests

- `go test ./internal/adapters/signet ./internal/config ./internal/adapters/nostr ./internal/app ./internal/adapters/hiveci ./internal/adapters/loom`
- `go test ./...`

Both commands passed on 2026-07-11.
