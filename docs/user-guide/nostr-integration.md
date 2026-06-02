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

Everything in Bahia is an **event**:

```json
{
  "id": "abc123...",
  "pubkey": "npub1...",
  "created_at": 1704067200,
  "kind": 5961,
  "content": "{\"service_id\":\"svc-123\"}",
  "tags": [
    ["service", "svc-123"],
    ["t", "bahia"]
  ],
  "sig": "signature..."
}
```

### Event Categories

| Category | Kind Range | Purpose |
|----------|------------|---------|
| **Requests** | 5961-5989 | Operator commands |
| **Status** | 6961-6984 | Progress updates |
| **Results** | 7961-7979 | Terminal outcomes |
| **Read Models** | 31961-31999 | Current state |
| **AI/ML** | 38390-38399 | ML commands/results |
| **Backup** | 38400-38419 | Backup operations |

### Read Models

**Read models** are replaceable events reflecting current state:

```json
{
  "kind": 31961,
  "content": {
    "service_id": "svc-123",
    "desired_artifact": "art-789",
    "observed_artifact": "art-789",
    "status": "healthy"
  },
  "tags": [
    ["d", "svc-123:env-456"]
  ]
}
```

For `(kind, pubkey, d-tag)`, the **latest event wins**.

## Relay Topology

### Sidecar

The **relay sidecar** is the primary control plane:

```yaml
nostr:
  sidecar:
    public_url: "wss://sidecar.example.com"
    backend_url: "wss://sidecar-backend.example.com"
```

Browser and API traffic goes through the sidecar.

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

Kind 31974 contains capability bootstrap:

```json
{
  "kind": 31974,
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
  "kinds": [31961],
  "authors": ["<bahia-service-pubkey>"],
  "#service": ["svc-123"]
}
```

### Workflow Pattern

1. **Subscribe** with scoped filter
2. **Process** stored events
3. **Wait for EOSE** (End of Stored Events)
4. **Keep open** for realtime updates

### Command Lifecycle Pattern

Signer-first commands use request, status, and result events as an event stream:

1. Sign the request locally and record its event id.
2. Subscribe for terminal result events scoped by `#e=<request_event_id>` before publishing when possible.
3. Publish and require at least one relay `OK` with `accepted=true`.
4. Treat status events as progress only; they never mean completion.
5. Complete only from the terminal result kind correlated to the request and requester.
6. Surface `AUTH`, `CLOSED`, zero-accepted publish, explicit abort, and configured timeout outcomes as distinct failures or degraded waits.

### Example: Deployment Follow

```javascript
// Subscribe to deployment events
const filter = {
  kinds: [6961, 7961],
  "#e": [requestEventId]
};

relay.subscribe(filter, {
  onevent: (event) => {
    if (event.kind === 6961) {
      // Progress update
      console.log("Status:", event.content);
    } else if (event.kind === 7961) {
      // Terminal result
      console.log("Result:", event.content);
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
  kind: 5961,
  content: JSON.stringify({
    service_id: "svc-123",
    environment_id: "env-456",
    artifact_id: "art-789"
  }),
  tags: [
    ["service", "svc-123"],
    ["environment", "env-456"],
    ["artifact", "art-789"]
  ],
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

Sensitive operations use NIP-44 encryption:

### Request (5980)

```json
{
  "kind": 5980,
  "content": "<NIP-44 encrypted>",
  "tags": [
    ["p", "<bahia-service-pubkey>"],
    ["encrypted", "bahia-encrypted-v1"]
  ]
}
```

### Result (7980)

```json
{
  "kind": 7980,
  "content": "<NIP-44 encrypted to requester>",
  "tags": [
    ["e", "<request-event-id>", "", "reply"],
    ["p", "<requester-pubkey>"],
    ["status", "succeeded"]
  ]
}
```

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
| `http_native` | HTTP is the expected protocol for that surface, such as Blossom/SBOM/continuity discovery-style interactions. |

Signer-first route files must not import REST API clients for command submission. The unit guard `web/tests/unit/route-transport-matrix.test.js` fails when a pure `nostr_native` or `nostr_request_result_facade` route adds `$lib/api/*` route imports without an explicit non-signer-first matrix classification.

Current exceptions are matrix-classified: artifact Blossom/SBOM HTTP surfaces, continuity HTTP status/simulation, and ML REST-to-Nostr compatibility ingress.

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

For REST endpoints:

```javascript
const authEvent = {
  kind: 27235,
  content: "",
  tags: [
    ["u", "https://bahia.example.com/api/v1/services"],
    ["method", "POST"]
  ]
};
const signed = await window.nostr.signEvent(authEvent);
const header = "Nostr " + btoa(JSON.stringify(signed));
```

## Authorization

### Pubkey Allowlists

Operations check pubkey authorization:

| Allowlist | Purpose |
|-----------|---------|
| `nostr.authorized_pubkeys` | General operator access |
| `adoption.allowed_pubkeys` | Runtime adoption |
| `direct_runtime_actions.allowed_pubkeys` | Deploy/restart/stop |

### Event Pubkey

Authorization is based on `event.pubkey`:

```json
{
  "pubkey": "abc123...",  // ← This is checked
  "kind": 5961,
  ...
}
```

## Event Kinds Reference

### Service Operations (596x)

| Kind | Name |
|------|------|
| 5961 | DeployRequest |
| 5962 | RollbackRequest |
| 5963 | ServiceAction |
| 5964 | ServiceCreate |
| 5965 | EnvironmentCreate |
| 5966 | DeploymentApproval |

### Status (696x)

| Kind | Name |
|------|------|
| 6961 | DeploymentStatus |
| 6962 | ServiceStatus |
| 6963 | ActionStatus |

### Results (796x)

| Kind | Name |
|------|------|
| 7961 | DeploymentResult |
| 7962 | ActionResult |
| 7963 | ServiceCreateResult |

### Read Models (3196x)

| Kind | d-tag pattern |
|------|--------------|
| 31961 | `service_id:environment_id` |
| 31962 | `service_id` |
| 31963 | `environment_id` |
| 31964 | `route_id` |
| 31965 | `route_id:environment_id` |

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
