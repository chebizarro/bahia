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
    event_retention: 0s                              # regular events durable; set a duration to cap them
    request_retention: 24h
    request_retention_kinds: [25910, 1059, 21059]    # request/transport kinds
    negentropy_max_events: 1000000
    auth_private_key: ""
    administrator_pubkeys: ["<64-hex-admin-pubkey>"]
    config_trusted_pubkeys: ["<64-hex-config-author-pubkey>"]
    admin_policy_path: "./data/relay-sidecar/relay-admin-policy.json"
    config_projection_path: "./data/relay-sidecar/config-fabric-projection.json"
    service_id: "bahia-relay-sidecar"
    scope: "prod"
    max_query_limit: 2000
    subscriber_queue_size: 1024
```

- `public_url` / `browser_relays` are exposed through ContextVM discovery (`11316`-`11320`) and the `d=bahia-browser-v1` NIP-51 relay set (`30002`) to the frontend.
- `contextvm_relays` is the direct ContextVM request/reply relay policy and is projected as `d=bahia-contextvm-v1`. Bahia subscribes and publishes through its deduplicated union with the enabled sidecar URL; `browser_relays` is the direct fallback when this list is empty.
- `service_relays` is the backend service publish/backfill policy and is projected as `d=bahia-service-v1`; `nostr.relays` remains only a backward-compatible service alias.
- `relay_auth_unavailable=exclude_and_fail` means auth-required relays without usable credentials are excluded from the current operation and the operation fails if remaining relays cannot satisfy the success rule.
- `backend_url` is used by Bahia itself for publish/subscribe in sidecar-first mode. In Docker Compose this should point at `ws://relay:3334/relay` so backend and browser both target the explicit relay mount.
- Retention is by kind class and runs at startup and every 15 minutes, by each event's `created_at`:
  - Request/transport kinds in `request_retention_kinds` (default ContextVM `25910` and gift wraps `1059`/`21059`) are deleted after `request_retention` (default `24h`, at least `1m`). Only `1059` is actually stored: ephemeral kinds (`20000`–`29999`), including `25910` and `21059`, are broadcast-only and never stored.
  - Regular events, such as `4903` audit and state facts, are durable by default (`event_retention: 0`). A duration of at least `1h` caps their age.
  - Replaceable and addressable events are never age-swept: latest-wins keeps one event per coordinate. Kind-5 deletion requests are never swept either, because they are the tombstones that stop a deleted event being re-accepted. Validation rejects these kinds in `request_retention_kinds`.
  - NIP-40: an event whose `expiration` has passed is refused on publish, never returned by REQ/COUNT/negentropy, and deleted by the next sweep (an index of expirations avoids scanning).
  - NIP-11 `retention` describes these classes.
