# FP-5l35 ContextVM Response Role Verification

Source: `fp-5l35`; design review: `fleet-planning/docs/designs/hygiene-observation-transport.md` section 2.7.

## Implementation

Bahia now classifies response-shaped JSON-RPC envelopes after NIP-59 trust, replay, deduplication, and routing checks but before request authorization. An envelope with no method and a present `result` or `error` field returns from responder dispatch without publishing an error event. No maintenance response consumer is added; that remains `fp-20aa`.

## Verification

Run on 2026-09-02:

- `go test ./internal/controlplane -run 'TestContextVMTransport_(IgnoresNIP59WorkerResponses|RejectsUnauthorizedRequester|JSONRPCParseAndMethodErrors|RandomKeyGiftWrapDispatchesAndResponds)$' -count=1` — PASS.
- `go test ./internal/controlplane -count=1` — PASS.

The regression covers both prior failure modes: an unauthorized worker result no longer produces `-32001`, and an authorized worker error response no longer produces `-32600`. Existing request behavior remains covered by the full package suite.
