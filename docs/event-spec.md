# Bahia Nostr Event Specification

This is the authoritative description of every Nostr event Bahia publishes,
consumes or relays. Code is the source of truth where a detail is not stated
here: kind numbers and topics live in `internal/kinds`, intent parsing in
`internal/controlplane/intent_processor.go`, the sidecar's read/write policy in
`internal/relaysidecar`, and the Go-generated wire fixtures under
`web/tests/fixtures/*intent*.json`.

How to choose a mechanism for a new semantic is in the
[implementation guide](nostr-event-implementation-guide.md). The transports,
authorization and surfaces that carry these events are in
[control planes](control-planes.md).

## 1. Layers

| Layer | Meaning | Kind |
|---|---|---|
| Intent | A client-signed desired state or request | `30900` with `t=bahia-intent`, gift-wrapped in `1059`/`21059` for sensitive domains |
| Intent status | Bounded, requester-scoped acceptance or rejection | `30315` with `t=intent-status` |
| Interactive RPC | Request/response that is not state: assistant turns, secret reveal, run-log fetch | ContextVM `25910`, usually inside `1059`/`21059` |
| State | Current control-plane records, latest-wins by coordinate | `30900` (`schema=bahia.cp-state.v1`), `30078` app data, `30004` curation sets |
| Status | Operational status and progress | `30315` (NIP-38) |
| Audit | Immutable facts and attestations | `4903` |
| Transcript | Encrypted assistant transcript entries | `30316` |
| Discovery | Bootstrap and capability advertisement | `11316`–`11320`, `30002`, `10002`, `10050` |
| Documentation | Published user guide | `30023` (`30024` drafts) |
| Deletion | NIP-09 deletion requests | `5` |

The authority model behind this is in
[intents and authority](architecture/intents-and-authority.md); delivery
guarantees are in [outbox delivery](architecture/outbox-delivery.md).

The flow for every write is the same: the client signs an intent, a relay
`OK` proves delivery, the daemon answers with a bounded `30315` status, and the
canonical outcome is the `30900`/`4903`/`30315` records the daemon's own
publishers emit. A status or RPC reply is never the answer to "what is the
current state of X"; that answer is a REQ against canonical state.

## 2. Kind catalog

### Bahia canonical kinds

| Kind | Role | Notes |
|---|---|---|
| `30900` | Control-plane state and intents | Addressable. `t=bahia-intent` marks an intent; every other record carries a family `#t` topic (§5) |
| `30315` | Operational status, intent status | Addressable (NIP-38) |
| `4903` | Audit facts and attestations | Regular; never carries `d` |
| `30316` | Assistant transcript | Addressable; `content` is a service-held AEAD envelope |
| `30078` | NIP-78 app data: SBOM references, config-fabric documents, security finding details | Addressable |
| `30004` | NIP-51 curation set: SBOM availability lists | Addressable |
| `30318` | Dashboard widget (NIP-CAS-0009), published by agents and rendered read-only by the web widgets wall | Addressable; publishers are allowlisted by `PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS` |
| `25910` | ContextVM JSON-RPC | Ephemeral; never stored |
| `1059` / `21059` | NIP-59 gift wrap (stored / ephemeral) | Wraps `25910` and sensitive `30900` intents |
| `11316`–`11320` | ContextVM server, tools, resources, resource-templates, prompts announcements | Replaceable |
| `30002` | NIP-51 relay sets: `d=bahia-browser-v1`, `bahia-contextvm-v1`, `bahia-service-v1` | Addressable |
| `10002` | NIP-65 service relay preferences (advisory) | Replaceable |
| `10050` | NIP-51 DM relay list, only for explicitly configured DM features | Replaceable |
| `30023` / `30024` | Long-form user guide pages published by `internal/docs` | Addressable |
| `30360`, `31410`, `31411` | Readiness status, identity definition, replay checkpoint | Addressable |
| `30351`–`30353`, `31400`–`31404`, `38430`, `38431` | Continuity status, degraded-mode, recovery progress; continuity profile, failover policy, standby node, replication policy, recovery workflow; failover and recovery requests | Operator-authored definitions are accepted only from `nostr.authorized_pubkeys` |
| `5` | NIP-09 deletion | Applied by the sidecar for the requester's own events and coordinates |

### Interop kinds Bahia reads or writes

