# Nostr Integration

Bahia is Nostr-native: signed Nostr events are the control plane. Operators publish signed **intents**, the daemon answers with bounded **status** events, and every durable fact is a service-signed **canonical record** on the relays. The browser, the CLI, and MCP are all clients of the same event contract.

## Identities

| Identity | Key source | Signs |
|----------|------------|-------|
| Bahia service | `nostr.private_key` | Canonical state, audit facts, intent statuses, discovery, documentation |
| Operator | NIP-07 extension, NIP-46 bunker, or a local key (CLI) | Intents, ContextVM requests, NIP-42 AUTH |
| Workers, runtimes, CI runners | Their own keys | Advertisements, job results, attestations |

Fleet operators are the pubkeys in `nostr.authorized_pubkeys`; `nostr.bootstrap_owners` maps an organization UUID to the pubkey that may create it before any membership record exists. The daemon publishes its operator allowlists as fleet-OCK-encrypted records (`d=operators:continuity`, `d=operators:soul-factory`, topic `operator-allowlist`), so a browser holding the fleet key can trust other operators' documents without the relay learning who they are.

## Event model

| Role | Kind | Author | Notes |
|------|------|--------|-------|
| Intent | `30900` with `t=bahia-intent` | operator | Replaceable per `(pubkey, d)`; optionally gift-wrapped |
| Intent status | `30315` | service | `d=intent-status:<requester>:<coordinate>`, expires after 7 days |
| Canonical state | `30900` with `schema=bahia.cp-state.v1` | service | One record per entity coordinate; `#t` names the family |
| Audit fact | `4903` | service | Regular event, `t=cp-audit`, `state=<coordinate>`, `fact=<dedupe id>` |
| Confidential request | `25910` inside NIP-59 `1059` | operator | Secret reveal, run-log fetch, operator assistant |
| Discovery | `11316`–`11320`, `30002`, `10002` | service | ContextVM announcements, NIP-51 relay sets, advisory NIP-65 list |
| Documentation | `30023` | service | This guide, `t=bahia-docs`, `d=<topic>` |

Interop kinds that Bahia consumes or emits with other fleet software keep their own contracts: Hive-CI workflow runs/results (`5401`/`5402`), Loom jobs (`5100`, `5101`, `5102`, `30100`), worker advertisements, SoulFactory lifecycle events (`1950`, `5950`, `6950`, `7950`, `31950`–`31953`, `38384`, `38386`), NIP-34 repository events, NIP-22 comments, config fabric (NIP-51 lists and NIP-78 `30078`), and SBOM references (`30078`, `30004`).

### Intents

An intent is a level-triggered desired-state document signed by the operator:

```json
{
  "kind": 30900,
  "tags": [
    ["d", "service:3f1b…"],
    ["t", "bahia-intent"],
    ["domain", "service"],
    ["op", "update"],
    ["schema", "bahia.intent.service.v1"],
    ["org", "11111111-1111-1111-1111-111111111111"],
    ["intent_id", "01926b5e-…"]
  ],
  "content": "{\"name\":\"payment-api\",\"artifact_repo\":\"ghcr.io/acme/payment-api\",\"expected_updated_at\":\"2026-09-01T12:00:00Z\"}"
}
```

- `d` is the entity coordinate; `domain` selects the handler; `op` defaults to `update`.
- `intent_id` is a UUIDv7 idempotency key. The daemon records every processed `intent_id` in its local store and never executes it twice. Reusing an `intent_id` with different content is a **conflict** for request-style operations (`build/request`, `adoption/scan`, `tool/approval-response`, `org/rekey`, the ML operations).
- `org` is required except for fleet-scoped domains: `dns`, `ml`, `worker`, `adoption`, `tool`, `security`, `sbom`, `relay`, and `org/rekey`.
- Updates carry the canonical record's RFC3339 `updated_at` as `expected_updated_at`; a stale revision is rejected as a conflict (numeric epochs are rejected outright).
- Confidential domains are gift-wrapped (NIP-59 `1059`) to the service pubkey: `org`, `secret`, `notification`, and `relay` from the web app; `org`, `secret`, and `notification` from the CLI. The daemon unwraps them and processes the inner `30900` exactly like a plain intent. A NIP-44-capable signer is required for these domains.

Registered domains: `adoption`, `artifact`, `backup`, `build`, `deployment`, `dns`, `environment`, `llm`, `ml`, `notification`, `org`, `package`, `policy`, `relay`, `runtime`, `sbom`, `secret`, `security`, `service`, `tool`, `worker`. All are enabled by default; `nostr.intent_domains_disabled` lists domains the daemon must ignore.