- NIP-09: a kind-5 request is stored and applied. `e` references delete the requester's own events; `a` references (`<kind>:<pubkey>:<d>`, with an empty `d` for plain replaceable kinds) delete every version of the requester's coordinate up to the request's `created_at`. References to other authors' events and to deletion requests are ignored. A deleted event, or an older version of a deleted coordinate, is refused with `OK false` / `blocked: … (NIP-09)`, including when the request arrived first. This holds for coordinates of any length. The eventstore's tag index skips values longer than 100 bytes, and relay config coordinates are 108, so the sidecar keeps its own deletion index in the same bbolt file, keyed by a SHA-256 of the coordinate. A write costs one lookup in it. Entries are added before their request is stored and removed only when the NIP-40 sweep removes it. A store written before the index existed is indexed once, on its first open. For the same reason, versions of an addressable coordinate whose `d` is empty or longer than 100 bytes are matched on `d` by the sidecar rather than through the `#d` index. A store written before `bahia-irsry.44` is repaired once on its first open after the upgrade (its own marker): stored kind-5 requests are applied and every such coordinate is collapsed to its latest version (`bahia-irsry.54`). The daemon's local event store uses the same code (`internal/boltcoord`).
- Tag filters on values of any length: REQ and COUNT filters on a tag value the eventstore does not index (empty, or longer than 100 bytes, such as `#a` with a relay config coordinate or `#d` with an empty `d`) are answered from a second sidecar index in the same bbolt file, keyed by tag name and a SHA-256 of the value (`bahia-irsry.52`). The read costs one index range per such value plus the eventstore's own read of the filter's other values for that tag, merged newest first; every event is checked against the whole filter. It never scans the store. Every write and delete keeps the index in step, and a store written before it existed is indexed once, on its first open after the upgrade (one full read, behind a marker). The daemon's local event store does the same for its `QueryEvents`.
- NIP-77: negentropy is enabled. A client reconciles a filter's full set, for example with `nip77.NegentropySync(ctx, url, filter, local, local, nip77.SyncEventsFromIDs)`, instead of paging `since` cursors under `max_query_limit`. A `NEG-OPEN` whose filter matches more than `negentropy_max_events` (default `1000000`, at most `10000000`) is refused with `NEG-ERR` ("narrow it with since/until"); it is never reconciled against a truncated set.
- `max_query_limit` caps events yielded by one replay query after honoring a lower client `limit`. The default/current checked-in value is `2000`, advertised as NIP-11 `limitation.max_limit` and `default_limit`. `EOSE` completes only that bounded query; clients that may reach the cap must narrow resource tags and `since`/`until` windows, overlap windows, and deduplicate by event id, or use NIP-77.
- NIP-11 advertises NIPs 1, 9, 11, 17, 40, 42, 44, 45, 51, 59, 65, 70 and 77. `limitation` states the enforced bounds: `max_message_length` (512000), `max_content_length` (65535, the eventstore codec's limit), and `created_at_upper_limit` (600 seconds). `created_at_lower_limit` is omitted: regular and ephemeral events created more than a year ago are refused, but replaceable and addressable events and deletion requests are accepted at any age (C-11, the same rule the daemon applies to inbound events), and the field cannot express a per-kind bound. `posting_policy` states the rule. `restricted_writes` is true while the NIP-86 allowed-pubkey set is non-empty.
- Stored events must fit the eventstore codec: content up to 65535 bytes, a tag section up to 65535 bytes, and at most 255 items per tag. Larger events are refused with `OK false` / `invalid: …`. Ephemeral events are not stored and only need to fit `max_message_length`. ContextVM therefore sends any gift wrap too large to store (plaintext over 40,960 bytes, which NIP-44 pads and base64-encodes past 65,535) as an ephemeral `21059` wrap instead of a stored `1059`. The web and `pkg/client` do this for requests, and the daemon for replies. The wrap is relayed live to the peer, which subscribes to both wrap kinds before publishing, and is never stored. A frame over `max_message_length` makes the relay close the connection without any OK, so the web checks each ContextVM request's size before publishing and fails with an error that points to Blossom. For the same reason, inline `sbom/import` payloads are limited to 360 KiB, in both the web and the daemon; a larger SBOM is uploaded to Blossom and imported by `location`.
- `subscriber_queue_size` (default `1024`, allowed `1`–`65536`) bounds the live events queued per client connection. A subscription whose connection falls further behind is sent `["CLOSED", <id>, "error: live delivery queue overflowed; …"]`, and its listener is removed server-side. The client re-subscribes from its last received event. `GET /metrics` on the sidecar port exposes `bahia_relay_sidecar_subscription_overflow_closes_total`, `bahia_relay_sidecar_subscriber_queue_size`, and `bahia_relay_sidecar_retention_deleted_events_total{cause="nip40_expired"|"request_retention"|"event_retention"}`.
- Live events that are saved while a `REQ`'s stored query runs, before khatru registers the live listener, are buffered and delivered after the stored events. Events the stored query already returned are skipped. The buffer is bounded by `subscriber_queue_size`, and overflowing it also yields `CLOSED`. Delivery across that boundary is at-least-once: if an event's save lands just before the query reads it but its live dispatch comes only after the listener is registered, it can arrive twice. Clients deduplicate by event id.
- `administrator_pubkeys` seeds the durable NIP-86 administrator allowlist only when `admin_policy_path` is absent. The policy file then owns the allowlist, allowed/banned pubkey sets, relay metadata, and used NIP-98 authorization IDs. `config_trusted_pubkeys` authorizes signed NIP-51/NIP-78 desired state; it defaults to the administrator seed when omitted.
- The mounted YAML file is authoritative for `sidecar.enabled`, `public_url`, `backend_url`, `max_query_limit`, `nostr.contextvm_relays`, and `reconcile.enabled`. Their legacy environment variables seed missing YAML keys once through an atomic write and never override keys already present. Send `SIGHUP` to `bahia-server` or `bahia-relay` to validate the mounted file and rebuild the affected in-process runtime without recreating the container.
- When sidecar mode is enabled, canonical observable projectors remain sidecar-only. ContextVM request subscriptions and response publication instead use the enabled sidecar URL plus `contextvm_relays` (or the `browser_relays` fallback), ensuring progress and terminal results reach direct operator relay subscriptions.
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

Accepted history is durable state in `data_dir/events.bolt`, a pure-Go bbolt [`fiatjaf.com/nostr/eventstore`](../third_party/nostr/eventstore) database, not an in-memory-only cache. The sidecar does not federate or forward accepted events to external relays; external delivery requires Bahia to publish directly to those configured destinations. Queries are answered from indexes on kind, author, kind+author, `created_at` and single-letter tags (`#e`, `#p`, `#d`, `#a`, `#t`, …), and COUNT uses the same indexes. Replay reads copy each page of at most 1000 events out of the database before sending it, so a slow subscriber never holds a read transaction that writers wait on. The sidecar persists first, acknowledges promptly, and performs subscriber fanout asynchronously; slow subscribers therefore do not block unrelated publishers.

bbolt holds an exclusive lock on `events.bolt`. Only one `bahia-relay` process may use a `data_dir`; a second process fails to start after two seconds with "is another relay process using this data_dir?". Within one process, a `SIGHUP` reload shares the open database between the outgoing and the replacement runtime.

### Storage migration from SQLite (runbook)

Before `bahia-irsry.9.1`, history lived in `data_dir/events.sqlite`. The first start of the new binary imports it automatically, before the relay serves traffic:

1. Deploy as usual. In Docker Compose the store stays on the `relaydata` volume at `/var/lib/bahia/relay-sidecar`, so no volume or config change is needed.
2. Watch the start-up log for `relay sidecar importing legacy SQLite event store`, then `relay sidecar imported legacy SQLite store` with `rows`, `imported`, `already_present`, `skipped_invalid` and `skipped_oversize`. The import runs once per `data_dir`, and its result is recorded in `events.bolt`. It is idempotent: a crash mid-import simply reruns on the next start, and latest-wins replacement keeps only the newest version of each coordinate. The import syncs to disk once at the end rather than once per event.
3. A `Warn` line means some rows were skipped. `skipped_oversize` counts events beyond the codec limits above (for example a large `1059` gift wrap). `skipped_invalid` counts rows with corrupt JSON or an id that doesn't match the content. They remain in `events.sqlite`.
4. `events.sqlite` (with `-wal`/`-shm`) is left in place as a rollback copy. Rolling back to an older binary serves that copy, which is missing anything accepted since the upgrade. Once the new relay has served correctly for a retention cycle, delete `events.sqlite*`.

Relay-owner management uses NIP-86 `supportedmethods`, allowed/banned pubkey mutation and list methods, and relay name/description/icon methods. Every POST is gated by a kind-`27235` NIP-98 event whose id, signature, `u`, `method=POST`, exact body payload hash, ±60-second timestamp, single-use event ID, and persisted administrator signer are validated. Mutations atomically sync and rename the mounted policy before changing live admission or metadata. `config/status`, `config/reload`, and `config/reconcile` use the ContextVM kind-`25910` handler surface and the managed NIP-86 target.

Signed desired state is consumed directly: kind `30000` `service:<service_id>:membership` lists replace the allowed set, while kind `30078` `service:<service_id>:relay-sidecar` documents apply allowed/banned sets and metadata. The consumer verifies the event, trusted author, required tags, schema, target, and monotonic version, persists the desired projection, applies the mounted relay policy, and publishes signed kind-`30900` accepted/applied/rejected status. When a desired event is deleted (NIP-09) or expires (NIP-40), the consumer publishes `withdrawn` status and keeps the last applied policy live: it intentionally does not revert the allowlist, since an empty allowlist admits every pubkey. Publish a newer version to change it ([status semantics](nostr-event-implementation-guide.md#config-fabric-durable-status-receipts)).

Authorization belongs to consumers. Bahia validates signatures, authors, encryption, tags, capabilities, and application semantics before acting on an event. Relay admission is not an authorization boundary.

Bahia's PostgreSQL Nostr publish outbox is separate from this sidecar store. The outbox retries service-authored outbound events; the sidecar preserves events it has already accepted for replay and retention.

## Upstream mirroring guardrail

`mirror_external` is optional. Enable it only when external infrastructure makes the sidecar deployment the mirror boundary for `nostr.relays`; the embedded Khatru sidecar itself does not federate events. When true, Bahia routes public interop/audit subscriptions through the sidecar instead of also connecting directly to those upstream URLs; when false, Bahia keeps direct upstream subscriptions for non-control-plane interop. Canonical Bahia-owned control-plane publication remains sidecar-only in both modes, while ContextVM request and response pools always retain their direct policy destinations.
