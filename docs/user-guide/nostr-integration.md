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

Maintenance commands are always carried as standards-conformant NIP-59 kind `1059`, and workers return the immediate ContextVM response the same way. Absolute host filesystem paths are visible only after an authorized recipient unwraps the rumor; public audit/status events retain opaque correlation without path details.

```json
{
  "kind": 25910,
  "tags": [["p", "<bahia-service-pubkey>"], ["method", "service/deploy"]],
  "content": "{\"jsonrpc\":\"2.0\",\"id\":\"req-123\",\"method\":\"service/deploy\",\"params\":{\"service_id\":\"svc-123\",\"_meta\":{\"progressToken\":\"req-123\"}}}"
}
```

Complete-set deployment-unit changes use `environment/update` with both `deployment_units` and `expected_updated_at` from the latest environment read. Stale revisions return ContextVM code `-32009` without mutating database or canonical registry state; clients reread, remerge, and sign a new bounded retry rather than sending an unguarded replacement.

### Event Categories

| Category | Kind(s) | Purpose |
|----------|---------|---------|
| **ContextVM intents** | `25910`, `1059`, `21059` | JSON-RPC mutation requests, responses, and encrypted transport |
| **ContextVM discovery** | `11316`-`11320` | Server, tools, resources, templates, and prompts announcements |
| **Canonical state** | `30900`, `30078` | Control-plane state projections and app-specific data |
| **Canonical audit/status** | `4903`, `30315` | Immutable audit facts and NIP-38 operational statuses |
| **Collections/relays** | `30002`, `30004` | NIP-51 relay sets, topology, and SBOM availability lists |
| **SoulFactory interop** | `31950`, `31951`, `31952`, `31953`, `5950`, `6950`, `7950`, `1950`, `1951`, `30317`, `38384`, `38386` | Direct Nostr agent lifecycle events accepted by the Bahia sidecar as open interop data |
| **Migration fixtures** | `5961`-`6006`, `6961`-`6997`, `7961`-`7997`, `31961`-`32003`, `38390`-`38431`, `5980`, `7980` excluding documented SoulFactory interop overlaps | Legacy custom kinds retained only for startup migration, historical conversion tests, and fail-closed fixtures |

### SoulFactory/OpenClaw provisioning

SoulFactory is a domain-specific Nostr event flow rather than a REST lifecycle API. New mutation clients publish ContextVM `25910` requests using `soul-factory/provision` or `soul-factory/action`; Bahia retains the original request event id as the lifecycle correlation id.

1. Trusted operators may publish a signed `31953` fleet OpenClaw template; the reactor pins the latest trusted snapshot for subsequent provisions.
2. Operators publish signed `31952` Soul drafts.
3. Operators publish signed `25910` `soul-factory/provision` requests whose params contain the existing provisioning schema. Bahia validates them with the SoulFactory parser and adapts them into the staged reactor without changing their correlation identity. Existing signed `5950` requests remain lifecycle interop during contraction.
4. Bahia publishes correlated `6950` progress, sends scoped `38384` runtime-control events to trusted OpenClaw runtimes, validates `38386` results, publishes the final `31951` Soul read model, and publishes terminal `7950`.
5. Clients subscribe to scoped `6950`, `7950`, and `31951` events for durable progress/completion. Relay `OK` verifies event acceptance, not runtime completion.

REST routes for SoulFactory provisioning or lifecycle operations are a non-goal. Browser, CLI, MCP, and agent integrations must use signed ContextVM requests and scoped Nostr subscriptions instead of HTTP create/provision/suspend/resume calls. A ContextVM acknowledgment is not terminal completion; provisioning clients follow `30900` state at `soul-factory:provisioning:<request-event-id>` and matching `4903` audit facts, with existing correlated lifecycle events retained during contraction.

## Migrating Existing Deployments to the New Kinds

Bahia includes a startup migration app in `internal/nostrmigration` that converts historical Bahia custom events into the canonical ContextVM/canonical observable contract. Operators should run the migrated app with the migration app enabled rather than keeping legacy subscribers in the runtime.

What it converts:

| Legacy input | Canonical output |
|--------------|------------------|
| Legacy CRU/request kinds (`5961`-`6006`, `38390`-`38431`, `5102`) excluding SoulFactory interop `5950`, `1950`, `38384` | ContextVM `25910` methods, or NIP-09 `5` where the operation is deletion |
| Legacy status/progress kinds (`6961`-`6997`) excluding SoulFactory interop `6950` | `30315`, `4903`, correlated ContextVM responses, or domain observables |
| Legacy terminal result kinds (`7961`-`7997`) excluding SoulFactory interop `7950`, `1951`, `38386` | ContextVM responses plus `30900` / `4903` / `30315` observables |
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

Desired-state runtime deploys add metadata to existing ContextVM responses and canonical observables instead of adding new kinds. Compose/Docker status events may include the steps `building_desired_state`, `locking_environment`, `rendering`, `applying`, `observing`, and `projecting`; result/state projections may include `desired_hash`, `renderer`, `target`, revision/apply summaries, and `observation_id`. These fields are optional and backward-compatible. Public relay content is sanitized: secret values, generated Compose env-file contents, raw Docker endpoint URLs, Docker TLS material, bearer credentials, and NIP-98 credentials are not projected.

Worker resource-pressure and cleanup projections use the same canonical state layer:

| Schema | Domain | Purpose |
|--------|--------|---------|
| `bahia.state.worker.v1` | `worker` | Worker scheduling, telemetry, pressure, and capacity-class state. |
| `bahia.state.worker-cleanup.v1` | `worker` | Cleanup execution lifecycle for Fleet Health and worker remediation history. |

A cleanup execution projection is kind `30900` with tags such as `schema=bahia.state.worker-cleanup.v1`, `worker=<worker_pubkey>`, `status=<requested|dispatched|running|completed|failed>`, and `cleanup_mode=<reclaimable_only|aggressive>`. Cleanup mutation intent remains encrypted ContextVM `worker/cleanup`; public cleanup progress is represented by this state projection.

## Relay Topology

### Sidecar

The **relay sidecar** is the primary control-plane relay surface. It is a standalone Khatru Nostr relay HTTP/WebSocket server, not a REST CRUD endpoint: `cmd/relay/main.go` starts it, `internal/relaysidecar/server.go` serves NIP-11 relay metadata over HTTP and Nostr WebSocket traffic on `/` plus the configured `nostr.sidecar.public_url` path such as `/relay`, and operators expose it with `nostr.sidecar.listen_addr` plus any web proxy mapping. The mounted YAML is authoritative for mutable sidecar policy. Legacy sidecar, ContextVM-relay, and reconcile environment values only seed missing keys once; send `SIGHUP` to apply a validated file change without recreating the container.

```yaml
nostr:
  sidecar:
    enabled: true
    listen_addr: "0.0.0.0:3334"
    public_url: "wss://sidecar.example.com/relay"
    backend_url: "ws://relay:3334/relay"
```

Browser and API Nostr relay traffic goes through the sidecar. The sidecar accepts every valid signed Nostr event kind and every ordinary NIP-01 subscription filter; it does not use event-kind, author, recipient, or filter-scope allowlists. The advertised `nostr.browser_relays` / `nostr.sidecar_url` and configured backend/upstream relay sets select routing destinations, not which events those relays accept. Consumers remain responsible for authorizing and interpreting events.

### Relay Policy Sources

Backend configuration separates relay policy by purpose even when a deployment intentionally reuses the same physical relay URL:

```yaml
nostr:
  # Backward-compatible alias for backend/service relays. Do not treat this as browser policy.
  relays:
    - "wss://service-relay.example.com"
  # Canonical backend service publish/backfill source. If absent, nostr.relays is used as a compatibility alias.
  service_relays:
    - "wss://service-relay.example.com"
  # Browser-safe bootstrap/read relays.
  browser_relays:
    - "wss://sidecar.example.com"
  # Preferred ContextVM request/reply relays. If absent, clients may fall back to browser_relays with degraded metadata.
  contextvm_relays:
    - "wss://contextvm-relay.example.com"
  # Fixed behavior when a relay requires NIP-42 AUTH and no signer is available.
  relay_auth_unavailable: "exclude_and_fail"
```

