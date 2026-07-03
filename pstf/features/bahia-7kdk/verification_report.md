# Verification Report: bahia-7kdk

## Observed behavior

- The default agentic assistant fails transcript replay because the sidecar rejects reads for kind `30316` with `event kind 30316 is not readable from the Bahia sidecar`.
- `internal/relaysidecar/policy.go` delegates sidecar readability to `kinds.IsReadableKind`.
- `internal/kinds/policy.go` made only `IsCanonicalObservableKind || IsOpenInteropKind` readable, and assistant transcript kind `30316` was missing from the canonical kind source and canonical observable switch.

## Intended behavior

- Assistant transcript kind `30316` is a canonical, service-authored assistant observable readable through the sidecar.
- Transcript content remains AEAD-encrypted ciphertext; this fix does not change encryption, key ownership, or transcript-store replay/decryption behavior.
- Go and frontend kind constants stay drift-checked.

## Implementation evidence

- Added `kinds.AssistantTranscript = 30316` to `internal/kinds/kinds.go`.
- Added `AssistantTranscript` to `IsCanonicalObservableKind`, which makes `IsReadableKind(30316)` and `IsBahiaProjectionKind(30316)` true.
- Added `ASSISTANT_TRANSCRIPT = 30316` to `web/src/lib/nostr/kinds.gen.js`.
- Added policy and relay-sidecar tests that assert kind `30316` is readable.

## Verification evidence

- `go test ./internal/kinds ./internal/relaysidecar` — passed. This includes `TestGeneratedFrontendKindsMatchCanonicalGoKinds`, `TestIsBahiaProjectionKind`, `TestIsReadableKind`, and the sidecar readable-filter regression.
- `go build ./...` — passed.
- `cd web && npm run test:unit` — passed: 73 test files, 576 tests.

## Deployment note

This fix only takes effect on the live relay `wss://bahia.sharegap.net/relay` after the Bahia edge stack is redeployed. The self-hosted `edge-01` runner is back online, so merging to `master` triggers Deploy Bahia Edge, which redeploys the sidecar.
