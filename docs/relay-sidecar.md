# Bahia Relay Sidecar

`bahia-relay` is Bahia's Nostr relay: a khatru-based WebSocket/NIP-11 server
with a bbolt event store. It is the relay the web app, the CLI, MCP clients
and the daemon itself talk to. It stores every valid signed event its policy
admits, serves public topics to anyone and protected topics to authenticated,
admitted readers, and keeps regular events durably. It does not federate; the
daemon publishes to any further relays itself.

`cmd/relay/main.go` loads the configuration, opens the service signer and
runs `relaysidecar.New(...)`. The relay is mounted on `/` and on the path of
`nostr.sidecar.public_url` (for example `/relay`), so a reverse proxy can
expose a single path to browsers.

## Service identity

The sidecar runs as Bahia's service identity and reads the same `nostr.signer`
settings as the daemon (`local`, `nip46` or `nip55l`; see
[`runbooks/service-signer.md`](runbooks/service-signer.md)). The signer's
pubkey is the NIP-11 `pubkey`, is always admitted to write and to read
protected topics, and signs the config-status acknowledgements. Startup fails
when no signer is configured, it cannot be opened, or it reports a pubkey
other than `nostr.public_key`; the sidecar never runs without its identity.

A `SIGHUP` keeps the open signer session when the reloaded `nostr.signer`,
`nostr.public_key` and `nostr.private_key` are unchanged. When they change,
the new signer is opened first; the old one is closed only after the
replacement runtime has taken over, and a signer that fails to open leaves
the running sidecar untouched. On shutdown the signer is closed after the
relay and its config workers have stopped.

## Configuration

```yaml
nostr:
  service_relays:                  # the daemon's publish/backfill relays beyond the sidecar
    - "wss://service-relay.example"
  browser_relays:                  # advertised to browsers (d=bahia-browser-v1)
    - "ws://localhost:3000/relay"
  contextvm_relays:                # intents and ContextVM (d=bahia-contextvm-v1)
    - "ws://localhost:3000/relay"
  sidecar:
    enabled: true                  # default false
    listen_addr: "0.0.0.0:3334"    # default 127.0.0.1:3334
    public_url: "ws://localhost:3000/relay"   # default ws://127.0.0.1:3334
    backend_url: "ws://relay:3334/relay"      # what the daemon connects to; default ws://127.0.0.1:3334
    data_dir: "./data/relay-sidecar"
    mirror_external: false
    event_retention: 0s            # 0 = regular events are durable; otherwise ≥ 1h
    request_retention: 24h         # ≥ 1m
    request_retention_kinds: [25910, 1059, 21059]
    negentropy_max_events: 1000000 # ≤ 10000000
    auth_private_key: ""
    administrator_pubkeys: ["<64-hex>"]
    config_trusted_pubkeys: ["<64-hex>"]      # defaults to administrator_pubkeys
    admin_policy_path: "./data/relay-sidecar/relay-admin-policy.json"
    config_projection_path: "./data/relay-sidecar/config-fabric-projection.json"
    service_id: "bahia-relay-sidecar"
    scope: "prod"
    max_query_limit: 2000
    subscriber_queue_size: 1024    # 1–65536
    read_auth_mode: "enforce"      # enforce | warn | off
    read_auth_allowed_pubkeys: []  # extra NIP-42 readers of protected topics
```

- `backend_url` is what the daemon dials; `public_url` is what clients dial
  and is advertised through discovery. In Docker Compose both point at the
  explicit `/relay` mount (`ws://relay:3334/relay` from the daemon,
  `ws://localhost:3334/relay` or the nginx `/relay` proxy from browsers).
