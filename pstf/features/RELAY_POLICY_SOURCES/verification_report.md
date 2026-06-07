# Relay Policy Sources Verification

Beads: `bahia-8epx.2`, `bahia-8epx.2.1`, `bahia-8epx.2.2`, `bahia-8epx.2.3`

## Scope

This slice implements backend config/validation semantics for independent relay policy sources. It intentionally does not publish new NIP-51 sets, normalize browser discovery, implement CLI discovery fallback, add NIP-86 administration, or touch SoulFactory/OpenClaw/ngit relay separation.

## Evidence

Targeted checks run on 2026-06-07:

```bash
go test ./internal/config
# ok   github.com/openagentsinc/bahia/internal/config 0.276s

go test ./internal/adapters/nostr
# ok   github.com/openagentsinc/bahia/internal/adapters/nostr 0.239s
```

## Expected coverage

- `nostr.service_relays` is the canonical service publish/backfill source.
- `nostr.relays` remains a compatibility alias for service relays and is not browser policy.
- `nostr.browser_relays` remains browser-safe bootstrap/read policy.
- `nostr.contextvm_relays` is an explicit ContextVM request/reply source and resolves to browser relays only when absent.
- `nostr.relay_auth_unavailable` accepts only `exclude_and_fail`.
- Removed private mirror fields remain rejected.

## Remaining work outside this slice

Projection consumers for `bahia-browser-v1`, `bahia-contextvm-v1`, `bahia-service-v1`, and advisory NIP-65 publication remain in downstream relay-strategy Beads.
