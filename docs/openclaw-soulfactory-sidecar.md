# OpenClaw SoulFactory Sidecar

Bahia owns the OpenClaw SoulFactory adapter as a separate sidecar. It does not require direct upstream OpenClaw changes or REST lifecycle control APIs.

## Nostr contract

The sidecar:

- signs and publishes kind `30317` capability announcements with `runtime=openclaw`;
- subscribes to addressed kind `38384` requests using scoped filters for trusted SoulFactory controller pubkeys and this runtime pubkey;
- validates event ID/signature, schema, required tags, controller trust, runtime addressing, method params, idempotency key, and correlation fields before executing local side effects;
- publishes signed, correlated kind `38386` results with `#e`, `#p`, `method`, `idempotency-key`, `soul`, `agent-id`, `spec-hash`, `schema`, and `status` tags;
- uses EOSE only for backfill transition and never infers terminal completion from timeout, relay closure, or polling;
- persists idempotency/result fingerprints in a local JSON store so exact replays after restart republish a cached result instead of repeating side effects.

## Local OpenClaw control surface

Run the sidecar with a local command driver. The command receives one JSON `OpenClawControlInvocation` on stdin and must return an `OpenClawControlOutcome` JSON on stdout:

```json
{
  "status": "success",
  "result": {
    "agent_id": "agent-alice",
    "runtime": "openclaw",
    "runtime_binding": "openclaw://agents/agent-alice",
    "state": "running",
    "spec_hash": "sha256:...",
    "observed_at": 1715700005,
    "warnings": []
  },
  "error": null
}
```

The command can wrap supported local OpenClaw surfaces such as the OpenClaw CLI/gateway-local RPC or plugin SDK execution path. It must not expose or depend on a REST SoulFactory lifecycle API.

## Example

```bash
go run ./cmd/openclaw-soulfactory-sidecar \
  -relays wss://relay.example \
  -private-key "$OPENCLAW_SOULFACTORY_PRIVATE_KEY" \
  -trusted-controller-pubkeys "$SOULFACTORY_CONTROLLER_PUBKEYS" \
  -control-relays wss://relay.example \
  -idempotency-store ~/.cache/bahia/openclaw-soulfactory-sidecar-idempotency.json \
  -command /usr/local/bin/openclaw-soulfactory-control
```

The command also receives `SOULFACTORY_METHOD`, `SOULFACTORY_AGENT_ID`, and `SOULFACTORY_SPEC_HASH` environment variables for routing convenience.