- `sidecar.enabled`, `public_url`, `backend_url`, `max_query_limit`,
  `nostr.contextvm_relays` and `reconcile.enabled` are owned by the mounted
  YAML file. Their environment variables (`BAHIA_NOSTR__SIDECAR__ENABLED`,
  `BAHIA_NOSTR__SIDECAR__PUBLIC_URL`, `BAHIA_NOSTR__SIDECAR__BACKEND_URL`,
  `BAHIA_NOSTR__SIDECAR__MAX_QUERY_LIMIT`, `BAHIA_NOSTR_CONTEXTVM_RELAYS`,
  `BAHIA_RECONCILE_ENABLED`) seed a key that is absent from the file, once,
  through an atomic write, and never override a key that is present. Send
  `SIGHUP` to `bahia-relay` or `bahia-server` to re-validate the file and
  rebuild the affected runtime without recreating the container.
- `administrator_pubkeys` seeds the NIP-86 administrator set only when
  `admin_policy_path` does not exist yet; afterwards the policy file owns
  administrators, the allowed and banned pubkey sets, relay metadata and the
  used NIP-98 authorization ids. `config_trusted_pubkeys` are the authors
  whose signed NIP-51/NIP-78 desired state the config consumer applies.
- `mirror_external: true` declares the sidecar deployment the mirror
  boundary for `nostr.relays`: the daemon then routes public interop and
  audit subscriptions through the sidecar instead of also connecting to
  those URLs. The sidecar itself never forwards events.

## Admission

Every event must have a correct id and a valid Schnorr signature, a
`created_at` no more than 10 minutes in the future, and — for regular and
ephemeral kinds — a `created_at` no older than one year; replaceable and
addressable events and deletion requests are accepted at any age. An event
whose NIP-40 `expiration` has passed is refused. Events that do not fit the
store (content or tag section over 65535 bytes, more than 255 items in a tag)
are refused with `OK false` / `invalid: …`; ephemeral events are not stored
and only need to fit the 512000-byte message limit.

Write admission is by pubkey, not by kind:

- banned pubkeys are always refused;
- when the allowed set is non-empty only its members, the service pubkey,
  and two scoped exceptions may write: pubkeys in the **intent-author set**
  may publish `30900` events tagged `t=bahia-intent`, and
  `config_trusted_pubkeys` may publish the `30000`/`30078` config documents;
- when the allowed set is empty every valid pubkey may write.

The intent-author set is pushed by the daemon through the NIP-86
`setintentauthors` method whenever its TrustSet changes (fleet operators,
org members, bootstrap owners). This requires
`nostr.relay_administration.enabled: true` with a `bahia_owned` target for
this sidecar and a resolvable `administrator_private_key_ref`; otherwise the
daemon logs `intent authors syncer disabled: …`.

Search (`NIP-50`) is not implemented and such filters are refused; every
other NIP-01 filter, including one without `kinds`, is accepted.
Authorization of what an event *means* belongs to its consumers; relay
admission is a transport boundary, not an authorization boundary.

## Read authentication (NIP-42)

`read_auth_mode` governs `REQ`, `COUNT` and `NEG-OPEN` against protected
records. `enforce` (the default; an unset or unknown value normalizes to it)
answers a protected filter from an unauthenticated socket with
`["AUTH","<challenge>"]` followed by `["CLOSED",<id>,"auth-required: …"]`,
and an authenticated but unadmitted pubkey with `CLOSED … restricted: …`.
`warn` logs `read auth would reject REQ (warn mode)` and serves the filter
anyway; `off` disables read-side auth. Only `enforce` is suitable for
production.

What is protected is decided per filter in
`internal/relaysidecar/read_auth.go`:

- Kind `30900` is classified by its `#t` topic. The public topics are the
  registry, state, worker, DNS, backup, ML, SBOM, security-summary,
  assistant-status, continuity-heartbeat, managed-instance-health and
  route-canary families, and every OCK-encrypted family (org, member,
  invite, key envelope, secret, notification, payment, security finding,
  Blossom, package and tool intents, LLM release, artifact signature and
  SBOM) — see the visibility column of the
  [topic table](event-spec.md#family-topics). `security-findings`,
  `security-audit`, `assistant-transcript`, `assistant-session`,
  `relay-settings`, `config-status`, `runtime-observation`,
  `soul-factory-saga-run`, `soul-factory-adapter-ledger` and
  `soul-factory-runtime-policy` are protected, as is any `30900` filter
  without `#t` and any filter that mixes a protected topic in.
- Every other kind is protected except `0`, `3`, `5`, `10002`, `10050`,
  `22242`, `30002`, `30166`, `30315`, `30617`, `30618` and NIP-34
  `1617`–`1633`. Audit `4903`, app data `30078`, gift wraps, the SoulFactory
  and Loom families, documentation `30023` and intents therefore need AUTH.

Admitted readers are the service pubkey (the `nostr.signer` identity), NIP-86
administrators and allowed pubkeys, the intent-author set, and
`read_auth_allowed_pubkeys`. Any service that subscribes to a protected topic
with its own key must be listed: a separate SoulFactory controller key,
pinned runtime pubkeys, Loom workers reading job kinds, the Hive-CI runner.
The DNS agent (`dns-zone-sync`), the FIPS bridge (`dns-endpoint`), CLI state
reads and `pkg/discovery` read public topics only. `bahia config …` and
`pkg/client` intent and ContextVM clients authenticate with the operator key,
which is admitted through the intent-author sync. The web app hydrates public
read models before sign-in and answers the challenge with the signed-in
operator's signer; a signed-in pubkey the sidecar does not admit sees
`restricted:` in its connection status and only the public models.

Verify with `curl -H 'Accept: application/nostr+json' https://relay.example/`
(`"limitation":{…,"auth_required":true}`) and an unauthenticated
`["REQ","x",{"kinds":[30900],"#t":["soul-factory-runtime-policy"]}]`, which
is answered with `AUTH` and `CLOSED`, while `#t: ["service-state"]` returns
events and `EOSE`.

## Storage, queries and delivery

- Accepted history is durable in `data_dir/events.bolt`
  (`fiatjaf.com/nostr/eventstore`, pure Go). bbolt holds an exclusive lock, so
  one process per `data_dir`; a second one fails after two seconds with
  `is another relay process using this data_dir?`. A `SIGHUP` reload shares
  the open database between the outgoing and replacement runtime.
- When `data_dir/events.sqlite` is present, startup imports it once before the
  relay serves traffic. The summary reports `rows`, `imported`,
  `already_present`, `skipped_invalid` and `skipped_oversize`. Imported rows
  are recorded in `events.bolt`; the SQLite source remains in place so the
  operator can retain or remove it after verifying the bbolt store.
- Queries use indexes on kind, author, kind+author, `created_at` and
  single-letter tags; `COUNT` uses the same indexes. Tag values the
  eventstore does not index (empty, or longer than 100 bytes, such as `#a`
  with a config coordinate) are answered from the sidecar's own hashed tag
  index, so filters on any value length work without a scan.
- `max_query_limit` caps the events one replay query yields after honouring a
  lower client `limit`; it is advertised as NIP-11 `limitation.max_limit`
  and `default_limit`. `EOSE` completes only that bounded query. Replay
  copies pages of at most 1000 events out of the database before sending, so
  a slow subscriber never holds a read transaction open.
- The relay persists first, acknowledges, then fans out asynchronously.
  `subscriber_queue_size` bounds the live events queued per connection; a
  subscription that falls further behind is sent
  `["CLOSED",<id>,"error: live delivery queue overflowed; …"]` and must
  re-subscribe from its last event. Live events saved while a `REQ`'s stored
  query runs are buffered and delivered after it; delivery across that
  boundary is at-least-once, so clients deduplicate by event id.
- NIP-77 negentropy is enabled. A `NEG-OPEN` whose filter matches more than
  `negentropy_max_events` is refused with `NEG-ERR` ("narrow it with
  since/until") rather than reconciled against a truncated set.
- NIP-11 advertises NIPs 1, 9, 11, 17, 40, 42, 44, 45, 51, 59, 65, 70 and 77,
  `limitation.max_message_length` (512000), `max_content_length` (65535),
  `created_at_upper_limit` (600), `auth_required` (true under `enforce`),
  `restricted_writes` (true while the allowed set is non-empty), the
  retention classes and a `posting_policy` text.
- `GET /metrics` on the sidecar port exposes
  `bahia_relay_sidecar_subscription_overflow_closes_total`,
  `bahia_relay_sidecar_subscriber_queue_size` and
  `bahia_relay_sidecar_retention_deleted_events_total{cause}`.

Large ContextVM wraps: a gift wrap whose plaintext exceeds 40,960 bytes pads
and base64-encodes past the 65535-byte content limit, so clients send it as
an ephemeral `21059` instead of a stored `1059`; the peer subscribes to both
wrap kinds before publishing. A frame over `max_message_length` closes the
connection without an `OK`, so the web checks request size before publishing
and inline `sbom/import` payloads are limited to 360 KiB (larger SBOMs go to
Blossom and are imported by `location`).

## Retention and deletion

Retention is by kind class and runs at startup and every 15 minutes, by
`created_at`:

- kinds in `request_retention_kinds` (default `25910`, `1059`, `21059`) are
  deleted after `request_retention`; of these only `1059` is ever stored,
  since ephemeral kinds (`20000`–`29999`) are broadcast only;
- regular events (`4903` audit facts, interop facts) are durable unless
  `event_retention` caps their age;
- replaceable and addressable events are never age-swept (latest-wins keeps
  one per coordinate), and kind-5 requests are never swept because they are
  the tombstones that stop a deleted event being re-accepted. Validation
  rejects these kinds in `request_retention_kinds`;
- an event whose NIP-40 `expiration` has passed is never returned and is
  deleted by the next sweep.

NIP-09: a kind-5 request is stored and applied. `e` references delete the
requester's own events; `a` references (`<kind>:<pubkey>:<d>`, empty `d` for
plain replaceable kinds) delete every version of the requester's coordinate
up to the request's `created_at`. References to other authors' events and to
deletion requests are ignored. A deleted event, or an older version of a
deleted coordinate, is refused with `OK false` / `blocked: … (NIP-09)` even
when the deletion arrived first. Coordinates of any length are handled
through the sidecar's own deletion index (`internal/boltcoord`, keyed by a
SHA-256 of the coordinate), which the daemon's local event store shares.