Authorization uses the verified event pubkey. Organization-scoped domains check the operator's role in the organization (owner, admin, deployer, viewer); fleet-scoped domains and operations (policies, workers, DNS, ML, tool decisions, adoption) require a fleet operator pubkey. Adoption requires the signer in `adoption.allowed_pubkeys`. Pubkeys listed in `nostr.authorized_pubkeys`, `adoption.allowed_pubkeys`, and `direct_runtime_actions.allowed_pubkeys`, together with every organization member, form the intent-author set the daemon pushes to the relay sidecar.

### Intent status

The daemon answers each processed intent with one replaceable `30315` per requester and coordinate:

```json
{
  "kind": 30315,
  "tags": [
    ["d", "intent-status:<requester-pubkey>:service:3f1b…"],
    ["domain", "intent"],
    ["status", "accepted"],
    ["t", "intent-status"],
    ["p", "<requester-pubkey>"],
    ["intent_id", "01926b5e-…"],
    ["expiration", "<unix-seconds>"],
    ["e", "<intent-event-id>"]
  ],
  "content": "{\"intent_id\":\"01926b5e-…\",\"coordinate\":\"service:3f1b…\",\"result\":\"applied\",\"data\":{…}}"
}
```

`status` is `accepted`, `rejected`, or `conflict`; `reason` explains a rejection or conflict. Request-style operations return their bounded output in `data` (a build ID, a redacted adoption findings page, a deployment preview hash). Status payloads are bounded: a deployment preview is compacted to its hash and summary, an adoption scan is paged, and a policy evaluation is refused above 16 KiB. A relay `OK` means delivery only; durable truth is the canonical record that follows.

### Canonical state

Every entity is projected as a service-signed `30900` record:

```json
{
  "kind": 30900,
  "tags": [
    ["d", "service:<service-id>:environment:<environment-id>"],
    ["domain", "service"],
    ["schema", "bahia.cp-state.v1"],
    ["legacy_kind", "31961"],
    ["deleted", "false"],
    ["t", "service-state"]
  ]
}
```

- Relays keep the newest event per `(pubkey, kind, d)`; a deletion is the same coordinate republished with `deleted=true`.
- `t` is the family topic. Subscribe by `#t` and the service author; `domain`, `schema` and `entity` are checked client-side because relays index only single-letter tags.
- `legacy_kind` is a numeric family discriminator inside the envelope, never a wire kind.

Public topics (readable without AUTH): `dns-zone`, `dns-endpoint`, `dns-policy`, `dns-backend`, `dns-zone-sync`, `service-state`, `service-registry`, `environment-registry`, `llm-route`, `llm-state`, `artifact-registry`, `deployment-intent`, `deployment-run`, `build-registry`, `policy-registry`, `package-repository`, `package-artifact`.

Every other topic is protected and requires NIP-42 authentication by an authorized reader. Confidential families are additionally encrypted: the content is NIP-44 ciphertext under an **organization content key (OCK)** that the daemon wraps to each member and to itself in `org-key-envelope` records. Fleet-wide confidential state (`payment-record`, `security-*`, `llm-release`, `package-intent`, `tool-*`, `notification-log`, `operator-allowlist`, SoulFactory ledgers) uses the well-known `fleet` scope, whose OCK is wrapped to every fleet operator and bootstrap owner. Removing a member or lowering a member's role rotates the organization's OCK, so the departed key cannot read later records. An organization with `strict_revocation` enabled is additionally **refounded** on every exclusion: the daemon finishes the key rotation and republishes the confidential records before the membership change is committed. `org/rekey` rotates a key on demand (owner or admin; fleet operators for the `fleet` scope).

### Audit facts

`4903` audit facts are append-only, `schema=bahia.audit.v1`, tagged `domain`, `type`/`event_type`, `t=cp-audit`, `state=<coordinate>` and `fact=<deterministic id>`. Consumers deduplicate on `fact`.

### ContextVM requests

A small set of operations are confidential request/response calls instead of intents. They are JSON-RPC 2.0 messages in kind `25910`, gift-wrapped (`1059`) to the service pubkey, with the inner event signed by the operator:

| Method | Purpose | Gate |
|--------|---------|------|
| `services/secrets-reveal` | Return a decrypted secret value | `secrets:read` in the owning organization |
| `deployments/run-logs-get` | Return stored logs of a completed run | run organization membership |
| `assistant/prompt`, `assistant/approval`, `assistant/cancel`, `assistant/reconcile` | Operator assistant sessions | fleet operator |