`nostr.relay_auth_unavailable=exclude_and_fail` means auth-required relays without usable credentials are excluded from the current operation, the relay CLOSED/OK reason must remain visible in health/error metadata, and the operation fails deterministically if the remaining relays cannot satisfy its success rule. Bahia must not fall back to REST or a legacy mutation path after a relay accepts signed ContextVM traffic.

Relay strategy publications for `bahia-browser-v1`, `bahia-contextvm-v1`, `bahia-service-v1`, and the advisory service NIP-65 `10002` list are authored by the configured Bahia service key. Clients validate these events against their configured trusted service pubkey list. Bahia does not ship multi-key publication rotation in this relay-strategy slice; deployments that need automatic dual publishing or signer orchestration require a separate key-management design.

### Optional NIP-86 relay administration

NIP-86 relay administration is an optional relay-owner HTTP management surface for Bahia-owned or Bahia-authorized relays. Bahia exposes it through the Nostr-native operator relay settings control plane: the browser publishes a ContextVM kind `25910` intent such as `settings/relay-admin.call`, Bahia validates the configured target, and only then calls the relay's NIP-86 HTTP endpoint with NIP-98 payload-bound authorization. NIP-86 is never used as Bahia application mutation transport.

NIP-86 relay administration is disabled by default. Configure only Bahia-owned or explicitly Bahia-authorized relay targets; public relays and arbitrary HTTP endpoints are rejected before any NIP-86 request is sent.

```yaml
nostr:
  relay_administration:
    enabled: true
    # A secret reference resolved by deployment tooling; do not put private-key material in config.
    administrator_private_key_ref: "secret://relay-admin/sidecar"
    targets:
      - ref: "sidecar"
        relay_url: "wss://sidecar.example.com"
        # Optional when the HTTP endpoint differs from relay_url converted from wss/ws to https/http.
        http_url: "https://sidecar.example.com"
        authorization: "bahia_owned" # or "bahia_authorized"
        administrator_pubkeys:
          - "<64-hex-admin-pubkey-authorized-by-the-relay>"
```

When enabled, Bahia support code may call only NIP-86 relay-owner methods such as `supportedmethods`, `allowpubkey`, `banpubkey`, `allowkind`, `disallowkind`, `changerelayname`, `changerelaydescription`, and IP/event moderation methods. Relay administration URLs must use `wss://` and `https://` except for localhost/loopback development targets. Each HTTP request uses `Content-Type: application/nostr+json+rpc` and a NIP-98 `Authorization: Nostr <event>` header. The signed kind `27235` event includes `u=<relay_url>`, `method=POST`, and the required `payload=<sha256-of-exact-json-body>` tag. The client refuses disabled config, unknown target refs, administrator pubkeys not authorized for that target, non-NIP-86 method names, HTTP status failures, and relay JSON errors.

### Operator relay settings UI

The Settings page separates persistent operator relay policy from local browser-session relay overrides.

Use **Settings → Operator Relay Policy** to add or remove:

- browser/bootstrap relays published as Bahia relay topology,
- ContextVM request/reply relays,
- service publish/backfill relays,
- trusted NIP-66 monitor pubkeys,
- notification DM receive relays published as NIP-51 kind `10050`, and
- NIP-86 managed relay targets.

Saving this section publishes a ContextVM kind `25910` intent with method `settings/relay-policy.apply`. Bahia validates the payload, republishes the standard service-signed NIP-51 relay events (`30002` for browser/ContextVM/service and `10050` for explicit notification DM relays), and publishes a canonical relay settings state event (`30900`, `d=relay-settings:operator`, `domain=relay-settings`, `schema=bahia.relay-settings.v1`) plus an audit event (`4903`). Backend hydration subscribes to every eligible configured relay with service-author and `#d` / `#domain` / `#schema` filters, tracks EOSE per relay, performs a bounded post-EOSE drain, validates the NIP-01 ID/signature/timestamp/tags/payload, and atomically promotes only the deterministic newest valid event.