| Protocol | Kinds | Direction |
|---|---|---|
| Loom | `10100` worker advertisement; `5100` job request; `30100` job status; `5101` job result; `5102` cancellation | advertisement/status/result inbound, request/cancellation outbound |
| Hive-CI | `5401` workflow run; `5402` workflow result | run outbound (self-dispatch) and inbound; result inbound |
| NIP-34 | `30617`, `30618`, `1617`–`1619`, `1621`, `1630`–`1633`, `10317`; replies `1111` | read through the sidecar as open interop |
| SoulFactory | `31950` template, `31951` soul, `31952` draft, `31953` fleet config; `5950`/`6950`/`7950` provisioning request/status/result; `1950` action, `1951` action result; `30317` runtime capability; `38384`/`38386` runtime control/result | exchanged directly with SoulFactory reactors and runtimes |
| Cascadia | `30000` NIP-51 lists (`service:<id>:membership` relay allowlists) | consumed by the sidecar config consumer |

The functions `kinds.IsRequestKind`, `kinds.IsCanonicalObservableKind` and
`kinds.IsOpenInteropKind` define exactly which kinds the daemon accepts as
requests, publishes as observables and exposes as interop. Numeric constants in
`internal/kinds` that are not in those sets exist only for
`bahia-migrate nostr` (§12) and fail-closed tests; nothing publishes or
subscribes to them.

## 3. Intents (`30900`, `t=bahia-intent`)

An intent is a client-signed addressable event. The daemon parses it with
`controlplane.ParseIntent`:

```json
{
  "kind": 30900,
  "pubkey": "<operator-pubkey>",
  "tags": [
    ["d", "<entity coordinate>"],
    ["t", "bahia-intent"],
    ["domain", "service"],
    ["op", "update"],
    ["schema", "bahia.intent.service.v1"],
    ["org", "<org-uuid>"],
    ["intent_id", "<replay key>"]
  ],
  "content": "{\"id\":\"<service-uuid>\",\"name\":\"api\",\"expected_updated_at\":\"2026-09-30T12:00:00.123456Z\"}"
}
```

Rules:

- `d`, `domain`, `intent_id` and `t=bahia-intent` are required. `op` defaults
  to `update`. `schema` is `bahia.intent.<domain>.v1`.
- `org` is required except for fleet-scoped domains (`dns`, `ml`, `worker`,
  `adoption`, `tool`, `security`, `sbom`, `relay`) and `org/rekey` of the
  fleet scope.
- `intent_id` is the idempotency key. A replay with the same id and content is
  acknowledged again without re-execution; the same id with different content
  is rejected as a conflict.
- `content.expected_updated_at`, when present, is the canonical record's
  RFC 3339 `updated_at` string (microsecond precision). A numeric epoch is
  rejected; a mismatch is rejected before any state changes.
- Content is the full desired state (level-triggered), not a diff.
- The daemon authorizes the signer through its TrustSet: per-org RBAC for org
  domains, `nostr.authorized_pubkeys` for fleet-scoped domains, and bootstrap
  owners for org creation. Unauthorized or malformed intents receive a
  `rejected` status.
- Intents for the sensitive domains `org`, `secret`, `notification` and
  `relay` must arrive as a NIP-59 `1059` gift wrap addressed to the service
  pubkey (`p` tag) whose rumor is the signed `30900`; a plaintext intent for
  these domains is rejected.
- `nostr.intent_domains_disabled` turns a domain off; all registered domains
  are on by default.

### Domains and operations

| Domain | Operations |
|---|---|
| `service` | `create`, `update`, `delete` |
| `environment` | `create`, `update`, `delete`, `worker-policy-apply` |
| `policy` | `create`, `update`, `delete`, `evaluate` |
| `deployment` | `create`, `approve`, `reject`, `rollback`, `preview`, `route-attach` |
| `runtime` | `deploy`, `restart`, `stop` |
| `llm` | `create`, `release-register`, `deploy`, `approve`, `reject`, `rollback` |
| `ml` | `model-create/update/delete`, `version-create/update/delete`, `endpoint-create/update/delete`, `model-import`, `recipe-apply`, `recipe-run`, `inference-deploy`, `inference-approval`, `inference-rollback`, `pin` |
| `backup` | `repository-register`, `repository-probe`, `run`, `restore`, `restore-approval`, `verify`, `retention-enforce`, definition/policy/recipe/retention apply |
| `package` | `repository-apply`, `repository-delete`, `publish`, `promote`, `yank`, `drift-detect` |
| `dns` | `zone-create/update/delete`, `backend-create/update/delete`, `endpoint-create/update/delete`, `policy-apply/update/delete`, `record-set`, `override-retire`, `drift-remediate` |
| `worker` | `cordon`, `uncordon`, `drain`, `undrain`, `maintenance-enter`, `maintenance-exit`, `labels-update`, `cleanup` |
| `org` | `create`, `update`, `rekey`; member and invite records under `org:member:` and `org:invite:` coordinates |
| `secret` | `create`, `update`, `delete` |
| `notification` | `create`, `update`, `delete`, `channel-test` |
| `artifact` | `register`, `import-observed`, `register-build-result`, `signature-verify` |
| `adoption` | `scan`, `import` |
| `build` | `request` |
| `tool` | `approval-response` |
| `security` | `scan-run` |
| `sbom` | `generate`, `import` |
| `relay` | `policy-set` |

