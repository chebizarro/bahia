# Nostr Integration

Bahia is **Nostr-native** — Nostr events are the primary control plane, not just an audit log.

## Overview

Nostr provides:
- **Real-time updates** via subscriptions
- **Cryptographic identity** via keypairs
- **Decentralized state** via relays
- **Audit trail** via signed events
- **Offline resilience** via local caching

## Key Concepts

### Events

Bahia clients now separate private mutation intent from public observable truth. Mutations are ContextVM JSON-RPC requests carried as kind `25910` messages, normally encrypted with ContextVM CEP-4/NIP-59 gift-wrap (`1059` or `21059`). Public reads subscribe to canonical observable/state kinds such as `30900`, `4903`, `30315`, `11316`-`11320`, `30002`, and `30078`.

```json
{
  "kind": 25910,
  "tags": [["p", "<bahia-service-pubkey>"], ["method", "service/deploy"]],
  "content": "{\"jsonrpc\":\"2.0\",\"id\":\"req-123\",\"method\":\"service/deploy\",\"params\":{\"service_id\":\"svc-123\",\"_meta\":{\"progressToken\":\"req-123\"}}}"
}
```

### Event Categories

| Category | Kind(s) | Purpose |
|----------|---------|---------|
| **ContextVM intents** | `25910`, `1059`, `21059` | JSON-RPC mutation requests, responses, and encrypted transport |
| **ContextVM discovery** | `11316`-`11320` | Server, tools, resources, templates, and prompts announcements |
| **Canonical state** | `30900`, `30078` | Control-plane state projections and app-specific data |
| **Canonical audit/status** | `4903`, `30315` | Immutable audit facts and NIP-38 operational statuses |
| **Collections/relays** | `30002` | NIP-51 relay sets and topology |
| **Migration fixtures** | `5961`-`6006`, `6961`-`6997`, `7961`-`7997`, `31961`-`32003`, `38390`-`38431`, `5980`, `7980` | Legacy custom kinds retained only for startup migration, historical conversion, and fail-closed fixtures; they are not production runtime subscriptions |

## Migrating Existing Deployments to the New Kinds

Bahia includes a startup migration app in `internal/nostrmigration` that converts historical Bahia custom events into the canonical ContextVM/canonical observable contract. Operators should run the migrated app with the migration app enabled rather than keeping legacy subscribers in the runtime.

What it converts:

| Legacy input | Canonical output |
|--------------|------------------|
| Legacy CRU/request kinds (`5961`-`6006`, `38390`-`38431`, `5102`) | ContextVM `25910` methods, or NIP-09 `5` where the operation is deletion |
| Legacy status/progress kinds (`6961`-`6997`) | `30315`, `4903`, correlated ContextVM responses, or domain observables |
| Legacy terminal result kinds (`7961`-`7997`) | ContextVM responses plus `30900` / `4903` / `30315` observables |
| Legacy read-model/discovery kinds (`31961`-`32003`, `31974`) | `30900`, `30078`, `11316`-`11320`, or `30002` depending on semantics |
| Legacy audit/activity kinds (`31000`-`31024`, `31310`-`31311`) | `4903` |
| Legacy encrypted request/result (`5980`, `7980`) | CEP-4 / NIP-59 `1059` or `21059` around ContextVM `25910` |

How it runs:

1. On startup, scan the local Nostr event repository for `LegacyKinds()`.
2. Optionally backfill legacy events from configured relays and wait for `EOSE`.
3. Resolve each legacy event to a canonical disposition.
4. Skip if the target canonical event already exists with `migrated-from=<legacy_event_id>`.
5. Build a canonical event tagged with `migration=bahia-nostr-native-v1`, `legacy-kind`, `migrated-from`, `schema`, `domain`, and layer metadata.
6. Sign with the Bahia service private key.
7. Publish to relays, accepting both fresh relay `OK` and duplicate relay `OK` outcomes as success.
8. Store the canonical event locally and log the migration summary.

The migration is idempotent and safe to run every startup. Non-dry-run migration requires a configured Nostr publisher and Bahia service private key. If relay backfill does not reach `EOSE`, or if publish/signing fails, fix relay/signing configuration and rerun startup; do not re-enable legacy production subscribers. Relay sidecar behavior is intentionally conservative: migration publishes canonical replacement/status/audit outputs through configured allowlisted relays, while historical inputs remain tagged with `migrated-from` and `legacy-kind` for traceability rather than being treated as live protocol.