The last accepted canonical policy is stored as a PostgreSQL server projection with its canonical payload, deterministic hash, event ID, author, event/acceptance timestamps, source relay, and last successful sync. Restart, relay outage, AUTH failure, timeout, parse failure, or zero-event EOSE never converts that projection to an empty policy. Empty policy is valid only when the trusted service explicitly signs an empty canonical payload. The signer-first `settings/relay-policy.get` response exposes `canonical_policy`, `truth_state`, and `server_projection` (availability, provenance, hash, last-sync, and freshness). A missing or unreachable projection dependency reports `unavailable`; a successful read with no accepted head reports `never-configured`. Neither response infers config defaults or synthesizes an empty policy.

Settings renders canonical truth explicitly:

- **Loaded — live** for a validated service-signed relay event,
- **Loaded — cached** or **Loaded — cached/stale** for the durable last-known-good projection,
- **Unavailable** when canonical truth cannot be hydrated (the form is labeled as a noncanonical draft rather than blank truth),
- **Never configured** when the signer-first durable read confirms no accepted head, and
- **Intentionally empty — explicitly signed** when the accepted canonical event really contains an empty policy.

The truth panel displays safe event ID, policy hash, source relay, and last-sync provenance. Query strings, fragments, credentials, signer URLs, and key material are not displayed or written to browser storage.

The canonical relay event, PostgreSQL server projection, relay discovery cache, and browser emergency override are separate concepts. Background hydration does not mutate shared runtime config, and the browser override does not become persistent policy. If canonical state arrives while an operator has unsaved local edits in Settings, the page preserves those edits and shows explicit **Apply Service Policy** / **Keep Local Edits** actions instead of silently overwriting fields. Apply remains disabled while truth is loading or unavailable. Normal Apply requests carry the expected projection event ID and hash, so the service rejects stale or missing heads instead of accepting a blind replacement. After a definitive unavailable result, the operator may unlock Apply only by checking the audited-replacement confirmation and entering a non-secret change/incident reference. The signed ContextVM request carries that confirmation separately from canonical policy with the fixed reason code `relay_hydration_unavailable`. Bahia rejects false unavailable claims when it can read a head, and publishes a correlated service-signed `4903` replacement-confirmation fact before any canonical mutation; audit publication failure prevents the replacement. Clients still treat the ContextVM response as an acknowledgment; durable truth comes from the validated service-signed event and its server projection.

The **Browser Session Relays** section is labeled **LOCAL / NONCANONICAL**. Its emergency override persists only in that browser profile and never updates service relay sets, ContextVM relays, DM relay lists, NIP-66 monitor trust, or NIP-86 targets. Storage uses the versioned `bahia.browser-relay-override.v2` envelope; compatible legacy arrays and older object shapes migrate in place without losing safe relay values. URLs containing credentials, query parameters, or fragments are rejected from browser persistence.

### Advisory relay metadata and DM relay lists

NIP-11 relay metadata and optional NIP-66 monitor events are advisory inputs only. They may annotate relay health, capability warnings, or operator-facing ranking, but they do not establish Bahia service trust, override trusted service pubkeys, or authorize removal of all configured relays. Missing, malformed, or limiting NIP-11 metadata is surfaced as relay health metadata while the configured relay list remains authoritative.

NIP-66 relay monitor trust is configured explicitly and is empty by default. When configured, browser DNS relay health uses scoped subscriptions for kind `10166` monitor announcements and kind `30166` relay discovery events from those monitor pubkeys only; untrusted monitors and reports for relays outside the configured relay set are ignored.

```yaml
nostr:
  browser_relays:
    - "wss://browser-relay.example.com"
  trusted_relay_monitor_pubkeys:
    - "<64-hex-monitor-pubkey>"
```

NIP-66 monitor tags such as `R=auth`, `R=payment`, `R=writes`, `N=<nip>`, and `rtt-open` are advisory health/capability annotations. They can warn that a configured relay is limiting, but they cannot add relays, remove relays, or make a monitor pubkey a Bahia service identity.

NIP-51 kind `10050` DM relay lists are reserved for explicitly DM-enabled Bahia features and receiving identities. Public browser bootstrap through `bahia-browser-v1` does not imply DM receive readiness, ContextVM relays are not DM relays, and Bahia does not publish DM relay lists by default.

