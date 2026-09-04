# ContextVM result delivery verification

## Scope

Bead `bahia-xwpsw` verifies bounded terminal-result delivery across the signer-first client, ContextVM server transport, relay publication outcomes, idempotent replay cache, and NIP-59/NIP-44 encrypted mode.

## Acceptance evidence

- `TestContextVMResultDeliveryE2EPlainRoundTrip` sends a signed client request through the fake in-process relay to `EncryptedRequestTransport.HandleContextVMEvent` and returns the signed server result to the client subscription.
- `TestContextVMResultDeliveryE2ERetryReplaysCachedDesiredStateHash` drops the first accepted terminal response, lets the client's 25 ms result timeout elapse, and proves the second publish returns the cached `desired_state_hash` while the handler count remains one.
- `TestContextVMResultDeliveryE2EEncryptedRoundTrip` exercises a local-key signer with real NIP-44 encryption, NIP-59 wrapping, server unwrap, signed inner response, client decrypt, and inner/outer correlation checks.
- `TestContextVMResultDeliveryE2EDualRelaySubscriptionPartialFailure` models failed subscription establishment on relay A and response publication only on relay B; the client completes using the actually subscribed relay set.
- Existing focused client tests cover EOSE activation, zero subscriptions, bounded retry, relay accounting, command construction, and correlation diagnostics.
- Existing control-plane and repository tests cover bounded server response publication retry, multi-relay partial success, in-memory replay, PostgreSQL restart replay, and response record storage.

## Implementation note

The end-to-end lost-result test exposed that event-id deduplication preceded idempotency-cache lookup. Request processing now checks the completed-response cache first, while still suppressing duplicate requests whose first execution is in flight. Response-event deduplication remains unchanged.

## Quality gates

Verified on 2026-09-03 from `/Users/bizarro/Documents/Projects/bahia`:

- `go build ./...` — PASS
- `go vet ./...` — PASS
- `go test ./...` — PASS

## Deviations

- The one timeout-dependent acceptance regression uses a 25 ms real timeout because the client has no injected clock; all relay activation and delivery coordination is channel-driven with no sleeps.
- Relay identities are in-process transports using local (`ws://127.0.0.1`) and public-shaped (`wss://relay.example`) URLs; no external relay or network dependency is required.