The response is a gift-wrapped `25910` addressed to the requester with `e=<inner request id>`. The daemon keeps a request ledger in its local store: a request event is executed at most once, a keyed request (`_meta.progressToken`) replays its saved response on retry, and an unkeyed or crashed request returns error `-32011` instead of re-executing. Requests older than 7 days, and requests created more than 2 minutes before a fresh ledger existed, are not run. Only stored `1059` wraps are recovered after downtime; plain `25910` and oversized-request `21059` wraps are ephemeral.

## Relays

### Relay roles

```yaml
nostr:
  private_key: "<service-hex-key or secret ref>"
  relays: ["wss://relay.example.com"]            # service publish/backfill relays
  service_relays: []                             # explicit alias for the same role
  browser_relays: ["wss://relay.example.com"]    # advertised to browsers and CLI discovery
  contextvm_relays: []                           # request/reply relays; browser_relays when empty
  nip34_relays: []                               # repository discovery relays
  publish_quorum: 1                              # write relays that must accept; -1 = all
  closed_retry_budget: 5                         # consecutive retryable CLOSED reissues per relay+filter (max 100)
  relay_auth_unavailable: exclude_and_fail       # the only supported value
  relay_quorum: { full_min_healthy: 2, degraded_min_healthy: 1, emergency_min_healthy: 1 }
  trusted_relay_monitor_pubkeys: []              # NIP-66 monitors (advisory only)
  dm_relay_lists: []                             # explicit NIP-51 10050 lists (notifications/service only)
  intent_domains_disabled: []
  authorized_pubkeys: []                         # fleet operators
  bootstrap_owners: {}                           # org UUID → owner pubkey
```

The service key publishes NIP-51 `30002` relay sets named `bahia-browser-v1`, `bahia-contextvm-v1`, and `bahia-service-v1`, plus an advisory NIP-65 `10002` list (ContextVM relays as `read`, service relays as `write`). Clients bootstrap from a configured relay and trusted service pubkey, then follow those sets. The web app receives its seed at container start as `PUBLIC_BAHIA_BOOTSTRAP_RELAYS` and `PUBLIC_BAHIA_SERVICE_PUBKEYS`.

The daemon's relay pool performs NIP-42 AUTH with the service key, reissues a REQ after `auth-required:` CLOSED, treats `blocked:`, `restricted:`, `invalid:`, `unsupported:`, `pow:` and `mute:` as terminal, and retries `error:`, `rate-limited:` and unknown reasons with backoff up to `closed_retry_budget` times (an EOSE resets the count). Exhaustion is counted by `bahia_nostr_relay_closed_retry_exhausted_total`. NIP-11 metadata and NIP-66 monitor reports annotate relay health but never add, remove, or trust relays.

### Publish outbox

Every event the daemon signs is written to its local outbox before any relay sees it. A pending entry is retried with backoff until every write relay has accepted it or reached a terminal state. When the retry budget is exhausted without quorum, the entry is **abandoned** and the record is marked undelivered locally; the `/health` check `canonical_delivery` reports `warn` with the affected coordinates until an operator re-enqueues them with `bahia outbox retry` (CLI, `--daemon`) or the `bahia_outbox_retry` MCP tool. Depth is exported as `bahia_nostr_outbox_depth`.

### Relay sidecar

The sidecar (`cmd/relay`) is a Khatru relay with a bbolt event store, NIP-42 read authentication, NIP-77 negentropy, NIP-86 administration, and retention by kind class. It serves NIP-11 metadata over HTTP and Nostr over WebSocket on `/` and on the path of `public_url`.

```yaml
nostr:
  sidecar:
    enabled: false
    listen_addr: "127.0.0.1:3334"
    public_url: "ws://127.0.0.1:3334"
    backend_url: "ws://127.0.0.1:3334"
    data_dir: "./data/relay-sidecar"
    service_id: "bahia-relay-sidecar"
    scope: "prod"
    read_auth_mode: enforce            # enforce | warn | off
    read_auth_allowed_pubkeys: []      # extra readers of protected topics
    administrator_pubkeys: []          # NIP-86 administrators
    config_trusted_pubkeys: []         # config-fabric authors
    event_retention: 0s                # regular events are durable; set ≥1h to cap
    request_retention: 24h             # stored request kinds (≥1m)
    request_retention_kinds: [25910, 1059, 21059]
    max_query_limit: 2000
    negentropy_max_events: 1000000
```