To publish a Bahia service DM receive list for notification DM flows, operators must explicitly enable notifications DM delivery and configure a DM relay list for the service identity:

```yaml
notifications:
  enabled: true
  nostr_dm: true
nostr:
  dm_relay_lists:
    - enabled: true
      feature: "notifications"
      identity: "service"
      relays:
        - "wss://dm-relay.example.com"
```

Only `feature: notifications` with `identity: service` is accepted in the current implementation. The relays above are normalized and published as a service-signed kind `10050` event with `relay` tags. They are not copied from `browser_relays`, `contextvm_relays`, or `service_relays`.

The Bahia service key publishes NIP-51 `30002` relay sets for canonical bootstrap and an advisory NIP-65 `10002` relay list for wider Nostr routing. The `10002` list advertises ContextVM request relays as `read` and service publish/backfill relays as `write`; clients must still use ContextVM discovery plus the NIP-51 `30002` relay sets for Bahia bootstrap.

### Repository Relay Hints

NIP-34 repository announcements (`30617`) may include a `relays` tag. Bahia preserves those tag values as repository `relayUrls` in browser selections. Repository announcement discovery itself uses the advertised `nostr.nip34_relays` policy when configured, instead of replacing the global Bahia singleton connection or querying only the browser sidecar.

When a user selects a NIP-34 repository, branch/state lookup for kind `30618` queries that repository's own `relays` tag values first with the scoped filter `{ kinds: [30618], authors: [repo_pubkey], "#d": [repo_identifier] }`.

If no NIP-34 relay policy or repository `relays` tag values are available, Bahia queries the globally connected Bahia read relays as a degraded fallback. Missing repository relay hints surface degraded metadata with reason `missing_repository_relays`. Incomplete `EOSE` remains visible through branch lookup degraded metadata, including relay summary and partial event count. The Bahia sidecar read/write policy accepts NIP-22 comments (`1111`) plus NIP-34 kinds `10317`, `1617`-`1619`, `1621`, `1630`-`1633`, and `30617`-`30618` as open interop data so a sidecar can be used as a NIP-34 relay when deployment policy advertises it.

### Encrypted Request Transport

Sensitive operations use the ContextVM relay policy where available and otherwise the Bahia browser relay set, with encrypted capability gated by discovery metadata:

```yaml
nostr:
  contextvm_relays:
    - "wss://contextvm-relay.example.com"
  browser_relays:
    - "wss://browser-relay.example.com"
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
3. Subscribe for the correlated ContextVM response and for observable state/audit/status kinds (`30900`, `4903`, `30315`, plus `30316` assistant transcript ciphertext for assistant flows and domain-specific standard NIPs) before publishing when possible.
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

When agentic assistant mode is enabled, the prompt still uses `assistant/prompt`. Clients subscribe to service-authored assistant status events (`30315`, `#domain=assistant`) and render the `phase` field for progress such as `tool_call_requested`, `tool_submitted`, `tool_observed`, `approval_required`, and `loop_completed`. Full turn transcript entries are service-authored kind `30316` events tagged with `schema=bahia.assistant-transcript.v1`; their production content is the service-held symmetric-key AEAD envelope described in the event spec, so clients without the key should render envelope metadata rather than pretending to decrypt it.

Action-level approvals are separate from the legacy plan-hash approval flow. When a status event has `phase=approval_required` and an `action_id`, the operator decision is published as `assistant/approval` with params:

```json
{
  "session_id": "assistant-session-id",
  "action_id": "deferred-action-id",
  "decision": "approve",
  "reason": "operator-visible reason"
}
```

A ContextVM acknowledgment for `assistant/approval` only confirms the decision was accepted for processing. Execution progress and terminal truth still come from scoped subscriptions to `30315`, `30316`, and any domain observables correlated to the downstream action.

### Example: Deployment Follow

Desired-state deploy followers should display optional `step`, `desired_hash`, `renderer`, `target`, and `observation_id` metadata when present, but should not require those fields to conclude whether an event is valid. The ContextVM response is an acknowledgment; completion and convergence still come from the subscribed observable stream.

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