Exact coordinates, content and permissions for each operation are in the
Go-generated fixtures (`web/tests/fixtures/deployment-intents.json`,
`d70-`, `d76-`, `d79-`, `d80-intent-content.json`, `llm-`, `package-` and
`backup-intents.json`). Desired-state operations (`create`, `update`, apply
verbs) are upserts of the full record; request operations (`scan`, `request`,
`run`, `evaluate`, `preview`, `channel-test`, …) produce daemon-authored output
that is carried only in the status `data`.

### Intent status (`30315`, `t=intent-status`)

The daemon publishes one replaceable status per requester and coordinate:

```json
{
  "kind": 30315,
  "pubkey": "<service-pubkey>",
  "tags": [
    ["d", "intent-status:<requester-pubkey>:<entity coordinate>"],
    ["domain", "intent"],
    ["status", "accepted"],
    ["t", "intent-status"],
    ["p", "<requester-pubkey>"],
    ["intent_id", "<replay key>"],
    ["expiration", "<unix seconds>"],
    ["e", "<intent event id>"]
  ],
  "content": "{\"intent_id\":\"…\",\"coordinate\":\"…\",\"result\":\"applied\",\"data\":{…}}"
}
```

`status` is `accepted` or `rejected`; `reason` explains a rejection; `data`
carries request output (a scan page, a build id, an org rekey summary,
`result=evaluated` with an `evaluation` object for policy evaluation). The
content is bounded (16 KiB for evaluations; adoption pages are byte-bounded
and redacted). The status is liveness and acceptance only; completion of
long-running work is proved by canonical state.

### Organization rekey

`domain=org`, `op=rekey`, content `{"org_id":"<uuid>","reason":"…"}`,
gift-wrapped like other org intents. An owner or admin may request it; the
fleet scope uses `org_id="fleet"`, no `org` tag and a fleet-operator signer.
The daemon distributes the new org content key, then republishes every
confidential record of that scope on its existing coordinate. The accepted
status reports `{"key_version":"v<n>","records_republished":<n>}`. An
organization record's `strict_revocation` flag (default `false`) makes member
removal and role downgrade trigger the same refounding.

## 4. Interactive RPC (ContextVM `25910`)

ContextVM is used only for interactions that are inherently request/response
and not state. The daemon registers and advertises exactly these methods:

| Method | Purpose | Gate |
|---|---|---|
| `assistant/prompt` | Start an assistant turn | fleet operator |
| `assistant/approval` | Decide a plan or action | fleet operator |
| `assistant/cancel` | Stop a run | fleet operator |
| `assistant/reconcile` | Account for a run | fleet operator |
| `services/secrets-reveal` | Return one secret's plaintext | org RBAC |
| `deployments/run-logs-get` | Return stored stdout/stderr of a run | org RBAC |

The daemon also uses the `maintenance/*` methods (`scan`, `report`,
`quarantine`, `restore`, `relocate`, `purge`, `gc`, `pressure`) as the
hygiene RPC between the daemon and workers; both directions are NIP-59
`1059` wraps and host paths exist only in the rumor.

Request: a signed `25910` whose content is a JSON-RPC 2.0 request
(`method`, `params`, `id`, optional `params._meta.progressToken`), with a
`p` tag naming the service pubkey, wrapped in `1059` (stored) or `21059`
(ephemeral; used when the wrap would exceed the sidecar's 65535-byte content
limit). The reply keeps the wrapper lifetime of the request and correlates to
the outer request with `e=<outer-request-id>,reply` and `p=<requester>`.

