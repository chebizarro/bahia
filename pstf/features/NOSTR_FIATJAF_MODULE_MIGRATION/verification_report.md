# NOSTR_FIATJAF_MODULE_MIGRATION Verification Report

## Item 1 scope
Migrated the core canonical adapter/helper layer to `fiatjaf.com/nostr` while avoiding the broad downstream CLI/public-client/protocol-consumer sweep.

## Evidence
- `internal/adapters/nostr/**`, `internal/nostrutil/**`, and `internal/controlplane/signer.go` contain no `github.com/nbd-wtf/go-nostr` imports.
- `go.mod` pins `fiatjaf.com/nostr v0.0.0-20260611214214-c4534c716026`.
- Targeted tests passed:
  - `go test ./internal/nostrutil ./internal/adapters/nostr/...`
  - Results: helper package compiled, adapter package passed, relayadmin package passed.

## Nostr semantics checked
- Relay subscriptions remain callback/channel-driven with EVENT, EOSE, CLOSED, and AUTH handling.
- `RelayPool` uses fiatjaf subscriptions per scoped filter and disables fiatjaf's fake EOSE timeout to avoid timeout-based historical completion.
- Publishing continues to verify OK acceptance through fiatjaf relay publish results/errors and preserves partial-failure behavior.
- Inbound validation verifies event ID, pubkey, signature, timestamps, tag structure, event hash, and Schnorr signature before persistence or dispatch.
- Tests continue to exercise dedupe, invalid-event drops, EOSE aggregation, closed-reason handling, relay auth failure metadata, and scoped author filters.
- Review follow-up fixed multi-filter subscription coverage: `RelayPool.Subscribe` now rejects multi-filter calls instead of silently subscribing only one filter, and `SubscribeAllWithEOSE` only accepts relays with subscriptions for every requested filter.
- Review follow-up restored an explicit non-negative kind validation guard; fiatjaf kinds are unsigned, so negative kinds are unrepresentable, but the trust-boundary invariant remains encoded.

## Blockers outside Item 1
`go test ./internal/controlplane` is blocked by downstream consumers still using `github.com/nbd-wtf/go-nostr`. The first compile blocker is:

```text
internal/notifications/nostr_dm.go:74:45: cannot use ev (variable of struct type "github.com/nbd-wtf/go-nostr".Event) as "fiatjaf.com/nostr".Event value in argument to s.relayPool.Publish
```

Additional remaining imports exist in downstream Item 2/3 areas such as `internal/controlplane`, `internal/notifications`, `pkg/client`, `pkg/discovery`, `cmd/cli`, `internal/soulfactory`, and non-core adapters. These were intentionally not migrated in Item 1.
