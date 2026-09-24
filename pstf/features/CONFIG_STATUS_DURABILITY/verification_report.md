# Config status durability — bahia-1antv

## Contract and design

Kind 30900 schema `cascadia.config.status.v2` uses a separate address per desired
event and phase. Terminal applied truth survives every phase's arrival order,
equal timestamps, later nonterminal publications, and later receipts for older
versions. Replay uses greatest effective version; accepted alone never clears
drift. V1 records remain readable. No comparator, store, or publisher locking
change is required. See the canonical event implementation guide for wire shape
and retention/upgrade trade-offs.

## Observed counterfactual before the fix

Ran `go test ./internal/relaysidecar -run '^TestConfigStatusAppliedSurvivesReplay$' -count=1 -v`
before changing production code. Both accepted-lowest-ID subtests failed with:

`terminal truth lost; retained statuses: map[accepted:true]`

Accepted first: SQLite returned `duplicate: event already exists` for applied;
Khatru and the production publisher nevertheless acknowledged both publications.
Applied first: accepted replaced applied. Both applied-lowest-ID cases passed.
Thus the source diagnosis is confirmed, except the already-merged NIP-01 fix
means lowest ID, not first arrival, now decides equal-time retention.

Repeated the final regression against HEAD's two production files (temporarily
restored only inside this worktree, then restored the fix) and observed the same
2 failures / 2 passes. The final test includes real Handle/activation and retains
the same counterfactual failure, not just direct status construction.

## Acceptance mapping

- `TestConfigStatusAppliedSurvivesReplay`: fixed clock/key, both lexical winners,
  both arrival orders, real Handle/activation, signing, production publisher,
  Khatru and SQLite; reopen the durable store and run production ListDrift using
  only retained events. A read adapter supplies SQLite results to ListByKind;
  publications never pass through an append-only test repository.
- `TestConfigStatusAppliedVersionsSurviveReplay`: multiple applied versions in
  the same second and both arrival orders, delayed old applied receipts, later
  accepted/rejected status, new desired drift until activation, restart replay.
- `TestConfigStatusMixedSchemaReplay`: legacy applied receipt, new v2 accepted
  and applied receipts, a later legacy lower-version receipt, and restart replay.
- `TestConfigConsumerPublishesStatusesConcurrently`: rendezvous forces Handle
  and activation publications to overlap; both real-store coordinates survive.
- `TestConfigFabricStatusCoordinatesAndTargetBinding`: valid v1/v2 forms,
  phase/target/schema isolation and applied target/version binding.
- Existing service and consumer tests preserve activation order, rollback,
  rejection and v1 projection behavior.

## Verification

Review of the inline worktree patch identified no blocking issues and recommended
mixed-schema replay coverage; that recommendation is implemented above.

Final worktree verification (all exit 0):

- `go build ./...`
- `go vet ./...`
- `go test ./...`
- `go test -race ./internal/relaysidecar ./internal/service -run 'TestConfig(Status|Consumer|Fabric)' -count=10 -cpu=1,4`
- `make race` (`CGO_ENABLED=1 go test -race ./... -count=1`)
- `git diff --check`

The full gates were rerun after adding the mixed-schema regression. The final
counterfactual retained the expected exit 1 before restoring the fix. All scoped
acceptance criteria pass. Issue lifecycle remains user-owned; no bd invocation,
push or merge.

## Context triage

Jev `find-lines` narrowed publisher, drift-reader, and policy-document reads
(presence 0.79, 0.91, 0.93). Eight requests, 48,027 input tokens, approximately
$0.002017; no relevance-threshold misfire identified.
