# bahia-vvk0 verification report

## Implementation evidence

- Backend ack emission: `internal/controlplane/encrypted_transport.go` publishes a no-`id` JSON-RPC `notifications/progress` notification after ContextVM routing/auth and before handler invocation.
- Backend silence boundaries: routing mismatch returns before ack; unauthorized requesters return `-32001` without ack.
- Discovery capability: `internal/adapters/nostr/projector.go` adds `encrypted_controlplane.progress_ack` and `wire_version: contextvm-jsonrpc-v2` under `control_plane`; the encrypted request routing tag remains the historical `contextvm-jsonrpc-v1` discriminator for compatibility.
- Frontend parser/timeouts: `web/src/lib/nostr/encrypted-controlplane.js` clears only the short ack timer on progress and resolves/rejects only terminal JSON-RPC responses; `web/src/lib/nostr/encrypted-controlplane-result.js` validates gift-wrapped inner ContextVM events are service-authored before parsing their JSON-RPC content.
- Frontend preconditions: `web/src/lib/nostr/encrypted-controlplane-utils.js` and transport call sites reject before publish for missing feature, invalid service pubkey, or no connected Bahia relay.
- UI state: `web/src/routes/services/[id]/+page.svelte` shows “Secrets unavailable” with a control-plane-unreachable hint and logs via `console.info`.

## Verification run

- `go build ./...` — passed.
- `go test ./internal/controlplane ./internal/adapters/nostr` — passed.
- `cd web && npm run lint` — passed, 0 errors / 0 warnings.
- `cd web && npm run build` — passed.
- `cd web && npm run test:unit -- --run tests/unit/encrypted-controlplane.test.js` — passed, 22 tests.
- `cd web && npm run test:unit -- --run` — passed, 71 files / 546 tests.

## Beads

Beads DB was not modified per user instruction. The implemented slice maps to bahia-vvk0, bahia-vvk0.1, bahia-vvk0.2, and bahia-vvk0.3.
