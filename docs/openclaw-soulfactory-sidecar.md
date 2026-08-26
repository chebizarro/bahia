# OpenClaw SoulFactory Sidecar

Bahia owns the OpenClaw adapter as a separate sidecar. It does not require upstream OpenClaw changes or REST lifecycle APIs. Provisioned OpenClaw souls run in containers; the sidecar/wrapper must not launch a persistent bare-metal gateway or agent.

Operators publish signed `31952` drafts and correlated `5950` requests. SoulFactory sends runtime control over Nostr and publishes progress/read models. REST provisioning/lifecycle routes are a non-goal.

## Nostr contract

The sidecar:

- signs kind-`30317` capability announcements with `runtime=openclaw`;
- subscribes to addressed kind-`38384` from configured trusted controller pubkeys;
- validates ID/signature, schema, tags/content agreement, addressing, controller trust, method, params, freshness, idempotency, and correlation before local effects;
- publishes signed correlated kind-`38386` with request/controller/runtime context;
- treats EOSE only as the stored/live boundary;
- reconnects subscriptions with backoff and reissues after NIP-42 auth;
- requires relay `OK` for publications;
- persists request/result fingerprints so exact replay after restart returns cached `38386` without repeating effects.

The browser/server request shape remains:

- `31952`: desired spec and `spec_hash`;
- `5950`: draft/spec/runtime/capability correlation and `method=soulfactory.provision`;
- `6950`/`7950`: progress and terminal result;
- `31951`: authoritative soul read model;
- `38384`/`38386`: runtime truth.

Generated workspace config uses operator-supplied relays, controllers, model, and secret references. It must not embed synthetic relays, inline keys, fake MCP URLs, or Signet bunker URIs. Agent bunker secrets are private handoff data and are removed from public SoulFactory artifacts before the sidecar sees the durable checkpoint.

## Local control surface

The sidecar command receives one `OpenClawControlInvocation` JSON document on stdin and returns one `OpenClawControlOutcome` on stdout.

`OpenClawCommandDriver` defaults to the packaged wrapper's exact set:

```text
soulfactory.provision
soulfactory.update
soulfactory.persona.update
soulfactory.revoke
```

Other methods are rejected unless the operator explicitly configures a driver that implements and advertises them.

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

The wrapper supports dry-run verification and dedicated non-dry-run gateways through `OPENCLAW_SOULFACTORY_RUNTIME_MODE=per-agent-compose`. Shared `existing-container` provisioning is rejected. It implements optimistic spec-hash checks for `soulfactory.update`.

The command receives `SOULFACTORY_METHOD`, `SOULFACTORY_AGENT_ID`, and `SOULFACTORY_SPEC_HASH`.

## Key and store configuration

Use `-private-key-file`/`OPENCLAW_SOULFACTORY_PRIVATE_KEY_FILE` for a mounted file containing the sidecar nsec or hex key. `OPENCLAW_SOULFACTORY_PRIVATE_KEY` is rejected without reading or logging its value.

The default idempotency store is under the user cache directory. Production services should supply a persistent `-idempotency-store` path under mounted `/data`.

Controller authorization is persisted in `-controller-policy-file`/`OPENCLAW_SOULFACTORY_CONTROLLER_POLICY_FILE` (by default, `openclaw-soulfactory-controller-policy.json` beside the idempotency store). `SOULFACTORY_CONTROLLER_PUBKEYS` and `-trusted-controller-pubkeys` seed the file only when it is absent and never override it afterward. The sidecar directly subscribes to signed NIP-51 kind-`30000` lists at `d=service:openclaw-soulfactory-sidecar:controllers`. A currently trusted author must sign the list; `service=openclaw-soulfactory-sidecar`, scope, positive monotonic version, `schema=cascadia.config.membership.v1`, content/tag equality, signature, and every `p` item are validated. The complete set and per-author accepted version are persisted before hot application and capability republication. Invalid, stale, replayed, and untrusted lists leave the prior policy active.

A trusted controller may also grant or revoke another controller with signed ContextVM kind-`25910` methods `soulfactory.controller.grant` and `soulfactory.controller.revoke`; the sidecar persists the event id, timestamp, and complete normalized set before activating it, republishes capability state, and returns a correlated signed `25910` response. Send SIGHUP to re-read an operator-edited persisted file and republish capability state.

## Example

```bash
openclaw-soulfactory-sidecar \
  -relays wss://relay.example \
  -private-key-file /etc/bahia/soulfactory/sidecar.key \
  -controller-policy-file /data/openclaw-soulfactory-controller-policy.json \
  -trusted-controller-pubkeys "$SOULFACTORY_CONTROLLER_PUBKEYS" \
  -control-relays wss://relay.example \
  -idempotency-store /var/lib/bahia/openclaw-soulfactory-sidecar-idempotency.json \
  -command /usr/local/bin/openclaw-soulfactory-control \
  -methods soulfactory.provision,soulfactory.update,soulfactory.persona.update,soulfactory.revoke
```

See [the control wrapper](openclaw-soulfactory-control-wrapper.md), [runtime contract](soulfactory-runtime-control.md), and [deployment runbook](soul-factory-sidecar-runbook.md).