## Administration (NIP-86)

`POST` to the relay's HTTP endpoint with a NIP-98 event (kind `27235`) whose
id, signature, `u`, `method=POST`, body payload hash, ±60-second timestamp,
single-use event id and persisted administrator signer are validated.
Methods: `supportedmethods`, `allowpubkey`, `banpubkey`,
`listallowedpubkeys`, `listbannedpubkeys`, `changerelayname`,
`changerelaydescription`, `changerelayicon`, `setintentauthors`. Mutations
atomically rewrite the policy file before changing live admission or
metadata. The daemon drives this API through
`nostr.relay_administration` (`administrator_private_key_ref`, `targets[]`
with `ref`, `relay_url`, optional `http_url`, `authorization` of
`bahia_owned` or `bahia_authorized`, `administrator_pubkeys`).

## Config Fabric consumer

Signed desired state from `config_trusted_pubkeys` is applied directly: a
kind-`30000` list with `d=service:<service_id>:membership` replaces the
allowed set; a kind-`30078` document with `d=service:<service_id>:relay-sidecar`
applies allowed and banned sets and relay metadata. The consumer verifies the
event, author, required tags, schema, target and monotonic version, persists
the projection to `config_projection_path`, applies it, and publishes a
signed `30900` `config-status` record (`accepted`, `applied` or `rejected`).
When a desired event is deleted (NIP-09) or expires (NIP-40) it publishes
`withdrawn` and keeps the last applied policy live, because an empty
allowlist would admit every pubkey; publish a newer version to change it.
`bahia config publish|rollback|drift` are the operator commands.

## Local topology

`docker-compose.yml` runs `relay` (`:3334`, serving `/` and `/relay`),
`bahia` (dialing `nostr.sidecar.backend_url`) and `web` (nginx proxying
`/relay` to the relay). Both Go services mount `config.compose.yaml` at
`/etc/bahia/config.yaml`; mutable sidecar routing and query policy live in
that file, not in environment blocks.
