# Bahia Nostr Event Implementation Guide

## Artifact, policy, and approval publishers

Artifact registration, policy create/update/delete/evaluate, and tool approval
publishers emit signed ContextVM JSON-RPC kind `25910` envelopes through the shared
command publisher, never retired request kinds `5985`–`5989` or `7977`. They retain
idempotency/progress tokens, correlate receipts with the request event, and require
relay acceptance. Methods are `artifact/register`, `policy/create`, `policy/update`,
`policy/delete`, `policy/evaluate`, and `tool/approval-response`. LLM approval uses
`approval/llm-approve` or `approval/llm-reject`, selected by the validated decision.

Publisher support does not imply a registered server consumer. Discovery's
`control_plane.methods` contains only registered methods; unsupported LLM, ML,
package, policy and tool methods are not advertised. AI/ML discovery describes
read models, not callable ML mutation handlers. DNS advertises `dns/record-set`
and retains `dns/override-retire`, not the unregistered `dns/record-override` or
`dns/backend-register`. Receipts acknowledge submission, never durable completion.

## Virtualization public projection contract

Virtualization reuses `CASControlState`/30900 and `CASAudit`/4903, consistent with
Cascadia registry CF-9 state and audit semantics; it allocates no numeric kind and
adds no legacy production kind. ContextVM intents remain 25910 with configured
wrapping. It emits no new NIP-38 status family. `vm-operation/approve-plan`
returns an approval-only acknowledgment, not an operation or provider result.
Deployment-delete intent includes approval-bound `data_disposition` with a retain
default; this changes no canonical projection kind, schema, or tag.

`persistent-vm/register-adoption` returns `status=registered` without an operation
coordinate. It registers measured candidate desired state, not ownership or an
applied baseline. A separately approved `adopt` operation binds private measured
configuration/image/component evidence. No raw measurement, host paths or storage
identity is added to public state/audit payloads; kinds and schemas are unchanged.

Both projections require `domain=virtualization`, explicit `entity`, `schema`,
`org`, `generation`, journal `sequence` and explicit `lifecycle_class` tags.
State uses `schema=bahia.state.virtualization.v1` and `d=<resource-prefix>:<uuid>`;
resource prefixes are `virtualization-host`, `vm-image`, `persistent-vm`,
`execution-plane`, `vm-checkpoint`, `vm-export`, `vm-operation`. Audit uses
`schema=bahia.audit.virtualization.v1`, `type=<journal change type>`,
`state=<coordinate>` and `protected=true`, without `d`. Public actors use `p`;
operation correlation uses a UUID `correlation` tag. Internal `journal` tags bind
outbox records to tenant/sequence/output type for crash-safe deduplication.

Content contains a full public-safe `resource`, never private repository JSON;
bootstrap, storage locations, arbitrary labels, console contents and raw provider
errors are excluded. Every audit fact is preserved; state coalesces per replay
page. Persisted signed events precede cursor advancement. Same-coordinate state
publication is serialized/rate-limited to avoid same-second ties; clients must
also reject older sequences/generations. The existing bus provides live wakeups,
and startup/gap recovery drains the journal without DB notifications or polling.
See the [operator contract](user-guide/features/virtual-machines.md).


This guide is the Bahia-specific implementation policy for Nostr event kinds, event shapes, and Cascadia fleet interoperability. It adapts the Cascadia Nostr-native event strategy into rules that agents working in this repository can apply directly.

Use this guide before adding, publishing, subscribing to, decoding, migrating, or documenting any Nostr event.

## Replaceable projection ordering