A routed and authorized request first receives a JSON-RPC notification
without `id` (`method="notifications/progress"`, `params.status="processing"`)
— discovery advertises this as capability `encrypted_controlplane.progress_ack`
with `wire_version="contextvm-jsonrpc-v2"`. Unauthorized requests receive the
terminal error without an ack; routing mismatches stay silent.

## 5. Control-plane state (`30900`, `schema=bahia.cp-state.v1`)

Every canonical record is one signed addressable event authored by the
service pubkey:

```json
{
  "kind": 30900,
  "pubkey": "<service-pubkey>",
  "tags": [
    ["d", "<coordinate>"],
    ["t", "service-state"],
    ["domain", "service"],
    ["entity", "state"],
    ["schema", "bahia.cp-state.v1"],
    ["legacy_kind", "31961"],
    ["deleted", "false"]
  ],
  "content": "{…}"
}
```

- `t` is the family topic. Relays index single-letter tags only, so REQs
  scope by `authors` plus `#t` (or an exact `#d`); `domain`, `schema` and
  `entity` are checked locally.
- `legacy_kind` is the family discriminator (`kinds.CPStateFamily`). It is a
  number inside the envelope, never a wire kind.
- `deleted` is `"true"` on a tombstone and `"false"` on a live record.
  Consumers compare the value. A tombstone shares its coordinate with the
  record it retires and carries minimal content (`{"deleted":true,"id":…}`).
- Confidential families carry a `state_hash` tag (keyed hash used for
  deduplication) and a `bahia.org-state.aead.v1` / `bahia.confidential.aead.v1`
  envelope as content: `envelope=service-held-aead`,
  `algorithm=xchacha20-poly1305`, `key_ref`, `key_version`, `nonce`,
  `ciphertext`, with `d` and `t` bound as associated data. Org-scoped
  records use the org content key (OCK, distributed through
  `org-key-envelope` records); fleet-scoped records use the fleet OCK
  ([confidential state](architecture/confidential-state.md)).
- Ordering follows NIP-01: newest `created_at` wins, lowest id on a tie. A
  payload's `updated_at` never promotes a losing event.

### Family topics

