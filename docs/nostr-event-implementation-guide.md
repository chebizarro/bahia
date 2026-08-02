# Bahia Nostr Event Implementation Guide

This guide is the Bahia-specific implementation policy for Nostr event kinds, event shapes, and Cascadia fleet interoperability. It adapts the Cascadia Nostr-native event strategy into rules that agents working in this repository can apply directly.

Use this guide before adding, publishing, subscribing to, decoding, migrating, or documenting any Nostr event.

## Core rule

Do not allocate or revive a Bahia-specific event kind just because a new semantic exists.

First ask whether the semantic is one of these:

1. a ContextVM JSON-RPC intent,
2. a NIP-51 collection,
3. a parameterized replaceable state object,
4. a NIP-38 status,
5. a NIP-58 badge or attestation,
6. a NIP-78 app data object,
7. a ContextVM discovery announcement,
8. an existing NIP.

If yes, use that mechanism. A new kind requires a written justification that its relay behavior, replaceability, retention, or indexing requirements differ from every existing mechanism.

## Bahia's four Nostr layers

| Layer | What it means | Bahia mechanism |
|---|---|---|
| Intent | A private command asking something to happen | ContextVM kind `25910`, usually wrapped with CEP-4/NIP-59 `1059` or `21059` |
| Observable | Public or scoped facts produced by execution | `30900` state, `4903` audit, `30315` status, and relevant standard NIPs |
| Collection | Replaceable sets such as memberships, relay sets, inventories, allowlists | NIP-51 kinds such as `30000`-`30004`, especially `30002` relay sets |
| State | Current snapshots, app config, registries, projections | `30900` for control-plane state and `30078` for app-specific data |

The normal flow is:

```text
ContextVM intent (private command)
  -> Bahia validates and executes
  -> canonical observable events (status, state, audit, app data)
  -> clients subscribe and converge from relay history + realtime events
```

The JSON-RPC response to a ContextVM request is only an acknowledgment or immediate error. Long-running completion is proved by observable events.

For encrypted ContextVM browser RPC, `control_plane.capabilities` may advertise `encrypted_controlplane.progress_ack` with `control_plane.wire_version="contextvm-jsonrpc-v2"`. In that mode, a routed and authorized request receives a no-`id` JSON-RPC notification (`method="notifications/progress"`, `params.status="processing"`) before handler execution. Treat it as liveness only: it clears the short ack deadline, never resolves terminal state, and must not be emitted for routing mismatches or unauthorized requests.

## Decision tree for implementers

### 1. Is this asking Bahia or an agent to do something?

Use ContextVM.

- Kind: `25910`
- Content: JSON-RPC 2.0
- Method: `<domain>/<operation>`
- Sensitive payloads: wrap the ContextVM message with CEP-4/NIP-59 kind `1059`, or `21059` when ephemeral gift-wrap is supported.
- Correlate retries with a stable idempotency key.

Examples:

| Operation | ContextVM method |
|---|---|
| Deploy service | `service/deploy` |
| Restart service | `service/restart` |
| Roll back service | `service/rollback` |
| Create DNS zone | `dns/zone-create` |
| Apply DNS policy | `dns/policy-apply` |
| Apply relay settings policy | `settings/relay-policy.apply` |
| Call managed relay administration method | `settings/relay-admin.call` |
| Run backup | `backup/run` |
| Restore backup | `backup/restore` |
| Cordon worker | `worker/cordon` |
| Promote package | `package/promote` |
| Scan adoption target | `adoption/scan` |
| Import adoption target | `adoption/import` |
| Request Security scan | `security/scan` |
| Request Security rescan | `security/rescan` |
| Read Security findings or schedules | `security/findings-list`, `security/schedules-list` |

Do not create request/status/result kind triplets for new operations.

### 2. Is this progress or current operational status?

Use NIP-38 status.

- Kind: `30315`
- Use `d` to identify the status coordinate.
- Include `status`, `domain`, and `schema` tags.
- Include `e` when the status is correlated with a ContextVM request or other source event.
- Include resource tags such as `service`, `environment`, `worker`, `artifact`, or `run` when available.

