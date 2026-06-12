# NOSTR_FIATJAF_MODULE_MIGRATION Verification Report

## Item 1 scope
Migrated the core canonical adapter/helper layer to `fiatjaf.com/nostr` while avoiding the broad downstream CLI/public-client/protocol-consumer sweep.

## Item 1 evidence
- `internal/adapters/nostr/**`, `internal/nostrutil/**`, and `internal/controlplane/signer.go` contain no `github.com/nbd-wtf/go-nostr` imports.
- `go.mod` pins `fiatjaf.com/nostr v0.0.0-20260611214214-c4534c716026`.
- Targeted tests passed:
  - `go test ./internal/nostrutil ./internal/adapters/nostr/...`
  - Results: helper package compiled, adapter package passed, relayadmin package passed.

## Item 1 Nostr semantics checked
- Relay subscriptions remain callback/channel-driven with EVENT, EOSE, CLOSED, and AUTH handling.
- `RelayPool` uses fiatjaf subscriptions per scoped filter and disables fiatjaf's fake EOSE timeout to avoid timeout-based historical completion.
- Publishing continues to verify OK acceptance through fiatjaf relay publish results/errors and preserves partial-failure behavior.
- Inbound validation verifies event ID, pubkey, signature, timestamps, tag structure, event hash, and Schnorr signature before persistence or dispatch.
- Tests continue to exercise dedupe, invalid-event drops, EOSE aggregation, closed-reason handling, relay auth failure metadata, and scoped author filters.
- Review follow-up fixed multi-filter subscription coverage: `RelayPool.Subscribe` now rejects multi-filter calls instead of silently subscribing only one filter, and `SubscribeAllWithEOSE` only accepts relays with subscriptions for every requested filter.
- Review follow-up restored an explicit non-negative kind validation guard; fiatjaf kinds are unsigned, so negative kinds are unrepresentable, but the trust-boundary invariant remains encoded.

## Item 1 blockers later resolved
`go test ./internal/controlplane` was blocked after Item 1 by downstream consumers still using `github.com/nbd-wtf/go-nostr`, first observed at `internal/notifications/nostr_dm.go`. Items 2 and 3 resolved the downstream migration and active dependency cleanup.

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

## Item 3 scope
Completed the residual repo-wide migration in `cmd/**`, `pkg/client/**`, `internal/api/**`, `internal/app/**`, `internal/controlplane/**`, `internal/mcp/**`, `internal/service/**`, `internal/soulfactory/**`, and `test/integration/**`, plus module dependency cleanup.

## Item 3 dependency cleanup
- `go.mod` and `go.sum` no longer contain `github.com/nbd-wtf/go-nostr`.
- `go.mod` retains `fiatjaf.com/nostr v0.0.0-20260611214214-c4534c716026` after `GOCACHE=/tmp/bahia-go-cache go mod tidy`.
- Active scoped search passed with no matches:
  - `rg -n "github.com/nbd-wtf/go-nostr|go-nostr" go.mod go.sum cmd pkg internal test/integration --glob '!_git_data/**'`

## Item 3 Nostr semantics checked and hardened
- Controlplane request subscription author filters now fail closed when configured authorized/factory pubkeys are malformed; malformed non-empty allowlists cannot collapse into broad authorless subscriptions. Verified by `TestRequestSubscriptionAuthorsFailClosedForMalformedConfiguredPubkeys`.
- Assistant downstream terminal result handling now validates result kind, `#e` request correlation, nonzero and future-bounded timestamp, canonical event ID, and Schnorr signature before trusting terminal status. Verified by `TestDownstreamResultMatchesReceiptRejectsWrongCorrelation` and package tests.
- SoulFactory relay bus no longer marks historical catch-up complete on subscribe failure, CLOSED-before-EOSE, or cancellation. Merged EOSE channels close only after real EOSE from all underlying subscriptions. Verified by `TestRelayBusEOSEWaitsForRelayThatRecoversAfterInitialSubscribeFailure` and `TestRelayBusClosedBeforeEOSEReissuesWithoutCompletingBackfill`.
- SoulFactory backfill/query tests that previously used placeholder production-like relay URLs now use deterministic fake relay EOSE behavior so tests do not depend on hidden network behavior or encode hardcoded production paths.
- Residual Item 3 code and tests construct canonical signed fiatjaf events, typed IDs/pubkeys/kinds/signatures, and scoped relay filters. No polling/sleep completion logic was introduced for event delivery or historical catch-up.

## Item 3 verification evidence
- Targeted SoulFactory review test:
  - `GOCACHE=/tmp/bahia-go-cache go test ./internal/soulfactory -timeout 30s`
  - Result: passed, `ok github.com/openagentsinc/bahia/internal/soulfactory 7.719s`.
- Targeted review packages:
  - `GOCACHE=/tmp/bahia-go-cache go test ./internal/controlplane ./internal/service ./internal/soulfactory`
  - Result: passed for all listed packages.
- Targeted residual Item 3 packages:
  - `GOCACHE=/tmp/bahia-go-cache go test ./cmd/... ./pkg/client/... ./internal/api/... ./internal/app/... ./internal/controlplane/... ./internal/mcp/... ./internal/service/... ./internal/soulfactory/... ./test/integration/...`
  - Result: all listed packages passed or reported `[no test files]`.
- Full repository test suite:
  - `GOCACHE=/tmp/bahia-go-cache go test ./...`
  - Result: all packages passed or reported `[no test files]`.
- Module cleanup:
  - `GOCACHE=/tmp/bahia-go-cache go mod tidy`
  - Result: passed with no reintroduction of the old module.
- Whitespace hygiene:
  - `git diff --check`
  - Result: passed.

## Historical reference review
A broad repository search still finds `github.com/nbd-wtf/go-nostr` / `go-nostr` only in historical planning, archival, or PSTF documentation, not in active Go imports or module dependencies. Allowed historical references observed before closeout include:
- `docs/archive/REVIEW-AND-ROADMAP-2024.md`
- `docs/analysis/nostr-fiatjaf-migration-orchestration.md`
- `pstf/features/NOSTR_FIATJAF_MODULE_MIGRATION/*`
- `pstf/features/BUCKET5_BLOSSOM_AUTH_BACKEND_TESTS/verification_report.md`

## Remaining tracked work
- `bahia-ncxo` remains open for NIP-44 stored-secret compatibility verification/migration because no representative persisted legacy ciphertext fixture was available in Items 2 or 3.
- Additional hardening follow-ups from Item 3 review are tracked in Beads for expected responder-author validation, deterministic relay-protocol edge tests, and bounded/persistent ContextVM idempotency. These are not active go-nostr migration blockers.
- `bahia-u2ma` can be closed after final commit/pull/rebase/push/status verification because the active repo-wide module migration and cleanup are complete.
