# Verification Report — bahia-igqp

## Verification run

```sh
GOCACHE=/tmp/bahia-go-cache go test ./internal/adapters/nostr ./internal/config ./internal/domain
```

Result: PASS

## Evidence

- FIPS config defaults and validation compile through `./internal/config`.
- Worker and DNS domain additions compile through `./internal/domain`.
- FIPS subscriber tests inject signed Kind 37195 EVENTs directly and verify parsing, pubkey matching, default no-auto-registration behavior, and fd00::/8 overlay address derivation without sleeps or polling.

## Boundary confirmation

This slice did not modify `internal/adapters/nostr/projector.go`, `cmd/fips-bahia-bridge/`, or `internal/app/app.go`.