| Area | `#t` topics | Coordinate pattern | Visibility |
|---|---|---|---|
| Services, environments | `service-state`, `service-registry`, `environment-registry` | `service:<svc>:<env>`; bare entity id | public |
| Deployments | `deployment-intent`, `deployment-run`, `build-registry`, `artifact-registry`, `policy-registry` | `<prefix>:<uuid>` or bare id | public |
| LLM | `llm-route`, `llm-state`, `llm-release` | `<prefix>:<uuid>` | public; release content is OCK ciphertext |
| Packages | `package-repository`, `package-artifact`, `package-promotion`, `package-intent` | `<prefix>:<id>` | public; intents are OCK ciphertext |
| ML | `ml-model`, `ml-model-version`, `ml-dataset`, `ml-recipe`, `ml-recipe-run`, `ml-endpoint`, `ml-endpoint-state`, `ml-evaluation`, `ml-provenance`, `ml-runtime-capability` | `<prefix>:<id>` | public |
| Backup | `backup-definition`, `backup-policy`, `backup-repository`, `backup-retention`, `backup-recipe`, `backup-run`, `backup-verification`, `backup-restore`, `backup-runtime` | `<prefix>:<id>` | public |
| DNS | `dns-zone`, `dns-endpoint`, `dns-policy`, `dns-backend`, `dns-zone-sync` | `zone:<name>`, `dnsbackend:<ref>`, deterministic endpoint/policy ids | public |
| Workers | `worker-state`, `worker-assignment`, `worker-drain`, `worker-eligibility`, `worker-cleanup` | `worker:state:<pubkey>`, `worker:assignment:<pubkey>`, `worker:drain:<pubkey>`, `worker:eligibility:<preview>`, `worker:cleanup:<pubkey>:<run>` | public |
| Supply chain | `sbom-reference`, `sbom-availability`, `artifact-signature`, `artifact-sbom`, `artifact-sbom-package` | content-addressed (`sbom:…:<sha256>`) | public |
| Security | `security-scan-status`, `security-summary` | `security:scan-summary:<run>`, `security:target:<hash>` | public |
| Security (execution) | `security-target`, `security-run`, `security-schedule`, `security-finding`, `security-finding-detail` | `security:target:<hash>`, `security:run:<id>` | fleet-OCK ciphertext |
| Security (details) | `security-findings`, `security-audit` | per run | protected (NIP-42) |
| Runtime | `runtime-instance-health`, `route-canary`, `runtime-observation` | `runtime:recovery:…`, `runtime:maintenance:…`, `route:<svc>:<env>:<unit>:<host>` | health and canary public; observations protected |
| Org and tenancy | `org-registry`, `org-member`, `org-invite`, `org-key-envelope` | `org:<id>`, `org:member:<org>:<pubkey>`, `org:invite:<org>:<id>` | OCK ciphertext |
| Secrets, notifications | `secret-registry`, `notification-channel`, `notification-log` | `<prefix>:<id>`; log is one latest-50 window per channel | OCK ciphertext |
| Payments | `payment-record` | per record | fleet-OCK ciphertext |
| Adoption, Hive-CI | `adoption-binding`; `hiveci-policy`, `hiveci-result`, `hiveci-initiation`, `hiveci-release` | `adoption:binding:<svc>:<env>`; `hiveci:policy:<hash>`, `hiveci:result:<event>`, `hiveci:initiation:<event>` | binding public; Hive-CI records fleet-OCK ciphertext |
| Tools | `tool-provision-intent`, `tool-denylist`, `tool-profile` | `<prefix>:<id>` | fleet-OCK ciphertext |
| Blossom | `blossom-admin`, `blossom-blob` | per blob/admin record | fleet-OCK ciphertext |
| Operators | `operator-allowlist` | `operators:continuity`, `operators:soul-factory` | fleet-OCK ciphertext; no `p` tags |
| Assistant | `assistant-status`; `assistant-session`, `assistant-transcript` | `bahia.assistant-session.v2:<session>` | status public; session and transcript protected |
| Relay and config | `relay-settings`; `config-status` | `relay-settings:operator`; `config-status:<service>:<policy>:<scope>:<event>:<status>` | protected |
| SoulFactory | `soul-factory-runtime-policy`, `soul-factory-saga-run`, `soul-factory-adapter-ledger` | per saga / ledger | protected; ledger is fleet-OCK ciphertext |
| Continuity | `continuity-heartbeat` (on `30315`) | per worker | public |
| Audit correlation | `cp-audit` (on `4903`) | — | protected (kind) |

"Public" means the sidecar serves the topic to unauthenticated readers;
"protected" means a NIP-42-authenticated, admitted reader is required
(`nostr.sidecar.read_auth_mode`, default `enforce`). Ciphertext families are
served to anyone because relay auth adds nothing to encryption.

### Operator allowlist records

`operators:continuity` mirrors `nostr.authorized_pubkeys`;
`operators:soul-factory` mirrors `soul_factory.authorized_pubkeys`. Content is
the fleet-OCK encryption of
`{"scope":"<scope>","pubkeys":[<64-hex, sorted>],"updated_at":"<RFC 3339>"}`.
The record is published at startup after the fleet OCK exists, replaced only
when the normalized set changes, and tombstoned when a scope is empty or
disabled. Nothing is published without a confidential encryptor. Browsers
holding the fleet OCK derive the trusted operator set per scope as
*allowlist ∪ signed-in key* and widen their continuity and SoulFactory
`authors` filters accordingly.

### Service and environment records

The registry and the projector build these records with one builder
(`internal/adapters/nostr/control_state_contract.go`), so a stored row and its
relay record are the same signed event. `created_at`/`updated_at` are minted
before publication at microsecond precision; `updated_at` is the revision
token clients send back as `expected_updated_at`. An environment record's
`deployment_units` lists explicit units sorted by `key` with their declared
fields and `implicit:false`; an environment without explicit units carries
only `{"key":"<default_unit_key>","implicit":true}`. Service-state content
carries typed `service_id`, `environment_id`, `drift_status`, an optional
non-secret `desired_runtime_state` snapshot and reconciliation backoff fields;
free-form failure text is not relay content.

### Config Fabric status

`domain=config-status`, `schema=cascadia.config.status.v2`,
`d=config-status:<service>:<policy>:<scope>:<config_event_id>:<status>`.
Accepted and rejected receipts never replace applied evidence; replay selects
the highest applied version and checks its target event id to determine
drift. When a desired document is deleted or expires the consumer publishes
`withdrawn` and keeps the last applied policy live.

### Virtualization

