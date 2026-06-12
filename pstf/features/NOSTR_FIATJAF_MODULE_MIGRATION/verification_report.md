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

## Item 2 scope
Migrated downstream protocol/NIP consumers in `internal/adapters/{secrets,signet,loom,hiveci,blossom,sbom,signing}/**`, `internal/auth/**`, `internal/fipsbridge/**`, `internal/notifications/**`, `internal/nostrmigration/**`, and `pkg/discovery/**` from `github.com/nbd-wtf/go-nostr` to `fiatjaf.com/nostr`, reusing `internal/nostrutil` for canonical key decoding, signing, pubkey/ID hex handling, and NIP-44 conversation key derivation.

## Item 2 evidence
- Scoped import search passed: no `github.com/nbd-wtf/go-nostr` or `go-nostr` references remain in the Item 2 package set.
- Targeted tests passed with writable build cache:
  - `GOCACHE=/tmp/bahia-go-cache go test ./internal/adapters/secrets ./internal/adapters/signet ./internal/adapters/loom ./internal/adapters/hiveci ./internal/adapters/blossom ./internal/adapters/sbom ./internal/adapters/signing ./internal/auth ./internal/fipsbridge ./internal/notifications ./internal/nostrmigration ./pkg/discovery`
- Results: all listed packages passed.

## Item 2 Nostr semantics checked
- NIP-44 consumers now derive canonical conversation keys from decoded `fiatjaf.com/nostr` public/secret key types and continue to encrypt/decrypt with NIP-44 payload APIs.
- NIP-46 Signet client now uses `fiatjaf.com/nostr/nip46` and canonical `nostr.Pool`, `SecretKey`, and event signing APIs.
- NIP-98 and Blossom authorization events are canonical signed events; tests verify event kind/tags/pubkey/signature behavior.
- Loom, Hive-CI, FIPS bridge, nostr migration runner, and discovery resolver retain event-driven subscription loops with scoped filters, EOSE handling, CLOSED handling, AUTH retry handling, validation before persistence/dispatch, and dedupe/replaceable semantics where already present.
- Scoped subscription filters now use canonical `[]nostr.Kind` and `[]nostr.PubKey`; FIPS/discovery/loom reject invalid configured author/worker pubkeys before a broad authorless filter can be used.

## Item 2 review follow-up
- Oracle review identified that relay backfill in `internal/nostrmigration/runner.go` recorded relay events before validation. Item 2 now validates historical backfill events before persistence/migration using canonical event ID/signature checks, tag structure checks, created_at presence, future-skew bounds, and configured backfill since/until bounds without rejecting legitimately old legacy events solely by age.
- Added `TestRunnerRejectsInvalidRelayBackfillEventBeforeRecording` to prove tampered relay backfill events are rejected before repository recording or migration publishing.

## Item 2 remaining tracked work
- `bahia-ncxo` tracks verification/migration for any existing NIP-44 stored secrets created by the prior private-key-as-recipient self-encryption path.
- Item 3 remains responsible for public clients, CLI, residual controlplane/service/soulfactory/test imports, dependency cleanup, repo-wide verification, and final removal of `github.com/nbd-wtf/go-nostr` from `go.mod`/`go.sum`.