Status events are for short-lived operational state such as `running`, `healthy`, `degraded`, `available`, `draining`, `failed`, or `completed`.

Desired-state runtime deploys report additive step progression on existing status events without allocating new kinds or changing `d` coordinates. The expected service deploy steps are `building_desired_state`, `locking_environment`, `rendering`, `applying`, `observing`, and `projecting`. Operators and agents should treat these as progress breadcrumbs only; terminal truth still comes from the correlated ContextVM response plus canonical state/audit/status observables.

### 3. Is this durable current state or a read-model projection?

Use canonical state.

- Kind: `30900`
- Parameterized replaceable by `(kind, pubkey, d)`.
- `d` must be stable and scoped: `<domain>:<entity>:<id>` or the narrower convention already used by the feature.
- Required tags: `d`, `domain`, `schema`.
- Strongly recommended tags: `entity`, `status`, resource tags.
- Content must be a complete current-state snapshot, not a patch.

Use NIP-78 kind `30078` instead when the object is app-specific data, user/application settings, local UI state, or a registry whose semantics are not a fleet-wide control-plane projection.

Relay settings operator policy uses canonical state kind `30900` with `d=relay-settings:operator`, `domain=relay-settings`, and `schema=bahia.relay-settings.v1`. The state records the current service-authored browser, ContextVM, service, DM, NIP-66 monitor, and NIP-86 managed-target policy after a `settings/relay-policy.apply` ContextVM intent is accepted.

### 4. Is this an immutable audit fact or attestation?

Use Bahia audit.

- Kind: `4903`
- Required tags: `domain`, `type`, `schema`.
- Include `e` for source/correlation when possible.
- Include `p` for responsible or requesting actors where appropriate.
- Include resource tags such as `service`, `environment`, `artifact`, `worker`, `package`, `dns_zone`, or `run`.
- Audit events should be treated as protected and long-retention. They are not normal delete targets, but relay availability is still bounded by configured `event_retention`; compliance evidence needs appropriate retention or archival storage.
- `protected=true` is Bahia audit metadata. Projected audits currently omit the NIP-70 `-` tag; that tag governs authenticated author publication, not read visibility.

Use NIP-58 badges for permission or capability grants; use `4903` for the audit trail describing the grant or revocation.

### 5. Is this service, tool, resource, or prompt discovery?

Use ContextVM discovery.

| Kind | Purpose |
|---|---|
| `11316` | Server announcement |
| `11317` | Tools list |
| `11318` | Resources list |
| `11319` | Resource templates list |
| `11320` | Prompts list |

Use NIP-89 kind `31990` only when advertising application handler capability to the broader Nostr ecosystem. Do not use Bahia's old `31974` system discovery kind in runtime code.

Bahia system discovery is a protocol envelope, not an implementation detail. The server announcement that browsers accept is:

| Field | Required value |
|---|---|
| Kind | `11316` |
| `d` tag | `bahia-system-v1` |
| `schema` tag | `bahia.system-discovery.v1` |
| `name` tag | `Bahia` |
| Content `schema` | `bahia.system-discovery.v1` |

Browsers subscribe with narrow filters over trusted service authors and `#d` values for the announcement plus the relay sets they consume. Any change to these discovery tags, tag order, `d` coordinates, content schema, or the browser-required relay-set tags is a compatibility-impacting protocol change and requires PSTF evidence plus compatibility review before merging.

### 6. Is this relay topology or bootstrap routing?

Use existing relay-list NIPs and existing protocol relay hints. Bahia does not allocate relay-routing kinds.

