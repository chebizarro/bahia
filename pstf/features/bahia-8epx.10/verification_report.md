# Verification Report: bahia-8epx.10

## Scope

This slice implements optional NIP-86 relay-owner HTTP administration for Bahia-owned or Bahia-authorized relays only. It does not modify existing NIP-42 websocket AUTH handling and does not route ContextVM kind `25910` application/control-plane mutations through NIP-86.

## Evidence

- `internal/config/config.go` adds `nostr.relay_administration`, disabled by default, with enabled-mode validation for an administrator private-key secret reference, explicit target refs, ws/wss relay URLs, optional HTTP management URLs, secure wss/https endpoints except localhost/loopback development targets, `bahia_owned` / `bahia_authorized` authorization assertions, and administrator pubkeys.
- `internal/adapters/nostr/relayadmin` adds an isolated NIP-86 HTTP client that permits only NIP-86 method names, sends `application/nostr+json+rpc`, signs NIP-98 kind `27235` authorization with `u=<relay URL>`, `method=POST`, and required `payload=<sha256 request body>`, and surfaces disabled, target, HTTP status, and relay JSON errors explicitly.
- Documentation in `docs/user-guide/nostr-integration.md` describes the safe default, config shape, secret-reference model, NIP-98 payload binding, Bahia-owned/authorized target boundary, and separation from ContextVM/NIP-42.

## Checks

- `GOCACHE=/tmp/bahia-go-cache go test ./internal/config ./internal/adapters/nostr/relayadmin` — passed.

## Remaining Work

No remaining work is expected for Beads epic `bahia-8epx.10` after the targeted tests pass. UI/CLI relay administration workflows, automatic reconciliation, NIP-11/NIP-66 metadata, and DM relay-list support remain outside this slice.
