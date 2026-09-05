# Verification Report — AI_FABRIC_SIGNER_FIRST_PROTOCOL

## Summary

`AI_FABRIC_SIGNER_FIRST_PROTOCOL` now has executable protocol verification for Bead `bahia-vn8o`.

The executable evidence follows the current repository authority in `docs/nostr-event-implementation-guide.md`: the historical AI/ML custom ranges `38390`-`38399` and `31980`-`31989` remain verified as legacy/migration inventory, while production signer-first runtime behavior uses canonical ContextVM kind `25910` plus canonical observable state/read-model events. The tests prove the legacy ranges are not revived in production subscriptions or production publish paths.

## Commands Run

Work/session setup:

- `bd prime`
- `bd show bahia-vn8o`
- `bd update bahia-vn8o --claim`

Verification commands, run on 2026-06-07:

- `gofmt -w internal/controlplane/ml_handlers.go internal/controlplane/ml_signer_first_protocol_test.go internal/controlplane/reactor_ml_requests_test.go internal/controlplane/reactor_operator_actions_test.go internal/adapters/nostr/subscriber_test.go`
- `GOCACHE=/tmp/bahia-go-build go test ./internal/controlplane` — passed
- `GOCACHE=/tmp/bahia-go-build go test ./internal/adapters/nostr` — passed

Review follow-up validation, run on 2026-06-07:

- `python3 -m json.tool pstf/features/AI_FABRIC_SIGNER_FIRST_PROTOCOL/{acceptance_criteria,test_matrix,feature_spec,defects}.json` — passed for each file
- `GOCACHE=/tmp/bahia-go-build go test ./internal/controlplane` — passed
- `GOCACHE=/tmp/bahia-go-build go test ./internal/adapters/nostr` — passed

## Acceptance Criteria Status

- `AC-SFP-001` — **verified**. `TestMLSignerFirstProtocolNamespacesAndCanonicalPublishing` proves the legacy command/result constants occupy exactly `38390`-`38399`, remain separate from the retired legacy DVM allocation as historical compatibility inventory, and production ML publishing emits signed ContextVM `25910` instead of legacy runtime events. Loom, Hive-CI, and SoulFactory kinds within `5000`-`7000` remain independent fleet-local protocols.
- `AC-SFP-002` — **verified**. `TestMLSignerFirstProtocolNamespacesAndCanonicalPublishing` proves the legacy read-model constants occupy exactly `31980`-`31989`; `TestSubscriberHandleEventInjectsCanonicalMLReadModelAndMarksEOSECaughtUp` proves canonical d-tagged ML state observation with EOSE catch-up.
- `AC-SFP-003` — **verified**. `TestMLSignerFirstRequestSubscriptionsAreScopedCanonicalContextVM` proves scoped canonical subscriptions; `TestMLBrowserRouteAvoidsHTTPPollingForCompletion` proves the ML route structurally calls the Nostr publish path for model import/deploy and has no `/api/v1/ml` HTTP completion polling or timer-based completion; existing relay primitive tests prove OK/CLOSED/AUTH/EOSE failure semantics.
- `AC-SFP-004` — **verified**. `TestHandleMLPhase1RequestsPublishCorrelatedTerminalResults` injects ML request events and verifies canonical ContextVM results include `e`, `p`, `status`, result `d`, and scoped tags including endpoint/model/version/recipe/run/deployment/artifact/worker/runtime/task/accelerator. `TestMLInjectedLegacyRequestsRequireCorrelationAndPublishCanonicalFailure` proves missing `d` is rejected with canonical correlation for the retained model-import direct handler only.
- `AC-SFP-005` — **verified**. Existing LLM request tests remain mapped as regression evidence that LLM compatibility behavior remains isolated from AI/ML semantics.

## Test Matrix Status

`test_matrix.json` has been updated from placeholders to executable tests and evidence commands. All criteria have mapped tests. Missing-`d` rejection is executable-proven only for the retained model-import direct handler; other retained ML direct handlers are covered for correlated happy-path/failure response tags.

## Defects / Remaining Gaps

- `D-SFP-EXEC-001` / `bahia-vn8o` — **resolved** by executable evidence.

No new Beads blocker was identified in this scope. HF/vLLM production integration evidence remains owned by sibling Bead `bahia-jicv`; this work did not modify `pstf/features/AI_FABRIC_HF_VLLM_DEPLOYMENT`.

## Protocol Notes

- Production AI/ML mutation publishing is signer-first ContextVM (`25910`) with stable `d`/JSON-RPC id/progress token correlation and relay accepted-count verification.
- Relay `OK` acceptance is represented at the publisher boundary by requiring at least one accepted relay; `TestMLCommandPublisherFailsWhenNoRelayAccepts` fails closed when no relay accepts.
- Relay `CLOSED`, `AUTH`, and EOSE primitive behavior is covered by deterministic relay/subscriber tests that inject channel events rather than sleeping.
- Long-running terminal truth remains relay-observed ContextVM result/read-model events, not REST response completion.

## Confidence Assessment

High for protocol-level executable verification in the touched scope. This does not claim HF/vLLM deployment backend production readiness, GPU availability, or artifact-gateway production integration; those remain under `bahia-jicv`.