- `30002`: NIP-51 relay sets for Bahia browser, ContextVM, service, and other service-authored relay-purpose groups. These remain Bahia's canonical bootstrap relay topology.
- `10002`: NIP-65 relay lists for general author relay preferences. Bahia publishes a service-authored advisory list with ContextVM request relays marked `read` and service publish/backfill relays marked `write`; it must not replace ContextVM discovery or NIP-51 relay sets.
- `10050`: DM relay lists only when direct-message routing is explicitly enabled for a Bahia feature and receiving identity; do not infer it from browser, ContextVM, or service relay sets.
- NIP-34 `30617` repository `relays` tags: repository/ngit routing hints for that repository only.
- NIP-11 metadata and optional NIP-66 monitor events: advisory relay capability/liveness inputs only; they do not establish Bahia service trust. NIP-66 `10166`/`30166` ingestion requires explicitly configured monitor pubkeys, uses scoped author and relay filters, and cannot add or remove configured relays.
- NIP-86: optional HTTP relay-owner administration with NIP-98 payload-bound authorization for explicitly configured Bahia-owned or Bahia-authorized relays. It is not ContextVM mutation transport and does not replace NIP-42 websocket AUTH.

Do not invent relay routing kinds.

Bahia relay-purpose taxonomy:

| Purpose | Owner | Canonical mechanism | Trust / exposure boundary |
|---|---|---|---|
| Public browser bootstrap/read models | Bahia service | NIP-51 `30002`, `d=bahia-browser-v1` | Public browser bootstrap boundary; sidecar public URL may be first by deployment policy. |
| ContextVM request/reply | Bahia service | NIP-51 `30002`, `d=bahia-contextvm-v1` | Preferred relay set for ContextVM mutation traffic; absence may fall back to browser relays with degraded metadata. |
| Service publish/backfill | Bahia service | NIP-51 `30002`, `d=bahia-service-v1`; advisory NIP-65 `10002` | Backend/service relay boundary; not automatically public browser bootstrap. |
| User/operator preferences | User/operator pubkey | NIP-65 `10002` | General author routing only; not service-strategy authorization. |
| Repository/ngit | Repository maintainer or SoulFactory | `nostr.nip34_relays`, NIP-34 `30617` `relays` tags, and `30618` state | Repository announcement discovery queries advertised NIP-34 relays when configured; branch/state lookups query repository-specific `relays` tags before global Bahia read relays. Missing NIP-34 relays remain a degraded fallback, not generic control-plane policy. |
| SoulFactory agent lifecycle | Operator, SoulFactory controller, runtime sidecar | ContextVM `25910` methods `soul-factory/provision` and `soul-factory/action`; canonical provisioning `30900`/`4903`; staged lifecycle kinds `31950`, `31951`, `31952`, `5950`, `6950`, `7950`, `1950`, `1951`, `30317`, `38384`, `38386` | New mutations enter through ContextVM and retain the request event id for correlation. Provisioning projects canonical state/audit; existing lifecycle events remain open interop while action and Soul read-model projection are completed. |
| DM receive routing | Receiving identity | NIP-51 `10050` | Explicit DM-enabled features and identities only; public bootstrap and ContextVM relay readiness do not imply DM readiness. |
| FIPS public adverts | FIPS/Bahia operator | Existing FIPS overlay advert contract plus explicit bridge relay config | Public advert boundary; safe only for information intentionally exposed as FIPS overlay metadata. |
| FIPS/Bahia endpoint/control | Bahia service/operator | ContextVM relay sets or explicit bridge relay config | Sensitive endpoint/control boundary; sharing with public relays is an explicit exposure decision. |
| Relay capability/liveness | Relay or trusted monitor | NIP-11; optional NIP-66 `10166`/`30166` | Advisory metadata; never overrides service pubkey trust or configured relay policy. |
| Relay administration | Bahia relay owner/operator | Optional NIP-86 over HTTP with NIP-98 auth | Administrative allow/ban/kind/metadata controls only; not application/control-plane mutation transport. |

### 7. Is this a list, membership, subscription, permission set, inventory, or registry of references?

Use NIP-51 collections.

Common Bahia conventions:

| Semantic | Preferred NIP-51 shape |
|---|---|
| Operators for a scope | kind `30000`, `d=operators:<scope>`, `p` tags |
| Approvers for a service/env | kind `30000`, `d=approvers:<service>:<environment>`, `p` tags |
| Relay bootstrap set | kind `30002`, `d=bahia-browser-v1`, `relay` tags |
| Watched repositories | kind `30001` or `30004`, stable `d`, `a`/`e`/URL tags as appropriate |
| Artifact or package inventory | kind `30004`, stable `d`, `a`/`e`/`r` tags |
| SBOM availability for one subject version | kind `30004`, `d=sbom:available:<subject-type>:<subject-key>`, `a` tags to SBOM reference events plus `sbom` summary tags |

Collections are updated by replacing the whole list. Delete collections with NIP-09 kind `5` when the list itself is removed.

SBOM availability lists are NIP-51 Curation Sets (`30004`), not Bahia-specific custom kinds. Each list is a complete replacement for one subject version and carries `domain=sbom`, `schema=bahia.sbom.available-list.v1`, `subject_type`, and `subject` tags. Each entry references the detailed `30078` SBOM reference app-data event with an `a` tag and includes an `sbom` tag containing subject digest, format, storage, location, payload hash, generator, and reference coordinate metadata.

### 8. Is this a permission, trust claim, certification, or capability grant?

Use NIP-58 badges.

- Kind `30009`: badge definition.
- Kind `8`: badge award.
- Use badge definitions for grants such as `can-deploy`, `can-approve`, `can-merge`, `can-sign`, `can-operate`, and `security-reviewed`.
- Scope badges with tags and content fields rather than creating one kind per permission.
- Emit `4903` audit events for security-relevant badge grants and revocations.

### 9. Is this a delete?

Use NIP-09 kind `5` for event deletion semantics.

For business-level deletes, publish a ContextVM method such as `service/delete`, then have Bahia publish canonical state showing tombstone/deleted status and any related audit fact. Do not create `DeleteFooKind` events.

## Canonical Bahia production kinds

These are the main event kinds production runtime code should publish or subscribe to for Bahia control-plane behavior.

| Kind | Name | Use |
|---:|---|---|
| `25910` | ContextVM message | JSON-RPC mutation intent and direct ContextVM responses |
| `1059` | NIP-59 gift wrap | Stored encrypted ContextVM envelope |
| `21059` | ephemeral gift wrap | Ephemeral encrypted ContextVM envelope when supported |
| `30315` | NIP-38 status | Operational status/progress; continuity heartbeat observations use `#domain=continuity`, `schema=bahia.status.continuity-heartbeat.v1`, and heartbeat `d`/`worker` tags |
| `30316` | Assistant transcript | Service-authored append-only assistant transcript entries; content is a service-held symmetric-key AEAD envelope with `key_ref`/rotation metadata mirrored in tags |
| `30900` | Cascadia/Bahia control state | Durable state/read-model projection |
| `4903` | Cascadia/Bahia audit | Immutable audit facts and attestations |
| `11316`-`11320` | ContextVM discovery | Server/tool/resource/prompt/template discovery |
| `30002` | NIP-51 relay set | Browser/ContextVM/service/operator relay topology |
| `30004` | NIP-51 Curation Set | SBOM availability lists and other curated reference inventories |
| `10002` | NIP-65 relay list | Advisory service relay preferences for wider Nostr routing |
| `30078` | NIP-78 app data | App-specific data, settings, registries, detailed projections; SBOM reference app-data uses `schema=bahia.sbom.ref.v1` |
| `30351`-`30353` | Continuity fabric observables | Continuity status, degraded-mode activation, and recovery progress; heartbeat observations are NIP-38 status kind `30315` with `#domain=continuity` |
| `31400`-`31404` | Continuity fabric definitions | Continuity profiles, failover policies, standby nodes, replication policies, and recovery workflows |
| `5` | NIP-09 deletion | Delete event references |

Other standard NIPs may be used directly when their semantics fit. Examples include NIP-58 badges, NIP-65 relay lists, NIP-89 app handlers, NIP-98 HTTP auth, NIP-70 protected events, and NIP-40 expiration.

## Runtime-prohibited legacy kinds

Legacy Bahia kind constants may still exist for migration inventory, fixture decoding, or historical documentation. They are not production runtime policy.