Operator-signed operation requests use kind `30900`, `t=bahia-intent`,
`t=virtualization`, `domain=virtualization`,
`schema=bahia.intent.virtualization.v1`, `op=request`, an `org` UUID,
`intent_id=<uuidv7>`, and `d=vm-operation:<operation_id>`. The complete
content binds `operation_id` (UUIDv7), `resource_id`,
`resource_kind=persistent_vm`, `action` (`start`, `graceful_stop`, or `reboot`),
`expected_generation`, `idempotency_key` equal to `intent_id`, and a reason.
The daemon checks the signature, tenant permission, envelope and content
binding, then refuses the operation while canonical commit and restart-safe
execution are unavailable. It attempts a `rejected` `30315` status; a status
publication failure leaves the request unaccepted, and a later replay can
reattempt the refusal. A relay `OK` is not operation acceptance; no SQL
journal row authorizes execution or state publication.

`schema=bahia.state.virtualization.v1`, `d=<resource-prefix>:<uuid>`, tags
`domain=virtualization`, `entity`, `org`, `generation`, `sequence`,
`lifecycle_class`, and `journal=<org>:<sequence>:state`. Content is the public
DTO only: `schema`, `sequence`, `change_type`, `occurred_at`, optional
`approval_id`, `resource` (with a `deleted` flag). No evidence, bootstrap,
console content, host path or credential bundle is projected. Audit facts use
`schema=bahia.audit.virtualization.v1`, `state=<coordinate>`, `type`,
`protected=true` and `journal=…:audit`. Consumers enforce journal
sequence/generation ordering in addition to NIP-01 for previously signed
records. The SQL-journal projector does not publish new state or audit.

## 6. Operational status (`30315`)

NIP-38 status is used where a status fact is more appropriate than a full
state replacement. Besides intent status (§3):

- **Deployment steps**: `step` tag with the shared sequence
  `building_desired_state`, `locking_environment`, `rendering`, `applying`,
  `observing`, `projecting`.
- **Run health** (Loom-backed runs): `domain=deployment`, `entity=run`,
  `t=deployment.run.health`, `d=<run-uuid>`, `e=<loom-job-id>`,
  `status=stale|recovered`; content `bahia.deployment-run-health.v1`. A run
  is stale after `nostr.stale_run_after` (default `5m`) without a Loom
  `30100` status. Publication requires an explicit `loom.relays` worker-status
  boundary and a fresh, durable `30100` catch-up plus live EOSE from every
  configured interop relay. A dropped/refused relay, storage failure, relay
  topology change, or incomplete same-second replay suspends publication
  until the affected relay catches up again; prior EOSE readiness is not
  reusable after restart.
- **Security scans**: `domain=security`, `schema=bahia.status.security-scan.v1`,
  `d=security:scan:<run_id>`, `run`, `target_type`, `target_key_hash`,
  `status` ∈ `accepted|queued|running|completed|failed|cancelled|degraded`.
- **Route canaries**: `schema=bahia.status.route-canary.v1`,
  `d=route:<service>:<environment>:<unit or none>:<hostname>`, with
  `status`, `outage=open|closed`, `classification`, `hostname`,
  `instance_status`, `service_healthy_route_broken`. An open outage is
  `unhealthy`, a warning or pre-threshold failure `degraded`, `route_ok`
  `healthy`.
- **Managed instances**: instance status under the `runtime-instance-health`
  family.
- **Continuity heartbeats**: `t=continuity-heartbeat`, `domain=continuity`,
  `schema=bahia.status.continuity-heartbeat.v1`, heartbeat `d`/`worker`
  tags.
- **Assistant health**: `t=assistant-status`.
- **Agent runtime releases**: `domain=agent-runtime-release`
  (`d=runtime-release:<uuid>`; `org`, `source`, `digest`, `branch`,
  `release_channel`) and `domain=agent-service-release`
  (`d=agent-service-release:<uuid>`; `agent`, `service`, `release`,
  `source_event`, optional `previous_binding`). Observable state only; it
  does not authorize deployment.

## 7. Audit (`4903`)

Audit facts are regular events: no `d`, so repeated facts about one entity
coexist. The projector's facts use `schema=bahia.audit.v1` and correlate by:

- `state=<cp-state d of the audited entity>`,
- `t=cp-audit` plus `t=<event type>`,
- `e=<source event id>` when known,
- `fact=<sha256(type, entity, content)>`, which makes republishing idempotent.