The unwrapped inner response carries `e=<request-event-id>` with reply marker and `p=<requester-pubkey>`. Long-running completion is observed through canonical kinds `30900`, `4903`, `30315`, `30078`, `30004`, domain NIPs, and NIP-09 kind `5` deletions where applicable.

### Early encrypted ContextVM progress ack

When system discovery advertises `control_plane.capabilities` containing `encrypted_controlplane.progress_ack` and `control_plane.wire_version` equal to `contextvm-jsonrpc-v2`, browser clients use a short ack deadline before the longer work deadline. This discovery wire version describes JSON-RPC payload semantics; the Nostr routing tag value `contextvm-jsonrpc-v1` remains a compatibility discriminator and does not negotiate progress acknowledgements. After routing and requester authorization succeed, but before invoking the domain handler, Bahia publishes a gift-wrapped JSON-RPC notification back to the requester:

```json
{
  "jsonrpc": "2.0",
  "method": "notifications/progress",
  "params": {
    "requestId": "<published-request-event-id>",
    "status": "processing"
  }
}
```

The notification has no `id`, `result`, or `error`. It only confirms that a routed, authorized Bahia service accepted processing; terminal success or failure still comes from the correlated JSON-RPC response and durable canonical observables. Routing mismatches remain silent, and unauthorized requests continue to return the existing `-32001` terminal error without a progress ack.

### Encrypted Operations

- Notifications (channels, logs)
- Organizations (members, invites)
- Payments (history)
- Secrets (create, reveal)
- Run logs
- Artifact verification
- Security OSV/SBOM scans and finding reads

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

Current route-level REST client exceptions are matrix-classified artifact Blossom/SBOM HTTP surfaces only. Canonical SBOM relay truth uses `30078` reference app-data and `30004` NIP-51 availability lists; legacy `30079` SBOM indexes are read-only compatibility data. ML import/deploy and continuity status/topology/simulation now use Nostr-backed browser paths.

## SBOM Generation and Import Observables

SBOM mutations are ContextVM intents (`sbom/generate` and `sbom/import`) carried as kind `25910` requests, optionally wrapped in `1059` or `21059`. The JSON-RPC response is only an asynchronous acceptance acknowledgment and includes the idempotency/status coordinate:

```json
{
  "accepted": true,
  "status": "accepted",
  "run_id": "<idempotencyKey>",
  "status_d_tag": "sbom:run:<sanitized-idempotencyKey>",
  "idempotencyKey": "<idempotencyKey>",
  "observable_kinds": [30315, 4903, 30078, 30004]
}
```

Durable truth is observed through scoped subscriptions:

```json
{
  "kinds": [30315, 4903, 30078, 30004],
  "authors": ["<bahia-service-pubkey>"],
  "#domain": ["sbom"],
  "#subject_type": ["artifact"],
  "#subject": ["sha256:<subject-digest>"]
}
```

Process historical `EVENT`s, treat `EOSE` as catch-up completion, and keep the subscription open for realtime progress when needed. Generated or imported SBOM payload bytes are stored on Blossom; Nostr events carry references, hashes, status, and audit facts rather than full SBOM payloads. Relay `OK` acceptance is required for the `30078` reference and the `30004` availability list before Bahia marks a manifest published.

Package and repository SBOM requests must identify immutable subjects. If `subject.digest` is omitted, use `subjectLocator`:

```json
{
  "subject": { "type": "package" },
  "subjectLocator": {
    "package": {
      "repository_id": "<package-repository-uuid>",
      "namespace": "@company",
      "package_name": "utils",
      "version": "1.2.3",
      "filename": "utils-1.2.3.tgz",
      "sha256": "<package-archive-sha256>"
    }
  }
}
```

```json
{
  "subject": { "type": "repository" },
  "subjectLocator": {
    "repository": {
      "repository_url": "https://git.example/company/api.git",
      "commit": "<40-or-64-hex-git-object-id>"
    }
  }
}
```

For repository archive snapshots, use `subjectLocator.repository.content_digest` as `sha256:<repository-archive-digest>`. Do not identify package or repository SBOM subjects by mutable names alone.

## Security OSV/SBOM Scan Observables