Production runtime code must not publish or newly subscribe to these legacy families, excluding the explicitly documented SoulFactory interop kinds `5950`, `6950`, `7950`, `1950`, `1951`, `30317`, `38384`, and `38386`:

- request kinds `5941`-`6006`, `38390`-`38399`, `38400`-`38431`, and older encrypted request/result kinds `5980`/`7980`
- status/result ranges `6941`, `6961`-`6997`, `7941`-`7997`
- old state/read-model ranges `31410`-`31411`, `31961`-`32003`, and old heartbeat kind `30350`; continuity fabric kinds `30351`-`30353` and `31400`-`31404` plus NIP-38 heartbeat status kind `30315` are canonical runtime observables
- old audit ranges `31000`-`31024`, `31310`-`31311`
- old discovery kind `31974`
- legacy SBOM index kind `30079`; new SBOM availability publication uses NIP-51 kind `30004` and detailed SBOM references use NIP-78 kind `30078`

If such a kind appears in production code, it must be one of these explicitly justified cases:

1. read-only legacy migration code in `internal/nostrmigration`,
2. tests proving legacy kinds are transformed or rejected,
3. documentation describing historical behavior,
4. metadata tag values such as `legacy_kind` on a canonical migrated event.

The presence of a `legacy_kind` tag does not make the event legacy. The event's `kind` field must still be canonical.

## Required event shapes

### ContextVM intent

Unencrypted example for local/dev or non-sensitive traffic:

```json
{
  "kind": 25910,
  "tags": [
    ["p", "<bahia-service-pubkey>"],
    ["domain", "service"],
    ["op", "deploy"],
    ["schema", "bahia.intent.service.v1"],
    ["d", "deploy-api-prod-01"]
  ],
  "content": "{\"jsonrpc\":\"2.0\",\"id\":\"deploy-api-prod-01\",\"method\":\"service/deploy\",\"params\":{\"service\":\"api\",\"environment\":\"prod\",\"artifact\":\"api:v2.4.0\"}}"
}
```

Sensitive production traffic should encrypt that message as a NIP-59 gift wrap:

```json
{
  "kind": 1059,
  "pubkey": "<random-wrapper-pubkey>",
  "tags": [["p", "<bahia-service-pubkey>"]],
  "content": "<NIP-44 encrypted inner ContextVM event>"
}
```

After unwrap, Bahia must verify the inner event signature and authorize the inner sender before executing.

### NIP-38 status

```json
{
  "kind": 30315,
  "tags": [
    ["d", "service:deploy:deploy-api-prod-01"],
    ["domain", "service"],
    ["schema", "bahia.status.service.v1"],
    ["status", "running"],
    ["service", "api"],
    ["environment", "prod"],
    ["e", "<contextvm-request-event-id>"]
  ],
  "content": "{\"step\":\"rolling-update\",\"message\":\"deployment started\"}"
}
```

### Canonical state projection

```json
{
  "kind": 30900,
  "tags": [
    ["d", "service:state:api:prod"],
    ["domain", "service"],
    ["entity", "service-state"],
    ["schema", "bahia.service-state.v2"],
    ["service", "api"],
    ["environment", "prod"],
    ["status", "healthy"]
  ],
  "content": "{\"service\":\"api\",\"environment\":\"prod\",\"desired_hash\":\"...\",\"observed_hash\":\"...\"}"
}
```

Projection decoders must reject canonical events with an empty or missing family/entity coordinate. A valid `30900`, `30315`, `4903`, or `30078` event must decode into an explicit projection family.

Desired-state runtime metadata is additive on existing service/deployment observables. Bahia may include `desired_hash`, `renderer`, `target`, environment or unit revision metadata, runtime target metadata, apply metadata summaries, and `observation_id` when available. Decoders must ignore unknown fields and tags. Secret plaintext, raw Docker hosts, Docker TLS material, and generated Compose env-file contents must not appear in public Nostr content or tags; only redacted secret refs or key-presence metadata may be projected.

### Audit fact