### Read Models

**Read models** are replaceable events reflecting current state. New client code should read canonical state from kind `30900` or NIP-78 kind `30078`, with domain and schema tags identifying the projection:

```json
{
  "kind": 30900,
  "content": {
    "service_id": "svc-123",
    "environment_id": "env-456",
    "desired_artifact": "art-789",
    "observed_artifact": "art-789",
    "status": "healthy"
  },
  "tags": [
    ["d", "service:svc-123:env-456"],
    ["domain", "service"],
    ["schema", "bahia.service-state.v1"]
  ]
}
```

For `(kind, pubkey, d-tag)`, the **latest event wins**.

Worker resource-pressure and cleanup projections use the same canonical state layer:

| Schema | Domain | Purpose |
|--------|--------|---------|
| `bahia.state.worker.v1` | `worker` | Worker scheduling, telemetry, pressure, and capacity-class state. |
| `bahia.state.worker-cleanup.v1` | `worker` | Cleanup execution lifecycle for Fleet Health and worker remediation history. |

A cleanup execution projection is kind `30900` with tags such as `schema=bahia.state.worker-cleanup.v1`, `worker=<worker_pubkey>`, `status=<requested|dispatched|running|completed|failed>`, and `cleanup_mode=<reclaimable_only|aggressive>`. Cleanup mutation intent remains encrypted ContextVM `worker/cleanup`; public cleanup progress is represented by this state projection.

## Relay Topology

### Sidecar

The **relay sidecar** is the primary control plane:

```yaml
nostr:
  sidecar:
    public_url: "wss://sidecar.example.com"
    backend_url: "wss://sidecar-backend.example.com"
```

Browser and API traffic goes through the sidecar. Operators must configure the sidecar relay allowlists so browsers can reach only the advertised `nostr.browser_relays` / `nostr.sidecar_url` set and backend migration/runtime publishers can reach only configured backend/upstream relays. Legacy-kind backfill may read from migration-configured relays, but live runtime publication remains canonical-only.

### Upstream Relays

Additional relays for interop and audit:

```yaml
nostr:
  relays:
    - "wss://relay.damus.io"
    - "wss://nos.lol"
```

### Encrypted Request Transport

Sensitive operations use the same Bahia browser relay set, with encrypted capability gated by discovery metadata:

```yaml
nostr:
  browser_relays:
    - "wss://encrypted.relay.example.com"
```

## Discovery

### Well-Known Endpoint

Bahia publishes discovery metadata:

```bash
curl https://bahia.example.com/.well-known/nostr.json
```

Returns:
- Service pubkey
- Relay URLs
- Feature flags
- Control plane kind maps

### Discovery Event

ContextVM kind `11316` is the canonical capability bootstrap. New clients use `11316` plus ContextVM capability announcements (`11317`-`11320`) and NIP-51 relay sets (`30002`). Legacy kind `31974` discovery may appear only as startup migration input or a compatibility fixture.

```json
{
  "kind": 11316,
  "content": {
    "nostr": {
      "browser_relays": ["wss://..."],
      "sidecar_url": "wss://..."
    },
    "features": {
      "relay_read_models": true,
      "encrypted_nostr_requests": true
    }
  }
}
```

## Subscribing to Events

### Basic Subscription

```json
{
  "kinds": [30900, 30315, 4903],
  "authors": ["<bahia-service-pubkey>"],
  "#domain": ["service"],
  "#service": ["svc-123"]
}
```

### Workflow Pattern

1. **Subscribe** with scoped filter
2. **Process** stored events
3. **Wait for EOSE** (End of Stored Events)
4. **Keep open** for realtime updates

### Command Lifecycle Pattern

ContextVM commands use JSON-RPC request/response for private intent acknowledgment, then public canonical events for observable progress and state:

1. Build a JSON-RPC 2.0 request with a Bahia method such as `service/deploy`, `worker/cordon`, `package/promote`, `dns/zone-create`, or `backup/run`.
2. Encrypt and route it through ContextVM (`25910` inside CEP-4/NIP-59 `1059` or `21059` where supported), tagged to the Bahia service pubkey.
3. Subscribe for the correlated ContextVM response and for observable state/audit/status kinds (`30900`, `4903`, `30315`, plus domain-specific standard NIPs) before publishing when possible.
4. Publish and require at least one relay `OK` with `accepted=true`.
5. Treat JSON-RPC response status as command acknowledgment only; long-running truth is the canonical observable event stream.
6. Surface `AUTH`, `CLOSED`, zero-accepted publish, explicit abort, and configured timeout outcomes as distinct failures or degraded waits.

