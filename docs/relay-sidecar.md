# Bahia Relay Sidecar

Bahia uses a Khatru-based Nostr relay sidecar as the supported browser-facing and server-facing control-plane topology. The sidecar is a standalone HTTP/NIP-11/WebSocket Nostr relay server, not a REST CRUD API: `cmd/relay/main.go` loads config and starts `relaysidecar.New(...).Run(ctx)`, while `internal/relaysidecar/server.go` mounts the Khatru relay on `/` and on the path from `nostr.sidecar.public_url` such as `/relay`. The sidecar stores rebuildable relay state; Bahia's database remains the source of truth and the projector republishes read-model snapshots when needed.

## Configuration

```yaml
nostr:
  relays:
    - "wss://upstream.example"     # compatibility service/upstream interop source; never browser or ContextVM policy
  service_relays:
    - "wss://service-relay.example" # backend service publish/backfill relays
  browser_relays:
    - "ws://localhost:3000/relay"  # browser relay discovery
  contextvm_relays:
    - "ws://localhost:3000/relay"  # ContextVM request/reply relays; may intentionally reuse the sidecar URL
  relay_auth_unavailable: "exclude_and_fail"
  sidecar:
    enabled: true
    listen_addr: "0.0.0.0:3334"
    public_url: "ws://localhost:3000/relay"
    backend_url: "ws://relay:3334"
    data_dir: "./data/relay-sidecar"
    mirror_external: false
    event_retention: 168h
    request_retention: 24h
    auth_private_key: ""
    administrator_pubkeys: ["<64-hex-admin-pubkey>"]
    config_trusted_pubkeys: ["<64-hex-config-author-pubkey>"]
    admin_policy_path: "./data/relay-sidecar/relay-admin-policy.json"
    config_projection_path: "./data/relay-sidecar/config-fabric-projection.json"
    service_id: "bahia-relay-sidecar"
    scope: "prod"
    max_query_limit: 2000
```

- `public_url` / `browser_relays` are exposed through ContextVM discovery (`11316`-`11320`) and the `d=bahia-browser-v1` NIP-51 relay set (`30002`) to the frontend.
- `contextvm_relays` is the preferred ContextVM request/reply relay policy and is projected as `d=bahia-contextvm-v1`; deployments may intentionally reuse the sidecar URL, but the purpose remains distinct from browser bootstrap.
- `service_relays` is the backend service publish/backfill policy and is projected as `d=bahia-service-v1`; `nostr.relays` remains only a backward-compatible service alias.
- `relay_auth_unavailable=exclude_and_fail` means auth-required relays without usable credentials are excluded from the current operation and the operation fails if remaining relays cannot satisfy the success rule.
- `backend_url` is used by Bahia itself for publish/subscribe in sidecar-first mode. In Docker Compose this should point at `ws://relay:3334/relay` so backend and browser both target the explicit relay mount.
- `request_retention` applies to stored ContextVM request/response transport kind `1059`; `event_retention` applies to every other stored kind. Ephemeral kinds (`20000`–`29999`), including `25910` and `21059`, are broadcast-only, are never stored by the sidecar, and are unaffected by retention settings. The sidecar sweeps expired events by their Nostr `created_at` timestamp at startup and every 15 minutes while running.
- `max_query_limit` caps events yielded by one replay query after honoring a lower client `limit`. The default/current checked-in value is `2000`. `EOSE` completes only that bounded query; clients that may reach the cap must narrow resource tags and `since`/`until` windows, overlap windows, and deduplicate by event id.
- `administrator_pubkeys` seeds the durable NIP-86 administrator allowlist only when `admin_policy_path` is absent. The policy file then owns the allowlist, allowed/banned pubkey sets, relay metadata, and used NIP-98 authorization IDs. `config_trusted_pubkeys` authorizes signed NIP-51/NIP-78 desired state; it defaults to the administrator seed when omitted.
- The mounted YAML file is authoritative for `sidecar.enabled`, `public_url`, `backend_url`, `max_query_limit`, `nostr.contextvm_relays`, and `reconcile.enabled`. Their legacy environment variables seed missing YAML keys once through an atomic write and never override keys already present. Send `SIGHUP` to `bahia-server` or `bahia-relay` to validate the mounted file and rebuild the affected in-process runtime without recreating the container.
- When sidecar mode is enabled, Bahia's own ContextVM transport and canonical observable projectors use the sidecar URL instead of connecting directly to `nostr.relays`. This keeps ContextVM `25910`, CEP-4/NIP-59 wrappers (`1059`/`21059`), canonical observables (`30900`, `4903`, `30315`, `11316`-`11320`, `30002`, `30078`), and relevant interop traffic sidecar-first.
- During the compatibility window, non-browser interop subscribers may still use `nostr.relays` as the upstream interop source unless `mirror_external=true`; service publish/backfill should prefer `service_relays`, browser bootstrap uses `browser_relays`, and ContextVM request/reply uses `contextvm_relays`. With mirroring enabled, Bahia uses the sidecar as the public upstream boundary and does not also connect directly to mirrored upstream URLs. Private, Loom, repository/ngit, DM, and relay-administration relays stay direct and separate unless a deployment explicitly routes those purposes through the sidecar.