Domain facts add `domain`, `type` and a domain schema
(`bahia.audit.deployment.v1`, `bahia.audit.security.v1` with `type` ∈
`security-scan|security-policy-breach|security-publication`,
`bahia.audit.route-canary.v1` on transitions only,
`bahia.audit.assistant-execution-checkpoint.v1` with encrypted content and
`session`/`run` tags). Bahia tags audits `protected=true` as semantic
metadata; it does not add the NIP-70 `-` tag. The sidecar keeps regular
events durably unless `nostr.sidecar.event_retention` is set.

## 8. App data and collections

- **SBOM reference** (`30078`): `domain=sbom`, `schema=bahia.sbom.ref.v1`,
  `d=sbom:ref:<subject-key>:<format>:<payload-sha256>`; content is the
  in-toto-style attestation envelope; tags identify subject, format, storage
  backend, location, `x` payload hash, media type, generator and NTIA status.
  Payload bytes live in Blossom.
- **SBOM availability** (`30004`): `domain=sbom`,
  `schema=bahia.sbom.available-list.v1`,
  `d=sbom:available:<subject-type>:<subject-key>`; replaced as a complete set
  with `a` tags to the `30078` references.
- **Security findings** (`30078`): `domain=security`,
  `schema=bahia.security.findings.v1`,
  `d=security:findings:<run_id>:<chunk_or_finding_hash>`; normalized
  public-safe findings. Raw OSV cache records are not published.
- **Config Fabric documents** (`30078`, `d=service:<service_id>:relay-sidecar`)
  and **membership lists** (`30000`, `d=service:<service_id>:membership`) are
  signed desired state the sidecar's config consumer applies.
- **Assistant transcript** (`30316`): content is a
  `bahia.assistant-transcript.v1` service-held AEAD envelope; tags `session`,
  `turn`, `role`, `seq`, `key_ref`, `key_version`, `key_rotation`, `envelope`,
  `t=assistant-transcript`, `p` for the operator.
- **SoulFactory fleet configuration** (`31953`): addressable by
  `d=soulfactory-fleet-config/v1`, tags `schema=soulfactory-fleet-config/v1`,
  `t=soulfactory-fleet-config`; content `{"schema","template":{agents,
  channels, plugins},"defaults":{model, bindings, required_plugins}}`.
  Secret-shaped strings are `${VAR}` placeholders. Provisioning pins the
  newest signed event from `soul_factory.authorized_pubkeys`.

## 9. Discovery and relay topology

| Kind | Content |
|---|---|
| `11316` | Server announcement: `schema`, `registries`, `versions`, `observed_deployments`, `control_plane` (`wire_version`, `capabilities`, kind map, advertised ContextVM methods), `blossom`, `runtime`, `oci`, `nostr.trusted_relay_monitor_pubkeys`, `assistant`, and `features` (`oci`, `harbor`, `blossom`, `hiveci`, `cashu`, `telemetry`, `notifications`, `auth`, `relay_sidecar`, `relay_read_models`, `encrypted_nostr_requests`, `llm_control_plane`, `direct_nostr_http_auth`, `mcp_transport`, `publish_enabled`) |
| `11317`–`11320` | Tools, resources, resource templates, prompts |
| `30002` | Relay sets: `d=bahia-browser-v1` (`nostr.browser_relays`), `d=bahia-contextvm-v1` (`nostr.contextvm_relays`, falling back to browser relays), `d=bahia-service-v1` (`nostr.service_relays`) |
| `10002` | Service NIP-65 list: ContextVM relays as `read`, service relays as `write`; advisory only |
| `10050` | DM relay list for `nostr.dm_relay_lists` entries with `identity: service`; never inferred |

Discovery requires `nostr.browser_relays`; with the sidecar enabled, missing
browser relays is a startup error. NIP-11 and NIP-66 are advisory capability
and liveness metadata; they never establish trust or remove configured
relays. NIP-86 is the sidecar's HTTP administration API, not an event kind.

## 10. Entity identity

An entity exists once its author has fixed its id; no database mints it. The
rationale is in [entity identity](architecture/entity-identity.md).

- Ids are RFC 9562 UUIDs in canonical lowercase hyphenated form. New ids are
  UUIDv7; a create may carry a UUIDv4. Other versions, nil/max, non-RFC
  variants and non-canonical spellings are rejected in a create; decoders
  accept any version in an existing coordinate.