### Assistant documentation context

The floating assistant keeps the same encrypted ContextVM operation for prompts: `assistant/prompt` carried as kind `25910` and normally wrapped with CEP-4/NIP-59 where supported. Documentation context does not introduce a relay polling loop, synthetic request/response route, or new Nostr completion signal.

When a page has route documentation metadata, the assistant composer shows a dismissible selected reference such as:

```json
{
  "route_context": { "route": "/services", "params": {} },
  "selected_refs": ["docs:features-services"]
}
```

The backend resolves `docs:<topic>` and `bahia://docs/<topic>` references from the central `docs/user-guide` catalog into a bounded `Documentation References` section before calling the assistant model. Missing docs refs are reported in context as unresolved references; they do not convert the prompt into a failed control-plane mutation. If the assistant later performs or follows an operational command, durable progress and terminal truth still come from the canonical observable event stream described above.

### Example: Deployment Follow

```javascript
// Subscribe to deployment events
const filter = {
  kinds: [30315, 4903, 30900],
  "#e": [requestEventId]
};

relay.subscribe(filter, {
  onevent: (event) => {
    if (event.kind === 30315) {
      // NIP-38 operational status
      console.log("Status:", event.content);
    } else if (event.kind === 4903) {
      // Immutable audit fact
      console.log("Audit:", event.content);
    } else if (event.kind === 30900) {
      // Canonical state projection
      console.log("State:", event.content);
    }
  },
  oneose: () => {
    console.log("Historical events loaded");
  }
});
```

## Publishing Events

### Signer-First Operations

Critical operations require signed events:

```javascript
const event = {
  kind: 25910,
  content: JSON.stringify({
    jsonrpc: "2.0",
    id: "deploy-svc-123-prod",
    method: "service/deploy",
    params: {
      service_id: "svc-123",
      environment_id: "env-456",
      artifact_id: "art-789",
      _meta: { progressToken: "deploy-svc-123-prod" }
    }
  }),
  tags: [["p", "<bahia-service-pubkey>"], ["method", "service/deploy"]],
  created_at: Math.floor(Date.now() / 1000)
};

// Sign with NIP-07
const signedEvent = await window.nostr.signEvent(event);

// Publish to relay
const ok = await relay.publish(signedEvent);
```

### Handling OK Response

```javascript
relay.publish(event).then(ok => {
  if (ok.accepted) {
    console.log("Published:", ok.eventId);
  } else {
    console.error("Rejected:", ok.message);
  }
});
```

## Encrypted Events

Sensitive operations use ContextVM JSON-RPC kind `25910`, normally wrapped with CEP-4/NIP-59 random-key gift-wrap (`1059` or `21059`). Legacy encrypted Bahia kinds `5980`/`7980` are startup migration/test fixtures only.

### Wrapped ContextVM request

```json
{
  "kind": 1059,
  "content": "<gift-wrapped kind 25910 ContextVM JSON-RPC request>",
  "tags": [
    ["p", "<bahia-service-pubkey>"],
    ["contextvm", "contextvm-jsonrpc-v1"]
  ]
}
```

### Wrapped ContextVM response

```json
{
  "kind": 1059,
  "content": "<gift-wrapped kind 25910 ContextVM JSON-RPC response>",
  "tags": [
    ["p", "<requester-pubkey>"],
    ["contextvm", "contextvm-jsonrpc-v1"]
  ]
}
```

The unwrapped inner response carries `e=<request-event-id>` with reply marker and `p=<requester-pubkey>`. Long-running completion is observed through canonical kinds `30900`, `4903`, `30315`, domain NIPs, and NIP-09 kind `5` deletions where applicable.

### Encrypted Operations

- Notifications (channels, logs)
- Organizations (members, invites)
- Payments (history)
- Secrets (create, reveal)
- Run logs
- Artifact verification

## Route Transport Classes

Bahia route/control-surface transport classification is tracked in PSTF at `pstf/features/BAHIA_NOSTR_AUDIT_PARITY/route_transport_matrix.json`. The matrix is the durable source for distinguishing signer-first Nostr surfaces from compatibility ingress.