Security scanning is an event-driven observer of canonical SBOM truth and an explicit ContextVM scan surface. SBOM generation/import still completes on SBOM `30078` references and `30004` availability lists; Security watches those events and publishes separate Security observables after it verifies the referenced payload hash and scans normalized targets.

Security ContextVM methods are:

| Method | Use | Completion signal |
|--------|-----|-------------------|
| `security/scan` | Request a scan for an SBOM reference, package coordinate, PURL, or Git commit. | Security `30315`/`30900`/`30078`/`4903` observables. |
| `security/rescan` | Request another scan run for a known target. | New correlated scan status and summary events. |
| `security/findings-list` | Read persisted finding projections. | Immediate read response only. |
| `security/schedules-list` | Read policy-derived schedules and freshness state. | Immediate read response only. |

Follow a scan with scoped filters:

```json
{
  "kinds": [30315, 30900, 30078, 4903],
  "authors": ["<bahia-service-pubkey>"],
  "#domain": ["security"],
  "#target_key_hash": ["<target-key-hash>"],
  "#e": ["<contextvm-request-event-id>"]
}
```

Security status events use `schema=bahia.status.security-scan.v1`; summaries use `bahia.security.scan-summary.v1` or `bahia.security.target-summary.v1`; normalized finding app-data uses `bahia.security.findings.v1`; audit facts use `bahia.audit.security.v1`. Clients process historical events until `EOSE`, keep subscriptions open for realtime convergence, deduplicate by event id, handle `CLOSED` and `AUTH`, and never poll REST or MCP for scan completion. Relay `OK accepted=true` is required for every Security observable publication.

Policy breach notifications use the existing notification dispatcher with event type `security.policy_breached`. Bahia sends that notification only when a persisted breach fingerprint is new or materially changed; unchanged recurring breaches do not generate duplicate notifications.

## Durability, replay, and retention

Bahia persists signed outbound events before publishing them when the event repository implements the outbox interface. A failed relay publish remains pending and the publisher retries unpublished records in batches of 100 with backoff; success updates the durable publish state. Monitor `bahia_nostr_outbox_depth` together with relay reconnect, re-request, and closed-reason metrics.

The sidecar stores accepted non-ephemeral events in SQLite with WAL and full synchronous writes, so retained relay history survives process and container restarts until its retention window expires. Replaceable events retain only the newest event for their replaceable key. Live ephemeral kinds `25910` and `21059` are broadcast rather than stored by the relay.

Backend subscribers resume from the newest persisted cursor with a one-second overlap and suppress duplicate event IDs. Their default catch-up limit is 1,000. Browser long-lived subscriptions use the same one-second overlap principle and preserve each original filter's cap. The Events view separately caps canonical read models at 1,000 and seven-day activity at 100.

The sidecar applies a hard query ceiling through `nostr.sidecar.max_query_limit` (default 2,000), even if a client asks for more. Retention sweeps run at startup and every 15 minutes. Defaults are:

```yaml
nostr:
  sidecar:
    event_retention: 168h
    request_retention: 24h
    max_query_limit: 2000
```

The retention sweep classifies stored ContextVM gift-wrap kind `1059` under the shorter request window. Ephemeral kinds (`20000`–`29999`), including `25910` and `21059`, are broadcast-only, are never stored by the sidecar, and are unaffected by retention settings. Other stored observables use general event retention. Retention settings must be positive when the sidecar is enabled.

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
| 30004 | NIP-51 SBOM availability lists |
| 30078 | NIP-78 app data, including SBOM references and Security findings |
| 30900 | Canonical control-plane state projection |
| 4903 | Canonical audit fact |
| 30315 | NIP-38 operational status |
| 5 | NIP-09 delete event where Nostr deletion semantics apply |

### Legacy migration inventory

Legacy Bahia-specific request/status/result/read-model ranges (`5961`-`6006`, `6961`-`6997`, `7961`-`7997`, `31961`-`32003`, `38390`-`38431`, `5980`, `7980`) are migration inventory only, excluding explicitly documented SoulFactory interop kinds `5950`, `6950`, `7950`, `1950`, `1951`, `30317`, `38384`, and `38386` where their numbers overlap those ranges. Production clients must not publish or subscribe to legacy Bahia numbers as live contracts.

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