## Local topology

`docker-compose.yml` starts:

- `relay`: the Khatru sidecar (`cmd/relay`) on `:3334` (serves both `/` and `/relay` for backward compatibility)
- `bahia`: backend publishing/subscribing to `nostr.sidecar.backend_url`
- `web`: nginx proxy exposing `/relay` to the browser

Both Go services mount `config.compose.yaml` at `/etc/bahia/config.yaml`; mutable sidecar routing and query policy is intentionally absent from their environment blocks.

Browser flow:

1. Bootstrap from ContextVM discovery (`11316`-`11320`) and NIP-51 relay sets (`30002`).
2. Read the browser relay set and service pubkey.
3. Connect to `/relay` WebSocket.
4. Subscribe to scoped ContextVM responses/gift-wraps and canonical observables (`30900`, `4903`, `30315`, `11316`-`11320`, `30002`, `30078`) and wait for the bounded query's EOSE.
5. Keep live subscriptions open.

## Policy

The sidecar accepts subscriptions for every Nostr event kind and does not maintain event-kind, recipient, or filter-scope allowlists. Its persisted relay-administration projection may enforce mutable pubkey admission: banned pubkeys are always rejected, and a non-empty allowed set admits only its members.

Before persistence, it still enforces protocol validity: the event ID must match the serialized event, the Schnorr signature must verify, and `created_at` must fall within the configured operational timestamp bounds. Search remains disabled because the store does not implement NIP-50; ordinary NIP-01 filters, including filters without `kinds`, are accepted.

Accepted history is durable SQLite state under `data_dir`, not an in-memory-only cache. ID, kind, author, `since`, and `until` filters are pushed into SQL before full NIP-01 matching. Replay uses a separate read pool so long queries cannot consume the single write connection needed to persist an event and return `OK`. The sidecar persists first, acknowledges promptly, and performs subscriber fanout asynchronously; slow subscribers therefore do not block unrelated publishers.

Relay-owner management uses NIP-86 `supportedmethods`, allowed/banned pubkey mutation and list methods, and relay name/description/icon methods. Every POST is gated by a kind-`27235` NIP-98 event whose id, signature, `u`, `method=POST`, exact body payload hash, ±60-second timestamp, single-use event ID, and persisted administrator signer are validated. Mutations atomically sync and rename the mounted policy before changing live admission or metadata. `config/status`, `config/reload`, and `config/reconcile` use the ContextVM kind-`25910` handler surface and the managed NIP-86 target.

Signed desired state is consumed directly: kind `30000` `service:<service_id>:membership` lists replace the allowed set, while kind `30078` `service:<service_id>:relay-sidecar` documents apply allowed/banned sets and metadata. The consumer verifies the event, trusted author, required tags, schema, target, and monotonic version, persists the desired projection, applies the mounted relay policy, and publishes signed kind-`30900` applied/rejected status.

Authorization belongs to consumers. Bahia validates signatures, authors, encryption, tags, capabilities, and application semantics before acting on an event. Relay admission is not an authorization boundary.

Bahia's PostgreSQL Nostr publish outbox is separate from this sidecar store. The outbox retries service-authored outbound events; the sidecar preserves events it has already accepted for replay and retention.

## Upstream mirroring guardrail

`mirror_external` is optional. Enable it only when the sidecar deployment is configured as the mirror boundary for `nostr.relays`. When true, Bahia routes public interop/audit subscriptions through the sidecar instead of also connecting directly to those upstream URLs; when false, Bahia keeps direct upstream subscriptions for non-control-plane interop. Bahia-owned control-plane publication remains sidecar-only in both modes.