| Class | Meaning |
|-------|---------|
| `nostr_native` | Route/control surface uses Nostr read models, scoped subscriptions, and signed command events where applicable. |
| `nostr_request_result_facade` | Route uses signed/encrypted Nostr transport, but wraps domain operations as correlated request/result operations rather than durable domain event semantics. |
| `rest_to_nostr_bridge` | Browser/API HTTP ingress submits work that the backend publishes as Nostr commands. Treat this as compatibility ingress, not signer-first browser control. |
| `rest_compatibility` | Legacy or compatibility REST endpoint remains mounted for clients or domain reads/writes. |
| `http_native` | HTTP is the expected protocol for that surface, such as Blossom/SBOM discovery-style interactions. |

Signer-first route files must not import REST API clients for command submission. The unit guard `web/tests/unit/route-transport-matrix.test.js` fails when a pure `nostr_native` or `nostr_request_result_facade` route adds `$lib/api/*` route imports without an explicit non-signer-first matrix classification.

Current route-level REST client exceptions are matrix-classified artifact Blossom/SBOM HTTP surfaces only. ML import/deploy and continuity status/topology/simulation now use Nostr-backed browser paths.

## Authentication

### NIP-07 (Browser Extension)

```javascript
const pubkey = await window.nostr.getPublicKey();
const signed = await window.nostr.signEvent(event);
```

### NIP-46 (Remote Signer)

```javascript
const bunker = new BunkerClient("bunker://...");
await bunker.connect();
const signed = await bunker.signEvent(event);
```

### NIP-98 (HTTP Auth)

For REST endpoints that remain HTTP-compatible, such as read-model queries:

```javascript
const authEvent = {
  kind: 27235,
  content: "",
  tags: [
    ["u", "https://bahia.example.com/api/v1/services"],
    ["method", "GET"]
  ]
};
const signed = await window.nostr.signEvent(authEvent);
const header = "Nostr " + btoa(JSON.stringify(signed));
```

Service and environment mutations use ContextVM JSON-RPC over Nostr directly, not NIP-98-authenticated REST writes.

## Authorization

### Pubkey Allowlists

Operations check pubkey authorization:

| Allowlist | Purpose |
|-----------|---------|
| `nostr.authorized_pubkeys` | General operator access |
| `adoption.allowed_pubkeys` | Runtime adoption |
| `direct_runtime_actions.allowed_pubkeys` | Deploy/restart/stop |

### Event Pubkey

Authorization is based on the verified inner ContextVM event `pubkey` after unwrap:

```json
{
  "pubkey": "abc123...",  // ← This is checked
  "kind": 25910,
  "tags": [["method", "service/deploy"]],
  ...
}
```

## Event Kinds Reference

### Production ContextVM and canonical kinds

| Kind | Purpose |
|------|---------|
| 25910 | ContextVM JSON-RPC request/response inner message |
| 1059 / 21059 | CEP-4/NIP-59 gift-wrap envelopes for ContextVM messages |
| 11316-11320 | ContextVM discovery and capability announcements |
| 30002 | NIP-51 relay sets |
| 30900 | Canonical control-plane state projection |
| 4903 | Canonical audit fact |
| 30315 | NIP-38 operational status |
| 5 | NIP-09 delete event where Nostr deletion semantics apply |

### Legacy migration inventory

Legacy Bahia-specific request/status/result/read-model ranges (`5961`-`6006`, `6961`-`6997`, `7961`-`7997`, `31961`-`32003`, `38390`-`38431`, `5980`, `7980`) are retained only for startup migration, historical conversion, and fail-closed fixtures. Production runtime code should not subscribe to or publish them as live control-plane behavior.

## Best Practices

1. **Use scoped filters** — Don't over-subscribe
2. **Handle EOSE** — Know when historical is done
3. **Check OK responses** — Verify publish success
4. **Deduplicate events** — By event ID
5. **Handle reconnects** — Re-subscribe after disconnect
6. **Verify signatures** — Don't trust unsigned events

## Troubleshooting

### Not Receiving Events

- Check relay connectivity
- Verify filter matches
- Check pubkey authorization

### Publish Rejected

- Check event signature
- Verify pubkey is authorized
- Check relay rate limits

### Encryption Failed

- Verify NIP-44 support in signer
- Check correct recipient pubkey
- Verify relay accepts encrypted events

## Related

- [Core Concepts](core-concepts.md) — Data model
- [MCP Tools](mcp-tools.md) — Tool invocation
- [Control Planes](../control-planes.md) — Full specification