Follow [NIP-01](https://github.com/nostr-protocol/nips/blob/master/01.md): retain
newer signed `created_at`, then the **lowest** event ID on a timestamp tie.
Replaceable kinds (`0`, `3`, `10000`–`19999`) use `(kind, pubkey)`;
addressable kinds (`30000`–`39999`) use `(kind, pubkey, d)`. In particular,
control state `30900`, status `30315`, app data `30078`, and SoulFactory fleet
config `31953` are addressable. Status labels do not override this rule.

Browser collections reduce each wire coordinate before merging legacy and
corrected coordinates by domain time. A payload's `updated_at` cannot promote
a losing same-coordinate event. Go projection-cache ordering uses the signed
wire timestamp, while decoded domain timestamps remain in the entity payload.
Migration `000068_relay_projection_wire_time` rebases persisted ordering
metadata from the source events' signed timestamps; metadata without a stored
source is invalidated for replay, without deleting canonical events or entities.
Archive latest queries, relay persistence, and runtime/fleet selectors use the
same lowest-ID tie-break. Replay must preserve tombstone winners as well.

A later publication within the same second is **not** necessarily a newer
revision: hashes do not encode causal order. Workflow fixtures asserting a
succession of states must assign distinct publication seconds, replay stable
events, and test genuine same-second conflicts separately. Config consumer
`accepted` and `applied` currently share one status coordinate; correct NIP-01
retention does not guarantee applied-status durability (`bahia-1antv`).

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

## Fleet-local Hive-CI exception

Hive-CI kinds `5401` (workflow run) and `5402` (workflow result) are an existing fleet-local protocol, not Bahia legacy kinds and not ContextVM migration targets. A signed inbound `build/request` remains a ContextVM kind-`25910` mutation, but Bahia's resulting CI-bus dispatch is a durable kind `5401`; it must not be published as ephemeral kind `25910`.

The upstream `hive-ci-protocol` and `loom-protocol` specifications are canonical (fleet decision 2026-09-19); Bahia conforms to them rather than the reverse.

Bahia-produced `5401` events use the hive-ci-protocol tag-only shape: empty content with `a`, `commit`, `branch`, `trigger`, `triggered-by`, `workflow`, `publisher`, and `t=hive-ci`. `a` is the configured NIP-34 repository announcement address. `publisher` is a **fresh per-run ephemeral pubkey** — the key that will sign the `5402` result, exactly as the protocol defines — never the Bahia service key. The `5401` itself is signed by the Bahia service key; operators must include that key in `hiveci.trusted_ci_pubkeys` for Bahia's own subscriber to accept self-dispatch. Bahia warns when that trust is absent and never adds it automatically.

After the control-plane relays accept a self-issued `5401`, Bahia submits a loom-protocol kind `5100` through the existing Loom client on the same relay pool and signer, in the spec's executable shape: `p=<trusted worker>`, `e=<5401 event id>`, `cmd=loom-ci`, and `args=[run --repo <credential-free mirror clone URL> --ref <ref> --workflow <path> --event push --actor <requester> --run <5401 event id> --dep <name>=<https url>@<40-hex sha> …]`. No `method`, `repo`, `ref`, `run`, `workflow`, or `dep` tags are emitted. The request carries exactly three `secret` tags, each independently NIP-44 encrypted to the selected worker: `HIVE_CI_GIT_USERNAME`, `HIVE_CI_GIT_PASSWORD`, and `HIVE_CI_NSEC` (the ephemeral publisher key, which the worker keeps in-process and uses only to sign the `5402`). Selection is restricted to `hiveci.trusted_loom_worker_pubkeys` whose signed kind-`10100` advertisement lists `loom-ci` as `S` software alongside `git`, `act`, and `docker`; administrative workload/feature labels are not capability evidence. Bahia resolves a dedicated, service-scoped read-only mirror credential before publishing and records only its opaque UUID in queued evidence.

One `(a, commit, workflow)` tuple is one build. Before publishing, `build/request` consults Bahia's ingested runs (`HiveCIRepository.FindWorkflowRun`): if a trusted producer — grasp-gitea on push, or an earlier Bahia request — already published the `5401` for that tuple, Bahia adopts it (returns its id as `ci_run_id`, records queued evidence) and publishes neither a competing `5401` nor a second Loom job. A lookup failure fails closed. Symmetrically, the subscriber persists a second signed `5401` for an already-ingested tuple as evidence but never dispatches it (`duplicate_workflow_run`).

This fleet-internal profile sends no `payment` tag because Bahia has no mint-backed Cashu payment capability; the worker authorizes unpaid CI requests by requester pubkey (`ALLOW_UNPAID_PUBKEYS`). Bahia's subscriber accepts the `5402` when it is signed by the `5401` `publisher`; acceptance from `hiveci.trusted_loom_worker_pubkeys` remains only for runs Bahia did not author (the observed-`5401` `release=true` dispatch path, which holds no publisher key).

The tag-only protocol has no build-argument field. The initiator fails closed before resolving credentials, mirroring, or publishing when `build_args` is non-empty; it never records requested values as build provenance unless they can reach the builder.

## SoulFactory fleet configuration exception

Kind `31953` is the parameterized-replaceable SoulFactory interoperability document for fleet-wide OpenClaw configuration. It uses `d=soulfactory-fleet-config/v1`, a matching `schema` tag/content field, and a complete `template` snapshot. This allocation is intentionally adjacent to the staged `31950`–`31952` SoulFactory family: provisioning reactors and external runtimes must query and carry it without treating it as Bahia service-authored canonical state. Only configured `soul_factory.authorized_pubkeys` compete for the current document. A generic NIP-78 object would have different policy classification and would not provide this SoulFactory runtime interoperability boundary.

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
- Maintenance methods are stricter: `maintenance/*` requests and immediate responses use the standards-conformant NIP-59 rumor → seal → kind-`1059` construction. Plaintext `25910` and the older direct-encryption envelope are transition readers only, never maintenance writer fallbacks.
- Correlate retries with a stable idempotency key.

Examples:

| Operation | ContextVM method |
|---|---|
| Preview managed service desired state | `service/deploy-preview` |
| Deploy service | `service/deploy` |
| Attach a public route to the current deployed service without artifact convergence | `service/route-attach` |
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
| Create/update environment | `environment/create`, `environment/update` |
| Read authorized environment details | `environment/get-details` |
| Read Security findings or schedules | `security/findings-list`, `security/schedules-list` |

Encrypted backup mutations check the verified inner-event pubkey for the
selected tenant's `backups:manage` capability before Bahia re-signs a canonical
backup command. That service-signed event carries a
`bahia.backup.delegation.v1` authority record and matching requester, original
request, tenant, and capability tags. Never infer requester authority from the
Bahia service event pubkey; service-self, incomplete, mismatched, unauthorized,
and ambiguous delegations fail closed.

`environment/get-details` accepts `id`, requires `environments:read` for the signed requester in the owning organization, and returns the environment plus its explicit or resolved implicit deployment units in the signed ContextVM result.

For `environment/update`, a supplied `deployment_units` array is authoritative and requires `expected_updated_at` from the latest read. The service checks it while holding the environment row lock; a stale write fails with JSON-RPC code `-32009` and does not mutate the database or canonical registry projection. Only retry after a fresh signed read, deliberate remerge, and new signature.

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

#### Config Fabric durable status receipts

Config consumers publish kind `30900`, `domain=config-status`, and
`schema=cascadia.config.status.v2`. Each receipt is a complete fact about one
signed desired config event and one phase, with this address:

`d=config-status:<service>:<policy>:<scope>:<config_event_id>:<status>`

The phases are `accepted`, `applied`, and `rejected`. The existing `service`,
`scope`, `version`, `status`, and `e=<config_event_id>` tags match the content.
An `applied` receipt must bind `last_applied_event_id` to `config_event_id` and
`effective_version` to `version`. `accepted` only acknowledges durable admission;
it is not proof of activation. A rejection of a duplicate desired event does
not retract a previously published applied fact.

This address separates both phases and target events. A relay retaining one
event per `(kind, pubkey, d)` therefore cannot replace applied truth with
accepted/rejected progress, or lose a newer target's applied fact to an older
target's same-second receipt. Retried publications of the same phase and target
still use ordinary NIP-01 replacement. No relay-specific status precedence,
mutex, fabricated future timestamp, or higher-resolution `created_at` is used:
NIP-01 timestamps remain Unix seconds and equal-time ties remain lowest-ID wins.

Replay folds applied receipts by greatest effective config version, not by
publication time. Rollback publishes a higher config version containing the
older policy. Drift clears only when both the applied event ID and effective
version match the current desired event. Keep subscriptions scoped to kind,
service/scope and, when following a target, `#e`; clients querying exact `#d`
addresses must enumerate the target's phases instead of the old shared address.
This trades a single lossy snapshot for up to three retained coordinates per
desired event per consumer author; history is not bounded to one service row.

Readers also accept retained `cascadia.config.status.v1` records at the old
`config-status:<service>:<policy>:<scope>` address. Writers emit only v2. Upgrade
readers before writers; an old v1-only reader cannot decode v2. Previously lost
v1 applied events cannot be reconstructed from accepted status: no migration
may invent activation evidence.


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
| SoulFactory agent lifecycle | Operator, SoulFactory controller, runtime sidecar | ContextVM `25910` methods `soul-factory/provision` and `soul-factory/action`, plus fleet-gated `soul-factory/saga/{inspect,retry,reconcile,safe-abort}`; canonical provisioning `30900`/`4903`; staged lifecycle kinds `31950`, `31951`, `31952`, `31953`, `5950`, `6950`, `7950`, `1950`, `1951`, `30317`, `38384`, `38386` | New mutations enter through ContextVM and retain the request event id for correlation. Provisioning projects canonical state/audit; existing lifecycle events remain open interop while action and Soul read-model projection are completed. |
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

After conformant NIP-59 unwrap, Bahia must verify the seal and canonical unsigned rumor, then authorize the seal-authenticated rumor author before executing. A NIP-59 rumor is intentionally unsigned; treating its missing signature as an authorization bypass or failure is incorrect.

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

### Managed-instance supervisor observables

The managed-instance health projector subscribes to internal runtime health, recovery, and maintenance events. It publishes NIP-38 kind `30315` status with schema `bahia.status.managed-instance-health.v1` and stable `d=runtime:instance:<service>:<environment>:<deployment-unit>:<sha256(runtime-target)>`, kind `30900` current state with schema `bahia.state.managed-instance-health.v1`, and immutable kind `4903` audit facts with schema `bahia.audit.managed-instance-health.v1`. Evidence is sanitized before projection and publication uses the signed durable outbox/relay-OK path. No polling or new event kind is introduced.

### Route canary observables

The route canary projector subscribes to the in-process route canary transitions the route canary supervisor publishes (`route.canary_outage_opened`, `route.canary_recovered`, `route.canary_classification_changed`) and projects each one to existing canonical kinds. No new event kind is introduced.

| Kind | Schema | Addressing | Purpose |
|------|--------|------------|---------|
| `30315` | `bahia.status.route-canary.v1` | `d=route:<service>:<environment>:<deployment-unit or none>:<hostname>` | Current bounded route health |
| `30900` | `bahia.state.route-canary.v1` | same `d` | Current durable route canary state; content carries the full `route_canary` state |
| `4903` | `bahia.audit.route-canary.v1` | no `d`; `state=<route coordinate>` | Immutable transition fact with sanitized per-perspective probe evidence |

The `d` coordinate is the same route coordinate the REST API, route lineage and in-process events use. All three events carry `domain=route`, `service`, `environment`, `deployment_unit` (when the route is bound to a deployment unit), `hostname`, `status`, `outage=open|closed`, `classification`, `perspective` (when known), `instance_status` (when a container status was observed), and `service_healthy_route_broken=true|false`. Status and state also carry `entity=route-canary`; audits carry `type=<in-process event type>` and `transition=opened|recovered|classification_changed`.

Vocabulary decisions:

- `domain=route` is a Bahia operational domain added under NIP-CAS-0002, which allows new `domain` values by convention. It does not collide with `domain=llm`, which Bahia uses for LLM routes.
- The managed hostname is carried in a `hostname` tag, not a `route` tag. The Cascadia `route` tag is registered for LLM/API route identifiers.

The bounded `status` tag is the fleet-health status of the route:

| Route state | `status` |
|-------------|----------|
| Outage open, whatever the latest classification (including while recovery streaks accumulate) | `unhealthy` |
| Failing below the open threshold (including a warning an operator promoted to a failure) | `degraded` |
| Warning classification: `tls_expiring`, `health_path_not_discriminating` (the route still serves) | `degraded` |
| `route_ok` | `healthy` |
| Unrecognized classification | `unknown` |

`service_healthy_route_broken` is true exactly when an outage is open and the observed container status is `healthy` or `running`, matching the REST API field of the same name. An unknown container status is not evidence that the service is up, so it never sets the flag.

Publication rules:

- `created_at` is the transition time. Replaceable status/state for a route are never overwritten by an older transition that is handled late; audit facts are immutable history and are always published.
- Each event is recorded as published only after the signed outbox/relay path returns relay `OK accepted=true`. A rejected publish does not stop the rest of the transition's events. All failures are returned together so the in-process bus retries the transition, and events already accepted are skipped on the retry.
- Dedupe state is bounded: the last accepted event per `(kind, route coordinate)`.
- The projector is wired only when Nostr publishing is enabled with a service private key.

Fleet-health telemetry counts `domain=route` as its own bounded domain. Route status/state define one entity per route coordinate. Route `4903` audit facts are lineage and never define or overwrite a route entity. A route status/state without a `d` coordinate cannot be attributed to a route and is counted as a projection error.

The inbound subscriber delivers every validated, persisted event to fleet-health telemetry as an idempotent observer, including relay echoes of Bahia's own publications. The publisher persists each signed event before its first relay attempt, so those echoes always arrive as already-persisted duplicates, and side-effect handlers remain gated on first insert.

Known limits:

- The post-deploy route gate records its outcome durably but does not publish in-process transitions, so a gate-declared outage reaches Nostr when the supervisor next reports a transition for that route.
- Route state is projected on transition. A route whose state predates the projector is projected on its next transition.
- Fleet-health telemetry keeps its projection in memory and does not replay relay history on restart. Replaceable route state remains queryable on relays.

### Agent runtime release read models

Shared verified agent runtime releases use canonical CAS control-state kind `30315` with schema `bahia.agent-runtime-release.v1`. `domain=agent-runtime-release` projects immutable runtime source and provenance; `domain=agent-service-release` projects an append-only agent/service binding and its optional `previous_binding`. Runtime source `repository`, `branch`, and `release_channel` are distinct from Soul workspace/persona repository fields. These are observables, not deployment intent (`25910`) commands.

### Adjudicated mutation consumers (2026-09-24)

Policy CRUD/evaluation and worker uncordon/undrain/maintenance-enter now execute
inside the ContextVM transport behind the fail-closed fleet operator gate.
Policy registry writes use `30900`, `domain=policy`, `schema=bahia.cp-state.v1`,
and `d=<policy-id>` (the same shape as the projector); they never emit the retired
policy registry kind. See the policy/worker user guides and the
`NOSTR_NATIVE_CONTEXTVM_MIGRATION` verification report for tested behavior and
the retained, unwired continuity/package/tool-approval findings.

### Assistant unified execution checkpoint (contract; not yet wired)

Assistant v2 reuses 30900 state at `d=bahia.assistant-session.v2:<session_id>`
with `domain=assistant` and `schema=bahia.assistant-session.v2`. The durable
execution journal is an immutable 4903 audit fact: required `domain=assistant`,
`type=execution-checkpoint`, and
`schema=bahia.audit.assistant-execution-checkpoint.v1`; add `session` and `run`
for scoped reads, optional `revision`/`prev` lookup hints, and `e`/`p` when
source/actor is known. Validate hints against authenticated checkpoint content.
Do **not** add `d` to
4903. Keep its content a service-held authenticated encrypted envelope, never
public tool arguments or approval secrets. This follows section 4 above and
Cascadia `NIP-CAS-0001`'s required 4903 tags and regular append-only class.
Checkpoint retention, accepted OK and archive recovery must be proved before
activation; public 30900 projections alone are not a dispatch journal. See
[the assistant design](designs/assistant-unified-execution.md).