```json
{
  "kind": 4903,
  "tags": [
    ["domain", "service"],
    ["type", "deployment"],
    ["schema", "bahia.audit.deployment.v1"],
    ["service", "api"],
    ["environment", "prod"],
    ["artifact", "api:v2.4.0"],
    ["e", "<contextvm-request-event-id>"],
    ["p", "<operator-pubkey>"]
  ],
  "content": "{\"action\":\"deploy\",\"result\":\"accepted\"}"
}
```

### ContextVM discovery

```json
{
  "kind": 11316,
  "pubkey": "<bahia-service-pubkey>",
  "tags": [
    ["name", "Bahia"],
    ["support_encryption"],
    ["support_encryption_ephemeral"],
    ["d", "bahia-contextvm-v1"]
  ],
  "content": "{\"protocolVersion\":\"2025-07-02\",\"serverInfo\":{\"name\":\"bahia\",\"version\":\"...\"},\"capabilities\":{\"tools\":{\"listChanged\":true}}}"
}
```

SBOM reference app-data uses NIP-78 `30078` with `domain=sbom`, `schema=bahia.sbom.ref.v1`, and a stable `d` coordinate of `sbom:ref:<subject-key>:<format>:<payload-sha256>`. The content is the in-toto-style SBOM attestation envelope, not the SBOM payload bytes. Required routing and validation tags include `subject_type`, `subject`, `format`, `storage`, `location`, `x=<payload-sha256>`, `media_type`, `generator`, and `ntia`; publishers must verify relay `OK` acceptance before treating the reference as published.

SBOM availability uses NIP-51 `30004` with `domain=sbom`, `schema=bahia.sbom.available-list.v1`, and `d=sbom:available:<subject-type>:<subject-key>`. The event is a complete replacement list for one subject version. It includes `a` tags to the corresponding `30078` reference coordinates and `sbom` summary tags. Historical `30079` SBOM index events are read-only migration data and must not be used for new publication.

Security uses the same decision tree without allocating a new kind:

- Explicit scan/rescan and read intent: ContextVM `25910` methods `security/scan`, `security/rescan`, `security/findings-list`, and `security/schedules-list`, usually wrapped with `1059` or `21059` for sensitive target or policy data.
- Progress: NIP-38 `30315` with `domain=security`, `schema=bahia.status.security-scan.v1`, and `d=security:scan:<run_id>`.
- Current state: `30900` with `schema=bahia.security.scan-summary.v1` for per-run summaries and `schema=bahia.security.target-summary.v1` for latest target summaries.
- App-specific details: NIP-78 `30078` with `schema=bahia.security.findings.v1` for normalized public-safe findings.
- Audit and policy breach evidence: `4903` with `schema=bahia.audit.security.v1`.

Security scans triggered by SBOM production subscribe to existing SBOM `30078` reference and `30004` availability events with exact `#domain=sbom`, `#schema`, subject, and service-author filters. The scanner treats `EOSE` as historical catch-up completion, keeps realtime subscriptions open when needed, handles `CLOSED` and `AUTH`, verifies inbound event signatures and hashes before trust, and verifies relay `OK` for every Security observable it publishes.

Relay topology is separate and should be published as NIP-51 relay sets:

```json
{
  "kind": 30002,
  "tags": [
    ["d", "bahia-browser-v1"],
    ["relay", "wss://relay.example.test"]
  ],
  "content": ""
}
```

## Cascadia fleet interoperability

Bahia should interoperate with Cascadia fleet applications by speaking standard Nostr mechanisms first and Bahia-specific schemas second.

Use these interop defaults:

| Need | Fleet mechanism |
|---|---|
| Command another agent/app | ContextVM `25910` JSON-RPC method, encrypted when sensitive |
| Discover agent/app capabilities | ContextVM `11316`-`11320`; NIP-89 `31990` for app-handler discovery |
| Discover relays | NIP-51 `30002`, NIP-65 `10002`, and DM relay lists `10050` where appropriate |
| Track operational status | NIP-38 `30315` |
| Track state/read models | `30900` with shared `domain`, `entity`, and `schema` tags |
| Track app data/config | NIP-78 `30078` |
| Manage memberships/inventories | NIP-51 lists |
| Represent permissions/capabilities | NIP-58 badges, plus audit `4903` for security-relevant changes |
| Delete event references | NIP-09 `5` |

