# Bahia Control Planes

Bahia is operated through signed Nostr events. This document describes the
surfaces a client can use, which relays each one talks to, and how each is
authorized. Event shapes are in the [event specification](event-spec.md);
the HTTP routes are in the [HTTP reference](api.md); the relay sidecar in
[its own document](relay-sidecar.md).

## Surfaces

| Surface | Transport | Used by | Writes | Reads |
|---|---|---|---|---|
| Relay subscriptions | NIP-01 REQ against the relay sidecar and configured relays | web, CLI, MCP, the daemon, agents | — | all canonical state, status, audit, discovery |
| Signed intents | kind `30900` `t=bahia-intent` published to the ContextVM relay set | web, CLI (`bahia …` mutation commands), MCP tools (in-process) | control-plane entity mutations | — |
| ContextVM RPC | kind `25910` gift-wrapped to the service pubkey | web (secret reveal, assistant), CLI (`bahia logs run`) | — | secret reveal, run-log fetch, assistant turns |
| MCP | JSON-RPC over HTTP at `POST /mcp` | agents and tooling with an NIP-98 signer | intent tools (signed in-process by the caller's key) | store-read tools answered from the daemon's local event store |
| HTTP | `GET /health`, `GET /ready`, `/metrics`, `/v2/*` (OCI), a small set of `/api/v1` routes | probes, Docker clients, browsers downloading blobs, operators | SBOM ingest, maintenance windows, legacy-agent reconciliation | virtualization resources, logs, payments, config-fabric drift, Blossom blobs |

The authority model (who may sign what, how the daemon admits an intent) is
in [intents and authority](architecture/intents-and-authority.md); the
client designs are in [web store-first](architecture/web-store-first.md)
and [CLI and MCP](architecture/cli-and-mcp.md).

There is no HTTP route that creates, updates or deletes a control-plane
entity. The web, the CLI and MCP all end in a signed `30900` intent; the
daemon's reply is a bounded `30315` status; the durable outcome is the
canonical record a REQ returns.

### How a write completes

1. The client mints the entity id (UUIDv7) and signs the intent with its own
   key (NIP-07, NIP-46 or a local key file).
2. The relay `OK` is delivery, not completion. The CLI and web wait for it and
   then watch the requester-scoped `30315` status
   (`intent-status:<pubkey>:<coordinate>`).
3. `accepted` or `rejected` arrives with any daemon-authored output in `data`.
4. The canonical record (`30900` family, `4903` facts, domain status) is the
   proof that the system reached the desired state. Clients that already hold
   the family in their local store see it update live.

MCP tools return `{"status": "pending|accepted|rejected|conflict|error",
"intent_id", "event_id", "status_kind": 30315, "status_coordinate", …}` and,
when the record is already visible in the local store, the resulting state.

## Relays and their purposes

Relay URLs are physical endpoints; purpose is policy. One URL may serve
several purposes, but each purpose has its own configuration key, its own
`30002` relay set and its own trust boundary.

| Purpose | Configuration | Advertised as | Boundary |
|---|---|---|---|
| Browser bootstrap and read models | `nostr.browser_relays` | `30002` `d=bahia-browser-v1` | public read models; required when the sidecar is enabled |
| Intents and ContextVM | `nostr.contextvm_relays` (falls back to browser relays) plus the sidecar URL | `30002` `d=bahia-contextvm-v1`; `10002` `read` entries | where the daemon subscribes for intents and RPC and publishes replies |
| Service publication and backfill | `nostr.service_relays` (`nostr.relays` supplies the list when this key is empty) | `30002` `d=bahia-service-v1`; `10002` `write` entries | where the daemon's own records go beyond the sidecar |
| Sidecar | `nostr.sidecar.backend_url` (daemon side), `nostr.sidecar.public_url` (clients) | — | the daemon publishes canonical observables to the sidecar pool |
| Loom workers | `loom.relays` | — | worker advertisements, job requests, status and results |
| NIP-34 repositories | `nostr.nip34_relays` plus the repository's own `30617` relay hints | — | repository operations prefer the repository's hints |
| DM delivery | `nostr.dm_relay_lists[]` with `identity: service` | `10050` | only for explicitly configured DM features |
| FIPS overlay | `fips.relay_urls` | — | overlay adverts and endpoint/control traffic |
| SoulFactory | `soul_factory.relays`, `additional_relays`, `nip05_relays` | — | agent lifecycle traffic |
| Relay administration | `nostr.relay_administration.targets[]` | — | NIP-86 over NIP-98; `bahia_owned` targets receive the intent-author sync |
| Relay capability and liveness | `nostr.trusted_relay_monitor_pubkeys` | — | NIP-11/NIP-66 are advisory; never a trust root |

When `nostr.sidecar.mirror_external` is true the sidecar is the mirror
boundary for `nostr.relays` and the daemon does not also connect to those
URLs directly. Relays that require NIP-42 AUTH for an operation the daemon has
no usable signer for are excluded from that operation and the operation fails
if the remaining relays cannot satisfy its success rule
(`nostr.relay_auth_unavailable: exclude_and_fail`, the only value).

Readiness tracks relay health against `nostr.relay_quorum`
(`full_min_healthy: 2`, `degraded_min_healthy: 1`, `emergency_min_healthy: 1`
by operating `mode`).

## Authorization

### Signers

- **Operators** sign with a Nostr key: NIP-07 in the browser, NIP-46 through
  a bunker (`--nostr-bunker-file` + `--nostr-client-key-file`, optionally
  `--nostr-bunker-relay`), or a key file (`--nostr-key-file`). NIP-46 signs
  both events and NIP-42 AUTH challenges; the client key is session material,
  not the operator identity.
- **The daemon** signs with `nostr.private_key` (the service pubkey). Every
  canonical record, status, audit fact and discovery event is authored by it.
- **Workers, agents and sidecars** sign with their own keys and are admitted
  by the allowlist of the surface they use.

### Who may do what

| Decision | Source of truth |
|---|---|
| Fleet operators (fleet-scoped intents, assistant RPC, MCP tool calls outside intents, continuity definitions) | `nostr.authorized_pubkeys`; published as the fleet-OCK `operators:continuity` record. An empty list denies every fleet-scoped request. |
| SoulFactory document authors | `soul_factory.authorized_pubkeys`; published as `operators:soul-factory` |
| Adoption scan and import | `adoption.enabled` plus `adoption.allowed_pubkeys` (cumulative with the fleet list) |
| Direct runtime deploy/restart/stop | `direct_runtime_actions.enabled` plus `direct_runtime_actions.allowed_pubkeys` (cumulative) |
| Org membership and roles | `org-member` records (OCK-encrypted `30900`), loaded into the daemon's TrustSet; `auth.bootstrap_owner_pubkeys` may create organizations and act as platform owners |
| Loom workers | `loom.authorized_pubkeys` |
| Relay write admission | the sidecar's NIP-86 policy file (`administrator_pubkeys`, allowed and banned sets) plus the intent-author set the daemon pushes with `setintentauthors` |
| Relay read admission (protected topics) | service pubkey, NIP-86 administrators and allowed pubkeys, intent authors, `nostr.sidecar.read_auth_allowed_pubkeys` |
| MCP and HTTP callers | NIP-98 `Authorization: Nostr <base64 event>` when `auth.enabled` is true; `Bearer` tokens are rejected with `401` |

Allowlists hold 64-hex pubkeys; they are trimmed, lowercased and
deduplicated at load, and enabling adoption or direct runtime actions without
at least one pubkey fails config load. Subject and email allowlists cannot
authorize signed requests.

### Sensitive domains

Intents for `org`, `secret`, `notification` and `relay` must be gift-wrapped
(`1059`) to the service pubkey; plaintext intents for these domains are
rejected. Their state records are encrypted with the org content key (OCK) or
the fleet OCK, so relays and non-members hold only ciphertext. Secret
plaintext leaves the daemon only through the `services/secrets-reveal`
ContextVM reply, which is encrypted to the requester.

## Interop boundaries

- **Hive-CI**: `build/request` is an intent; the CI bus is Hive-CI's
  `5401`/`5402`. Operators must trust the service pubkey as a CI issuer for
  self-dispatched runs.
- **Loom**: deployments executed by workers use Loom's own kinds; Bahia
  publishes run health as `30315` and, when `loom.canonical_projection` is
  enabled, job state as `30900`/`4903`.
- **SoulFactory**: provisioning and runtime control use the SoulFactory kinds
  directly; Bahia projects provisioning progress (`soul-factory:provisioning:
  <request-event-id>`) and saga state into canonical records.
- **Config Fabric**: NIP-51 `30000` membership lists and NIP-78 `30078`
  relay-sidecar documents from `nostr.sidecar.config_trusted_pubkeys` are
  applied by the sidecar, which publishes `config-status` receipts.

## Operator clients

- **CLI** (`bahia`): relay resolution is `--relay`, then
  `BAHIA_NOSTR_RELAYS`, then trusted-operator discovery from
  `--bootstrap-relay` / `BAHIA_NOSTR_BOOTSTRAP_RELAYS` and
  `--trusted-service-pubkey`. Mutation commands publish intents through a
  local outbox (`bahia outbox`) and follow `30315`; table-mode progress goes to
  stderr so JSON/YAML stdout stays machine-readable. There is no HTTP
  fallback. See the [CLI reference](user-guide/cli-reference.md).
- **Web**: bootstraps from the runtime seed (`PUBLIC_BAHIA_BOOTSTRAP_RELAYS`,
  `PUBLIC_BAHIA_SERVICE_PUBKEYS`), discovers relay sets, hydrates its
  IndexedDB store from REQs and stays live on subscriptions. Protected
  topics hydrate after the operator signs in and answers the sidecar's
  NIP-42 challenge. See [web app setup](web-app-setup.md).
- **MCP**: tool names, arguments and auth are in the
  [MCP tools reference](user-guide/mcp-tools.md).