- Coordinates keep each family's grammar with the author-minted id:
  `<resource-prefix>:<id>`, bare `<id>` for service and environment
  registries, composites such as `service:<svc>:environment:<env>`.
- The web stores, `pkg/client` (used by `bahia services create` and
  `bahia environments create`, which print the minted id and accept `--id`)
  and the MCP create tools mint a UUIDv7 before signing and accept a
  caller-supplied id.
- A create is resolved by id, then content: no record → created; same content
  → idempotent (nothing written or published); different content → rejected
  as a conflict (`id already exists with different content`). Deliberate id reuse by another author
  is a different relay coordinate and a conflict, never a write into someone
  else's entity.
- Names (`service`, `environment`, `policy`, `llm_route`) are fleet-wide
  uniqueness constraints, not identity; the DNS projector turns environment
  and service names into zone and label, so they are a shared namespace.
- Natural keys stay natural: `zone:<name>`, `dnsbackend:<ref>`,
  `sbom:ref:…:<sha256>`, `security:target:<hash>`,
  `soul-factory:provisioning:<request-event-id>`. Entities the daemon authors
  (runs, observations, backup runs) are minted in code as UUIDv7.

## 11. Reading and converging

- Subscribe with `authors=[service pubkey]` plus `#t` (or `#d`); process
  stored `EVENT`s until `EOSE`, then keep the subscription open. `EOSE`
  completes only the bounded query (`max_query_limit`, default 2000): narrow
  with `since`/`until` and resource tags, overlap windows, or use NIP-77
  negentropy for full-set reconciliation.
- Deduplicate by event id (delivery across the stored/live boundary is
  at-least-once). Apply NIP-01 replacement for addressable kinds.
- Handle `CLOSED` and `AUTH` explicitly: the sidecar answers protected
  filters from an unauthenticated socket with `AUTH` then
  `CLOSED … auth-required`, an authenticated but unadmitted pubkey with
  `CLOSED … restricted`, and a client that falls behind with
  `CLOSED … live delivery queue overflowed` (re-subscribe from the last seen
  event). The daemon's relay pool reissues a retryable `CLOSED` at most
  `nostr.closed_retry_budget` (default 5) times per relay and filter.
- Relay `OK` per relay is delivery. The daemon's own publishes succeed once
  `nostr.publish_quorum` relays (default 1) accept and keep retrying the rest
  from the local outbox (`bahia outbox` inspects it); an entry abandoned after
  its producer was told it was queued is flagged undelivered on its coordinate
  and reported by the `canonical_delivery` readiness check.
- Do not poll HTTP or MCP for completion of anything that has a canonical
  observable.

## 12. Event-store import (`bahia-migrate nostr`)

`bahia-migrate nostr` (`internal/nostrmigration`) converts recognized source events found in the local event store — and, with
`nostr.legacy_relay_backfill: true`, on an explicitly configured import relay —
into canonical events tagged `migration=bahia-nostr-native-v1`,
`legacy-kind`, `migrated-from=<source event id>`, `schema`, `domain` and layer metadata,
signed with the service key and published to the configured relays. It skips
targets that already carry `migrated-from` for the source, requires `EOSE`
from any backfill relay and treats accepted or duplicate `OK` as success. It
is idempotent; operators run it explicitly (the daemon never runs it) when
they import an event store produced by another Bahia installation. See the
[CLI reference](user-guide/cli-reference.md).

## 13. In-process event types

The daemon also emits typed in-process events for projectors, notification
dispatch and metrics. They are not Nostr kinds and never carry secret values,
raw environment values, Docker TLS material or credentials:

| Type | Key fields |
|---|---|
| `adoption.scan_completed`, `adoption.imported` | counts, `service_id`, `environment_id`, `artifact_id`, `status` |
| `runtime.deploy`, `runtime.restart`, `runtime.stop` | `service_id`, `environment_id`, `runtime_target`, `observation_id`, `health_status` |
| `llm_route.created/updated`, `llm_release.registered`, `llm_deployment_intent.*`, `llm_deployment_run.*`, `llm_route.observation`, `llm_route_state.changed`, `llm_route.drift_detected`, `llm_gateway_route.synced` | `route_id`, `release_id`, `environment_id`, `intent_id`, `run_id` |
| `security.policy_breached` | `policy_id`, `target_key_hash`, `fingerprint`, `severity_counts`, `violated_rules`; emitted only when the breach fingerprint is new or materially changed |