Admission: the sidecar verifies id and signature, refuses events more than 10 minutes in the future, refuses regular and ephemeral events older than one year, honours NIP-40 expiration, and blocks search filters. Writers are the service key, the NIP-86 admin allowlist, config-fabric authors (list and policy kinds only), and **intent authors**: the daemon pushes its trust set to the sidecar with the NIP-86 `setintentauthors` method, so an operator or organization member may publish `30900` intents without being a relay administrator.

Reads: with `read_auth_mode: enforce` (the default), a REQ or COUNT that targets a non-public kind or a protected `30900` topic — or a `30900` filter without `#t` — is answered with `CLOSED auth-required:` until the client authenticates as the service, an administrator, an intent author, or a `read_auth_allowed_pubkeys` entry, and NIP-11 advertises `auth_required`. `warn` logs instead of refusing; `off` disables read auth.

Retention: replaceable and addressable events keep latest-wins only; NIP-09 deletions are never swept; ephemeral kinds (`20000`–`29999`, including `25910` and `21059`) are broadcast and never stored. Sweeps run at startup and every 15 minutes. Send `SIGHUP` to reload a validated configuration file.

### NIP-86 relay administration

Bahia can administer its own or explicitly authorized relays over NIP-86 with NIP-98 payload-bound authorization. It is disabled by default and only targets listed here are ever called:

```yaml
nostr:
  relay_administration:
    enabled: true
    administrator_private_key_ref: "secret://relay-admin/sidecar"
    targets:
      - ref: "sidecar"
        relay_url: "wss://sidecar.example.com"
        http_url: "https://sidecar.example.com"      # optional
        authorization: "bahia_owned"                 # or "bahia_authorized"
        administrator_pubkeys: ["<64-hex>"]
```

Operators change relay policy from **Settings → Operator Relay Policy**, which publishes a `relay` intent; the daemon republishes the `30002`/`10050` relay events and a `relay-settings:operator` state record. The **Browser Session Relays** section is a local, non-canonical override stored only in that browser profile.

## Subscribing

Subscribe narrowly by author and single-letter tags, process history until `EOSE`, keep the subscription open, deduplicate by event id, and re-subscribe after `CLOSED` or reconnect.

```json
{"kinds": [30900], "authors": ["<service-pubkey>"], "#t": ["service-state"]}
{"kinds": [30315], "authors": ["<service-pubkey>"], "#p": ["<my-pubkey>"], "#t": ["intent-status"]}
{"kinds": [4903], "authors": ["<service-pubkey>"], "#t": ["cp-audit"], "#state": ["service:<id>:environment:<id>"]}
```

The browser keeps a persistent local event store. Pages render from the store first and show relay catch-up and degradation (`local-event-store`, missing EOSE, AUTH required) explicitly; an intent submitted while every relay is unreachable stays **pending** locally and is delivered on reconnect.

## HTTP surface

The daemon serves a small HTTP surface alongside the relays:

| Route | Purpose |
|-------|---------|
| `GET /health`, `GET /ready` | Liveness and readiness (readiness is `503` when any gating check fails) |
| `GET /metrics` | Prometheus metrics (authenticated when API auth is enabled) |
| `POST /mcp` | MCP JSON-RPC (platform admin) |
| `GET /api/v1/deployments/runs/{id}/logs` | Stored run logs from Blossom |
| `GET /api/v1/services/{id}/environments/{envId}/logs` | Live log stream (SSE) |
| `GET /api/v1/deployments/runs/{id}/cost`, `GET /api/v1/payments/history` | Payment reads from the local event store |
| `GET /api/v1/config-fabric/drift` | Config fabric drift (platform admin) |
| `GET /api/v1/blossom/blob/{hash}` | Content-addressed blob proxy (unauthenticated) |
| `POST`/`DELETE /api/v1/services/{id}/environments/{envId}/managed-instances/{unit}/maintenance` | Managed-instance maintenance override |
| `POST /api/v1/artifacts/{id}/sbom` | SBOM ingest |
| `POST /api/v1/soulfactory/legacy-reconciliation/{preview,apply}` | Reconcile pre-existing agent containers into Souls (platform admin) |
| `/v2/…` | OCI registry proxy |
| Virtualization routes | See [Virtual Machines](features/virtual-machines.md) |

HTTP requests authenticate with NIP-98 (`Authorization: Nostr <base64 kind-27235 event>` with `u` and `method` tags). Routes that need PostgreSQL return `503` when the daemon runs without it.

## Related

- [Core Concepts](core-concepts.md) — entities and coordinates
- [CLI Reference](cli-reference.md) — publishing intents from a terminal
- [MCP Tools](mcp-tools.md) — the same contract for agents