Common tags across fleet events:

- `domain`: routing and ownership area (`service`, `dns`, `backup`, `worker`, `package`, `ml`, `adoption`, `policy`, `security`, `soul-factory`)
- `schema`: content schema identifier; consumers must check it before parsing
- `d`: replaceable coordinate or idempotency/correlation key when applicable
- `e`: source or parent event id
- `p`: actor, requester, recipient, or relevant pubkey
- resource tags: `service`, `environment`, `worker`, `artifact`, `package`, `run`, `project`, `workflow`

Do not rely on relay indexing for multi-character tags unless the sidecar or target relay explicitly supports it. Still include semantic tags for consumers and internal sidecar indexing.

## Publication, replay, and retention invariants

- For service-authored events using `internal/adapters/nostr.Publisher`, persist the fully signed event as a pending `nostr_events` outbox row before relay delivery. Mark it published only after an accepted or duplicate relay `OK`; retain and retry failures with backoff.
- Do not generalize that outbox guarantee to every relay pool or client publisher. A caller request with zero accepted relays is not accepted, and a ContextVM receipt is not terminal business truth.
- Sidecar persistence precedes `OK`. Subscriber fanout must remain off the acknowledgment path so a slow subscriber cannot stall writes.
- Replay filters for IDs, kinds, authors, `since`, and `until` should be scoped in storage before full filter matching. Keep replay reads isolated from the write connection and enforce `max_query_limit`; `EOSE` ends only the bounded query, so clients that may hit the cap must narrow resource/time filters, overlap windows, and deduplicate.
- Retain ContextVM transport (`25910`, `1059`, `21059`) according to `request_retention`; retain observables and all other kinds according to `event_retention`.

## Migration app rules

Bahia has already deployed older events. Legacy events are migrated by a startup migration module rather than by keeping legacy runtime behavior alive.

Implementation rules:

1. Legacy subscriptions, decoders, and transforms belong in `internal/nostrmigration` or tests for that module.
2. The migration must be idempotent. Re-running startup must not duplicate canonical events.
3. Migrated events must publish canonical `kind` values and may include metadata tags such as `legacy_kind`, `migrated-from`, `migration`, and `schema`.
4. Runtime publishers and subscribers should not include legacy kind support just to ease rollout.
5. The relay sidecar accepts every valid Nostr event kind. Canonical-versus-legacy distinctions are application semantics enforced by Bahia consumers, never relay admission policy.
6. If a new migration transform is added, update `docs/control-planes.md`, `docs/event-spec.md`, `docs/nostr-commands.md`, `docs/protocol-compatibility.md`, and the PSTF verification evidence for the migration feature.

## Implementation checklist

Before adding or changing Nostr event code:

- [ ] Use `internal/kinds/kinds.go` rather than hand-written numeric constants.
- [ ] Confirm the kind is canonical production policy or explicitly migration-only.
- [ ] Verify that consumers validate authors, signatures, encryption, tags, and application semantics without relying on relay admission.
- [ ] Add or update event shape tests for required tags, content schema, and projection family decoding.
- [ ] Add idempotency and dedupe behavior for handlers.
- [ ] Verify relay `OK`, duplicate-`OK`, `CLOSED`, and `AUTH` paths; verify outbox state only when using the outbox-backed publisher.
- [ ] Subscribe with scoped filters and handle EOSE as historical catch-up, not completion.
- [ ] Update docs when event kinds, tags, schemas, or migration behavior change.
- [ ] Create Beads for deferred work rather than leaving comments or TODOs.

If the implementation wants a new event kind, stop and write the kind-allocation justification first. In most cases the correct fix is a ContextVM method, NIP-51 list, NIP-38 status, `30900` projection, `30078` app data event, NIP-58 badge, or ContextVM discovery announcement.
