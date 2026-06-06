# OpenClaw SoulFactory Sidecar

Bahia owns the OpenClaw SoulFactory adapter as a separate sidecar. It does not require direct upstream OpenClaw changes or REST lifecycle control APIs.

Soul provisioning is initiated by signed Nostr events, not by REST. Operators publish a `31952` Soul draft and a correlated `5950` provisioning request; SoulFactory then drives OpenClaw with runtime control events and publishes observable progress/read-model events. Adding REST provisioning or lifecycle routes is a non-goal for this MVP.

## Nostr contract

The sidecar:

- signs and publishes kind `30317` capability announcements with `runtime=openclaw`;
- subscribes to addressed kind `38384` requests using scoped filters for trusted SoulFactory controller pubkeys and this runtime pubkey;
- validates event ID/signature, schema, required tags, controller trust, runtime addressing, method params, idempotency key, and correlation fields before executing local side effects;
- publishes signed, correlated kind `38386` results with `#e`, `#p`, `method`, `idempotency-key`, `soul`, `agent-id`, `spec-hash`, `schema`, and `status` tags;
- uses EOSE only for backfill transition and never infers terminal completion from timeout, relay closure, or polling;
- persists idempotency/result fingerprints in a local JSON store so exact replays after restart republish a cached result instead of repeating side effects.

The browser/server provisioning request shape is:

- `31952` draft: editable desired soul spec, including runtime, relay policy, permissions, workspace, assets, and `spec_hash`.
- `5950` request: tags include `agent-id`, `draft`, `draft-event`, `e=<draft-event-id>;marker=draft`, `spec-hash`, `runtime`, `runtime-pubkey`, `capability`, `method=soulfactory.provision`, and `request-kind=5950`; content includes `schema=soulfactory-provisioning/v1`, `method=soulfactory.provision`, identity fields, draft refs, `spec_hash`, `brief`, and `requested_at`.
- `6950` progress and terminal `7950` result remain correlated to the operator request.
- Final `31951` is the durable Soul read model; runtime truth comes from `38386` and the projected `31951`, not from an HTTP response.

Generated OpenClaw workspace config must use operator-supplied relays, controller pubkeys, model, and secret/config references. It must not embed placeholder relay URLs, controller keys, inline private keys, or fake MCP URLs.

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
