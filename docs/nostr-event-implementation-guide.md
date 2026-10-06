# Bahia Nostr Event Implementation Guide

## Organization refounding

The daemon accepts `domain=org`, `op=rekey`, `schema=bahia.intent.org.v1` as a client-signed kind-30900 intent. JSON content is `{"org_id":"<org UUID>","reason":"<optional string>"}`; the `org` tag must match `org_id`. The intent's `d` coordinate can be the org UUID. An owner or admin may request it. For fleet-scoped state, use `org_id="fleet"`, no `org` tag, and a fleet-operator signer. The intent must be gift-wrapped like other sensitive org intents.

On success the requester-scoped kind-30315 status has `status=accepted` and `data={"key_version":"v<n>","records_republished":<integer>}`. Authorization or mid-batch errors produce `status=rejected` with a reason. The daemon first distributes the new org content key, then replaces each confidential current cp-state coordinate on the same kind-30900 `d` address. Records not yet replaced remain readable under their old key versions. Organization cp-state JSON also carries `strict_revocation` (boolean, default `false`); when true, member removal and role downgrade trigger this same refounding after rotation.

D80 request/desired-state operations use the established `30900` intent envelope and bounded `30315` status; the [Go-generated fixture](../web/tests/fixtures/d80-intent-content.json) defines domain/op/coordinate/content. Their canonical outcomes remain security status/findings, SBOM `30078`/`30004` plus `32017`/`32018`, artifact-signature `32016`, build/artifact cp-state, relay-settings protected cp-state, environment cp-state, and ML endpoint cp-state. Notification channel test has only bounded `30315` delivery data. The [command guide](nostr-commands.md#d80-request-operations-and-desired-state) records permissions and read subscription guidance.

## Deployment, runtime, LLM, and backup intents

With registered intent domains enabled by default, clients sign kind `30900` events with `schema=bahia.intent.<domain>.v1`, `domain=deployment|runtime|llm|backup`, `op`, and a stable `content.intent_id`. Deployment operations are `create`, `approve`, `reject`, and `rollback`; runtime operations are `deploy`, `restart`, and `stop`; LLM adds `deploy`, `rollback`, `approve`, and `reject`; backup adds `restore-approval`. If present, `content.expected_updated_at` is the canonical record's RFC3339 `updated_at` string, not a numeric epoch. See the [wire fixtures](../web/tests/fixtures/deployment-intents.json) and [operation table](designs/phase3-authority-inversion.md#13-intent-event-structure). The daemon emits bounded `30315` status and its existing canonical state publishers remain the sole state writers. ContextVM mutation handlers dispatch in-process through the same intent processor only for enabled domains; disabled domains retain their legacy execution path.

## D79 operator intent families

No new kind is introduced. Signed `30900` intents with `schema=bahia.intent.<domain>.v1` now accept ML model import, recipe definition/run, inference deploy/approval/rollback, tool provisioning approval, HiveCI build request, and adoption scan. The tag-level `intent_id` is the replay key even when approval content also has an `intent_id` target. The daemon invokes its existing registry, approval, build-initiation and adoption service methods once and reports the daemon-authored result through requester-scoped `30315` status. `adoption/scan` status data contains a redacted, byte-bounded page of findings. Coordinates, payloads and permissions are in the [D79 fixtures](../web/tests/fixtures/d79-intent-content.json). Unmigrated ContextVM methods use in-process dual dispatch; their legacy service path remains available until the caller migration closes.

## Legacy artifact, policy, and approval publishers

The web signs `artifact/register`, `artifact/import-observed`, `adoption/import`,
`dns/drift-remediate`, `deployment/preview`, `deployment/route-attach`, and
`policy/evaluate` as kind-30900 intents. Request-like preview and evaluation
results arrive in the correlated, bounded kind-30315 status: `data` for the
preview plan and `evaluation` for the policy decision. The web never treats
relay `OK` as daemon acceptance. The Go-generated D76 content fixtures are in
`web/tests/fixtures/d76-intent-content.json`.

Tool approval still publishes signed ContextVM JSON-RPC kind `25910` requests.
Web policy CRUD and evaluation now publish kind-30900 intents. MCP policy CRUD
already dispatches kind-30900 intents; MCP evaluation uses the same
`policy/evaluate` intent operation. No outbound
`PolicyCommandPublisher` remains. Never publish retired numeric request kinds
`5985`–`5989` or `7977`. LLM approval uses `approval/llm-approve` or
`approval/llm-reject`, selected by the validated decision.

Publisher support does not imply a registered server consumer. Discovery's
`control_plane.methods` contains only registered methods; unsupported LLM,
package, policy and tool methods are not advertised. ML registry discovery
advertises `ml/model-*`, `ml/version-*`, and `ml/endpoint-*` create, update,
and delete methods, gated to fleet operators. DNS advertises `dns/record-set`
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
| Create, update, or delete ML registry models | `ml/model-create`, `ml/model-update`, `ml/model-delete` |
| Create, update, or delete ML model versions | `ml/version-create`, `ml/version-update`, `ml/version-delete` |
| Create, update, or delete ML inference endpoints | `ml/endpoint-create`, `ml/endpoint-update`, `ml/endpoint-delete` |
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
- A status with a freshness window (a heartbeat) carries a NIP-40 `["expiration", "<unix seconds>"]` tag, rounded up to whole seconds, so relays and generic clients drop it when it goes stale. Do not add a custom TTL tag. Consumers treat a status as stale from its `expiration`. They read the retired `expires_after_ms` (milliseconds after `created_at`) only from heartbeats whose producers predate NIP-40.

Status events are for short-lived operational state such as `running`, `healthy`, `degraded`, `available`, `draining`, `failed`, or `completed`.

Desired-state runtime deploys report additive step progression on existing status events without allocating new kinds or changing `d` coordinates. The expected service deploy steps are `building_desired_state`, `locking_environment`, `rendering`, `applying`, `observing`, and `projecting`. Operators and agents should treat these as progress breadcrumbs only; terminal truth still comes from the correlated ContextVM response plus canonical state/audit/status observables.

### 3. Is this durable current state or a read-model projection?

Use canonical state.

- Kind: `30900`
- Parameterized replaceable by `(kind, pubkey, d)`.
- `d` must be stable and scoped: `<domain>:<entity>:<id>` or the narrower convention already used by the feature.
- Required tags: `d`, `domain`, `schema`, and a single-letter `t` topic naming the record family.
- Strongly recommended tags: `entity`, `status`, resource tags.
- The projector's cp-state envelope stamps `t=<domain>-<entity>` on every live record and tombstone, for example `service-registry`, `deployment-run`, `backup-run` or `dns-zone` (`internal/kinds/tags.go` `CPStateTopic*` and `DNS*Topic`; `CP_STATE_TOPICS` in `kinds.gen.js`). NIP-01 relays index only single-letter tags, so consumers scope 30900 REQs with `#t`, never `#domain` or `#schema`. The web control-plane read model subscribes to `{kinds:[30900], authors:[service], "#t":[...]}` for exactly the families it routes. Other producers of a routed family (for example the package handlers and the worker-state publisher) must stamp the same topic.
- Content must be a complete current-state snapshot, not a patch.
- DNS zone, policy, endpoint, and backend mutations and ML model, version, and
  endpoint mutations publish through this same envelope. Deletes and old
  coordinates retired by ML identity changes carry `deleted=true` on the
  exact live `(kind, pubkey, d)` coordinate; create/update records carry the
  complete current state and `updated_at` revision. DNS and ML desired state
  is persisted in the daemon's local outbox store; PostgreSQL is an optional
  one-time seed, not the mutation source of truth.
- Two families published by one author must never share a `d`. A relay keeps one event per `(kind, pubkey, d)`, so families that share a coordinate replace each other.
- One family with two writers must have one record shape. The service-registry and environment-registry records are written both by the projector and, before the cache write, by the relay-first registry. Both build them with the projector's builders (`internal/adapters/nostr/control_state_contract.go`) and sign under the projector's per-coordinate `created_at` floor and dedupe memory, through `RelayFirstStatePublisher`. So a projection of the state a relay-first record already carries is not signed again, and the next event on the coordinate is always newer. The relay-first record is delivered with `Publisher.PublishBeforeCommit`. One synchronous round goes to every control-plane relay. Below the publish quorum nothing is enqueued and the cache write is skipped. At the quorum the record is admitted to the control-plane outbox with that round's per-relay results, and the outbox retries only the relays that have not accepted. Their `updated_at` is the entity revision clients send back as `expected_updated_at`. It is written at full precision (RFC 3339 with fractional seconds) and is part of the dedupe fingerprint for these two families, while other families treat it as bookkeeping (`bahia-irsry.41`).

#### Worker cp-state coordinates

Worker read models are `30900` cp-state records in `domain=worker` whose family is the `legacy_kind` discriminator (`kinds.CPStateFamilyWorker*`, `32000`-`32004`, never wire kinds). Every worker family addresses its records under its own prefix, built only by the canonical worker d builder (`kinds.CPStateFamily.WorkerDTag`; the projector's `canonicalStateDTag` and the control plane's worker publishers both call it):

| Family | `legacy_kind` | `t` | `d` |
|---|---|---|---|
| Worker state | `32000` | `worker-state` | `worker:state:<worker pubkey>` |
| Assignment | `32001` | `worker-assignment` | `worker:assignment:<worker pubkey>` |
| Drain | `32002` | `worker-drain` | `worker:drain:<worker pubkey>` |
| Eligibility preview | `32003` | `worker-eligibility` | `worker:eligibility:<preview id>` |
| Cleanup execution | `32004` | `worker-cleanup` | `worker:cleanup:<worker pubkey>:<loom job or start time>` |

A tombstone uses the same coordinate as its live record. The web mirrors the prefixes as `WORKER_*_D_PREFIX` in `kinds.gen.js`, and a drift test keeps them equal. Consumers read a record's id (the worker pubkey or preview id) from the coordinate when the content and `worker` tag omit it, never from the raw `d`.

Before `bahia-irsry.36`, the projector published assignment and drain with a bare `d=<worker pubkey>` under the same author, so on a relay each replaced the other. **Old records on that shared coordinate are ignored, not re-keyed.** The daemon catalog skips a worker record that is not on its family's coordinate. The web applies assignment and drain records only from their family coordinates.

- At most one of the two families survived on each relay, and which one is arbitrary, so the survivor is not trustworthy current state for either family.
- The projector republishes every assignment and drain snapshot from the repository on its own coordinate at startup.
- Re-keying in `internal/nostrmigration` would copy a stale record onto the new coordinate and compete with the fresh snapshot. It would also turn a canonical-to-canonical rewrite into migration work, and migration exists for legacy kinds.

The orphaned records stay on relays (addressable events are never swept) until an operator deletes them.

**Workers are never removed, so nothing publishes worker tombstones.** Bahia has no worker removal flow: `WorkerRepository` has no delete and no `worker/*` ContextVM method retires a worker. `offline` is not a removal. Readers derive it from the age of the last advertisement (`domain.Worker`), and the worker comes back online when it advertises again. A tombstone makes consumers drop the worker (the web deletes it and the daemon cache marks it offline), which would be wrong for a worker that returns. If a decommission flow is added, it must publish tombstones on every coordinate of the worker (state, assignment and drain) through the same builder. The consumer paths for such tombstones already exist and are tested.

Use NIP-78 kind `30078` instead when the object is app-specific data, user/application settings, local UI state, or a registry whose semantics are not a fleet-wide control-plane projection.

#### Config Fabric durable status receipts

Config consumers publish kind `30900`, `domain=config-status`, and
`schema=cascadia.config.status.v2`. Each receipt is a complete fact about one
signed desired config event and one phase, with this address:

`d=config-status:<service>:<policy>:<scope>:<config_event_id>:<status>`

The phases are `accepted`, `applied`, `rejected`, and `withdrawn`. The existing `service`,
`scope`, `version`, `status`, and `e=<config_event_id>` tags match the content.
An `applied` receipt must bind `last_applied_event_id` to `config_event_id` and
`effective_version` to `version`. `accepted` only acknowledges durable admission;
it is not proof of activation. A rejection of a duplicate desired event does
not retract a previously published applied fact.

`withdrawn` (v2 only, with a non-empty `reason`) means the consumer dropped the
desired event because its author deleted it (NIP-09) or it expired (NIP-40).
Pending activation of that event is cancelled, but the consumer keeps enforcing
the last applied config: it does **not** revert the live relay policy, because
an absent membership list or policy document would read as an empty allowlist,
and an empty allowlist admits every pubkey. The withdrawn event keeps its
version floor, so the consumer will not re-accept it. To change the live config,
publish a newer version (or roll back, which republishes older content at a
newer version). Readers report a coordinate as withdrawn only while the
withdrawn event is still the latest desired event; a newer desired version
supersedes the withdrawal.

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


Relay settings operator policy uses canonical state kind `30900` with `d=relay-settings:operator`, `domain=relay-settings`, `schema=bahia.relay-settings.v1` and `t=relay-settings` (`kinds.RelaySettingsTopic`). The state records the current service-authored browser, ContextVM, service, DM, NIP-66 monitor, and NIP-86 managed-target policy after a `settings/relay-policy.apply` ContextVM intent is accepted.

Readers (the daemon hydrator and the web settings store) REQ the policy with `{kinds:[30900], authors:[service], "#d":["relay-settings:operator"]}` and check `domain` and `schema` locally. They never send `#domain` or `#schema`. The policy is one exact addressable coordinate, so `#d` is the narrowest indexed filter. Adding `#t` would AND with it and miss a policy retained from before the topic was stamped.

### 4. Is this an immutable audit fact or attestation?

Use Bahia audit.

- Kind: `4903`
- Required tags: `domain`, `type`, `schema`.
- Include `e` for source/correlation when possible.
- Include `p` for responsible or requesting actors where appropriate.
- Include resource tags such as `service`, `environment`, `artifact`, `worker`, `package`, `dns_zone`, or `run`.
- Never add `d`. 4903 is a regular kind, and every audit is its own fact. The retired addressable audit kinds `31000`-`31099` were published with `d=<entity>`, so each audit of an entity replaced the previous one (audit C-16). Their constants are deleted from `internal/kinds`, the publisher aliases and `kinds.gen.js` (`bahia-irsry.37`). Only `internal/nostrmigration` still names the range, to migrate old audit events onto 4903.
- Correlate a fact with tags instead: `state=<d of the audited entity's cp-state record>`, a single-letter topic (`t=cp-audit` for projector facts, plus `t=<event type>`), and `e=<source event id>` when the fact has a Nostr source.
- Make publication idempotent per source fact. Projector facts carry `fact=<sha256(type, entity, canonical content)>`. The projector signs each fact id once and remembers ids hydrated from retained 4903 records across restarts, so a republished bus event does not create a duplicate fact. Consumers may also drop a second event with a `fact` they have already seen.
- Audit events should be treated as protected and long-retention. They are not normal delete targets. The sidecar keeps regular events durably by default; an operator-set `event_retention` cap bounds that, so compliance evidence needs the cap left unset or archival storage.
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
| `30315` | NIP-38 status | Operational status/progress; continuity heartbeat observations use `#domain=continuity`, `schema=bahia.status.continuity-heartbeat.v1`, heartbeat `d`/`worker` tags, and a NIP-40 `expiration` tag |
| `30316` | Assistant transcript | Service-authored assistant transcript messages on a deterministic `d` (see "Transcript messages" below); content is a service-held symmetric-key AEAD envelope with `key_ref`/rotation metadata mirrored in tags |
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

The service-state `30900` content also carries REST-compatible typed fields when present: `desired_runtime_state` (non-secret snapshot), `reconcile_backoff_until`, and `reconcile_consecutive_failures`. An absent deployment unit omits `deployment_unit_id` rather than encoding an empty UUID. Free-form `reconcile_failure_metadata` is not projected because its diagnostic message can contain private runtime details; CLI reads use `drift_status` exactly as published.

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

### Transcript messages

Assistant transcript messages stay on addressable `30316`. A retried publish must not duplicate a message, and a regular kind cannot give that guarantee: every copy is AEAD-encrypted with a fresh random nonce, so a retry is a new event id, and the relay would keep both copies. On an addressable kind, a deterministic coordinate makes the relay replace the earlier copy. The coordinate is:

- `d=bahia.assistant-transcript.v1:<session>:msg:<logical id>` when the message has a logical id (every production append does), or
- `d=bahia.assistant-transcript.v1:<session>:seq:<20-digit sequence>` otherwise.

The logical id is preferred to the sequence. `AppendMessageOnce` derives the sequence from a replay, so a retry can compute a different sequence, and two concurrent appends can compute the same sequence for different messages. Never mint a random `d`. Readers keep the NIP-01 winner per coordinate (newest `created_at`, then lowest id) and then one record per logical id.

Every message carries two single-letter topics: `t=assistant-transcript` (`kinds.AssistantTranscriptTopic`) and `t=assistant-transcript:<session>` (`kinds.AssistantTranscriptSessionTopic`). The browser REQs its transcript with `authors`, `#p` (the operator) and the kind. The daemon's session replay (`AssistantTranscriptStore.Replay`) REQs `{kinds:[30316], authors:[service], "#t":["assistant-transcript:<session>"]}`. Schema, domain, session, turn and role are checked locally after decryption and are never sent as multi-letter tag filters, because relays index only single-letter tags.

Messages published before the topic was stamped (before `bahia-irsry.37`) do not match the daemon's replay REQ, so a session that predates it rebuilds model history only from messages appended since. The browser's `#p` REQ still shows them.

Assistant status (`30315`, `schema=bahia.assistant-status.v1`) carries `t=assistant-status` (`kinds.AssistantStatusTopic`). The browser REQs it with `{kinds:[30315], authors:[service], "#t":["assistant-status"], since}` and checks the schema locally.

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
- Fleet-private execution state: OCK-encrypted `30900` cp-state topics `security-target` (`legacy_kind=32020`), `security-run` (`32021`), `security-schedule` (`32013`), `security-finding` (`32012`), and `security-finding-detail` (`32014`). The target supplies restartable scan input; the deterministic run coordinate is the idempotent schedule claim and durable progress record. These numbers are family discriminators, never wire kinds.
- App-specific details: NIP-78 `30078` with `schema=bahia.security.findings.v1` for normalized public-safe findings.
- Audit and policy breach evidence: `4903` with `schema=bahia.audit.security.v1`.

### Adoption

Adoption (audit B-35) is an intent-driven workflow whose output is relay-canonical per resource. Each adopted workload publishes, in order, an `adoption-binding` cp-state record (`legacy_kind=32026`, `d=adoption:binding:<service-id>:<environment-id>`, `status=in_progress`), the `environment-registry` record with its deployment units, the `service-registry`, `build-registry` and `artifact-registry` records, OCK-encrypted `secret-registry` references for imported sensitive environment values (never their values), the `runtime-observation` and `service-state` records, and the binding again with `status=complete`. Every id is derived from the signed request (org, host alias, container target, image digest, intent id), so re-processing the same `30900` intent after a crash or a rejected publish re-addresses the same coordinates and completes only what is missing; the in-progress binding keeps a partial adoption visible. A publish failure is returned as a `30315` rejection whose `data` carries per-candidate progress, and the intent is not marked processed. Postgres is a rebuildable index written after the records are admitted. The binding discriminator is never a wire kind.

Security scans triggered by SBOM production subscribe to existing SBOM `30078` reference and `30004` availability events with exact `#domain=sbom`, `#schema`, subject, and service-author filters. The scanner treats `EOSE` as historical catch-up completion, keeps realtime subscriptions open when needed, handles `CLOSED` and `AUTH`, verifies inbound event signatures and hashes before trust, and verifies relay `OK` for every Security observable it publishes.

Finding detail parts use fixed `security:finding-detail:<hash>:part:<n>` coordinates. For multipart details the daemon publishes every part first and then replaces the base `security:finding-detail:<hash>` manifest with the authoritative `total_parts`; readers ignore parts outside that manifest. A base tombstone means no detail, even if older part coordinates remain retained.

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

## Entity identity for create paths

Normative companion to [event-spec "Entity identity and coordinates"](event-spec.md#entity-identity-and-coordinates) (bahia-irsry.35). The author of a create fixes the entity id. It is the entity segment of every addressable coordinate for that entity, so it must exist before the first relay publish or database write.

**Minting (clients).**
- Mint one UUIDv7 per create *attempt*: web `mintEntityId()` in `web/src/lib/entity-id.js`, Go `domain.NewEntityID()`.
- Keep that id for every retry of the same attempt, e.g. a timeout followed by "Create" again. Mint a new id only when the user starts a new entity (the form resets).
- Never derive an id from a name (UUIDv5 or a hash of `org:slug`). A predictable id lets anyone pre-claim the coordinate.
- The web stores `createService`/`createEnvironment`/`createPolicy`/`createLLMRoute` add an id when the caller passes none (`withEntityId`). Dialogs and pages pass their own id so retries stay idempotent.
- The operator client (`pkg/client`) mints in `CreateServiceNostr`/`CreateEnvironmentNostr`; the CLI mints first and prints the id (retry with `--id`). MCP create tools take an optional `id` and include it in their derived idempotency key.

**Validating (servers).**
- Parse the intent's optional `id` with `domain.ResolveCreateEntityID`. It accepts canonical lowercase UUIDv7/v4, mints a UUIDv7 when the id is absent, and wraps `domain.ErrInvalidEntityID` otherwise; the handler reports it as `invalid id: …`.
- Validate before authorization side effects and before any publish.

**Building and decoding coordinates.**
- Build `d` with the projector's coordinate builders (`canonicalStateDTag` and the family builders in `internal/adapters/nostr`). They are input-agnostic: they never ask who minted the id, so v7 and legacy v4 ids produce coordinates of the same shape.
- A coordinate builder takes the entity id. It must not take a row or a database sequence.
- Consumers read the entity id from `content.id` and accept any UUID version (`parseProjectionUUID` in the relay projection cache). Existing v4 coordinates therefore keep decoding.

**Resolving a create (idempotency).**
1. Apply the write-path defaults and normalization, including the id.
2. Look the id up. If it is absent, create.
3. If it is present, compare the declared content: every field except the id and timestamps, including the explicit deployment-unit set for environments.
   - Equal: return the stored entity and write, publish and emit nothing.
   - Different: return `*domain.EntityIDConflictError`, which is `errors.Is(err, domain.ErrEntityIDConflict)`. The ContextVM transport maps it to JSON-RPC `-32010`; the web checks `isEntityIdConflict(error)`.
4. Relay-first paths (`service.RelayFirstRegistry`) resolve the id *before* publishing. A conflicting create must never replace the existing coordinate, and an idempotent retry must not republish. The registry serializes check → publish → store per id within the process, so two concurrent creates of one id cannot both publish.
5. Repositories store the supplied id verbatim. They mint (UUIDv7) only when an internal caller passes none. A primary-key unique violation is reported as `repository.ErrAlreadyExists` and resolved by the service layer as in step 3; a name unique violation is `repository.ErrConflict`.

**Uniqueness without a central database.**
- Ids are unique by construction. Natural keys such as `(org, name)` are constraints the authoritative applier enforces, never identity.
- In Phase 3 the intent applier orders competing creates by `(created_at, event id)`. It applies the first and publishes a rejection status, referencing the loser's intent event id, for a later create whose id conflicts or whose natural key is taken.
- Authorization is checked against the org named in the intent. The stored org is part of the compared content, so an id can never reach into another org.

**Adopting the rule in another domain.**
- Add `id` to the create intent.
- Resolve it with `ResolveCreateEntityID` in the handler.
- Add a `replay<Entity>Create` check in the service with `resolveCreateByID` and a content fingerprint (`canonicalCreateContent`), see `internal/service/create_identity.go` and its uses for policies and LLM routes.
- Classify the primary-key violation in the repository.
- Mint once per attempt in the web store/dialog.
- Cover the four cases in tests: id round-trips to the coordinate, same-content retry, different-content conflict, and absent id minted.

## Publication, replay, and retention invariants

- For service-authored events using `internal/adapters/nostr.Publisher`, persist the fully signed event as a pending `nostr_events` outbox row before relay delivery. Mark it published only after an accepted or duplicate relay `OK`; retain and retry failures with backoff.
- Do not generalize that outbox guarantee to every relay pool or client publisher. A caller request with zero accepted relays is not accepted, and a ContextVM receipt is not terminal business truth.
- Sidecar persistence precedes `OK`. Subscriber fanout must remain off the acknowledgment path so a slow subscriber cannot stall writes.
- Replay filters are answered from the eventstore's kind, author, tag and time indexes, and each query is capped at `max_query_limit`; `EOSE` ends only the bounded query, so clients that may hit the cap must narrow resource/time filters, overlap windows, and deduplicate, or reconcile with NIP-77.
- Retain ContextVM transport (`request_retention_kinds`, default `25910`, `1059`, `21059`) according to `request_retention`. Regular events such as `4903` audits are durable unless `event_retention` caps them; replaceable, addressable and kind-5 events are never age-swept.

## Migration app rules

Bahia has already deployed older events. Legacy events are migrated by the offline `bahia-migrate nostr` tool (`internal/nostrmigration`) rather than by keeping legacy runtime behavior alive. It is not on the daemon startup path.

Implementation rules:

1. Legacy subscriptions, decoders, and transforms belong in `internal/nostrmigration` or tests for that module.
2. The migration must be idempotent. Re-running `bahia-migrate nostr` must not duplicate canonical events.
3. Migrated events must publish canonical `kind` values and may include metadata tags such as `legacy_kind`, `migrated-from`, `migration`, and `schema`. Migrated worker read models (retired `32000`-`32003` and the `Legacy*Worker*` aliases) land on their family's canonical coordinate (`worker:<entity>:<id>`, see "Worker cp-state coordinates"). They compete with the live record under NIP-01 replacement instead of sitting on a per-event `worker:migrated:<id>` coordinate. A record that names no worker keeps the per-event coordinate.
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
- [ ] Create paths take a client-minted entity id and resolve retries by content; never let a database mint an addressable `d` (see [Entity identity for create paths](#entity-identity-for-create-paths)).
- [ ] Verify relay `OK`, duplicate-`OK`, `CLOSED`, and `AUTH` paths; verify outbox state only when using the outbox-backed publisher.
- [ ] Subscribe with scoped filters and handle EOSE as historical catch-up, not completion.
- [ ] Update docs when event kinds, tags, schemas, or migration behavior change.
- [ ] Create Beads for deferred work rather than leaving comments or TODOs.

If the implementation wants a new event kind, stop and write the kind-allocation justification first. In most cases the correct fix is a ContextVM method, NIP-51 list, NIP-38 status, `30900` projection, `30078` app data event, NIP-58 badge, or ContextVM discovery announcement.

### Managed-instance supervisor observables

The managed-instance health projector subscribes to internal runtime health, recovery, and maintenance events. It publishes NIP-38 kind `30315` status with schema `bahia.status.managed-instance-health.v1` and stable `d=runtime:instance:<service>:<environment>:<deployment-unit>:<sha256(runtime-target)>`, kind `30900` current state with schema `bahia.state.managed-instance-health.v1`, and immutable kind `4903` audit facts with schema `bahia.audit.managed-instance-health.v1`. Evidence is sanitized before projection and publication uses the signed durable outbox/relay-OK path. No polling or new event kind is introduced.

### Route canary observables

The route canary projector subscribes to in-process route observations (`route.canary_observed`) and transitions (`route.canary_outage_opened`, `route.canary_recovered`, `route.canary_classification_changed`). Each observation refreshes `30315` and `30900`; only transitions add an immutable `4903` fact. No new Nostr wire kind is introduced.

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

## F74a MCP read families (30900)

The legacy-kind discriminator is a catalog key, not the wire kind. Each record
uses the shared `bahia.cp-state.v1` envelope, `t` topic, daemon author, and a
stable `d`; a delete publishes `deleted=true` on the same coordinate.

| Family | Legacy kind | `t` | `d` | Content / read auth |
|---|---:|---|---|---|
| LLM release | 32015 | `llm-release` | `llm:release:<id>` | Fleet-OCK encrypted; public ciphertext |
| Artifact signature | 32016 | `artifact-signature` | `artifact:signature:<id>` | Plaintext supply-chain record; public |
| Artifact SBOM | 32017 | `artifact-sbom` | `artifact:sbom:<id>` | Plaintext manifest details; public |
| SBOM package | 32018 | `artifact-sbom-package` | `artifact:sbom-package:<id>` | One package per indexed record; public |
| Latest runtime observation | 32019 | `runtime-observation` | `runtime:observation:<service-id>:<environment-id>` | Minimal non-secret snapshot; classified protected (NIP-42 enforced in `read_auth_mode=enforce`) |

SBOM references and availability remain their existing `30078` and `30004`
interop records; these 30900 families add the parsed manifest and package index
needed by MCP. Publishers use the shared cp-state signing/outbox path and reject
oversized records; an SBOM package list is never emitted as one event.
A durable outbox control marker drives one-time startup backfill of pre-existing
repository records. A failed post-commit projection marks the family dirty so
the next startup retries the backfill before MCP serves these reads.

### Wave F75 operator views (bahia-irsry.75)

The web reads managed-instance health, route canaries, Blossom administration, and Soul Factory runtime policy from verified BahiaEventStore events. The first two retain their existing `30315`/`30900` current-state projections and `4903` audit facts, now indexed by `t=runtime-instance-health` and `t=route-canary`. Every material health observation has one immutable audit fact; each route probe refreshes current state and only transitions create audit facts. Both streams are bounded in the browser with a seven-day audit backfill and at most 500 audit events per subscription.

| Family discriminator | Topic | 30900 coordinate | Content | Read authorization |
|---|---|---|---|---|
| 32040 managed-instance health | `runtime-instance-health` | `runtime:instance:<service>:<environment>:<unit>:<target-sha256>` | sanitized `health` | public sanitized operational signal |
| 32041 route canary | `route-canary` | `route:<service>:<environment>:<unit-or-none>:<hostname>` | full sanitized `route_canary`, observed instance status, contradiction flag | public sanitized operational signal |
| 32042 Soul runtime policy | `soul-factory-runtime-policy` | `soul-factory:runtime-policy` | `agent_runtimes` from validated daemon config; when SoulFactory is enabled also `controller_pubkeys` (the resolved controller identity) and, when pinned, `runtime_pubkeys` (`soul_factory.runtime_pubkeys`). Browsers use these as the trust root for `31951`/`6950`/`7950`/`1951` and `30317` authors. Content is plaintext, so these public keys are disclosed to every reader of the topic | member-authenticated on the Bahia relay sidecar when `read_auth_mode` is `enforce`; readable by anyone in `warn` (the default) or `off`, and on any other relay |
| 32043 Blossom administration | `blossom-admin` | `blossom:admin` | OCK-encrypted configured servers and observed health | ciphertext public, fleet OCK required |
| 32044 Blossom blob | `blossom-blob` | `blossom:blob:<owner-pubkey>:<sha256>` | OCK-encrypted BUD-02 descriptor and owner | ciphertext public, fleet OCK required |

#### Supervision state read from the local event store (B-33, B-34)

Route-canary and managed-instance supervision decide from the daemon's own canonical `30900` records in the local event store, never from PostgreSQL. PostgreSQL rows are a write-behind query index written after the canonical record; a failed index write is logged and blocks nothing.

| Input | Canonical record |
|---|---|
| Routes to probe, instances to supervise | `service-state`, `service-registry` and `environment-registry` records (latest version per coordinate; a tombstone or a replacement without the route or desired runtime state leaves the set at the next sweep) |
| Route failure streak, outage start ("failing since") | `bahia.state.route-canary.v1` at the route coordinate |
| Instance health before the current observation | `bahia.state.managed-instance-health.v1` at `runtime:instance:<...>` |
| Restart budget, backoff history, pending recovery claim | `bahia.state.managed-instance-recovery.v1` at `runtime:recovery:<service>:<environment>:<unit>:<target-sha256>` |
| Maintenance override | `bahia.state.managed-instance-maintenance.v1` at `runtime:maintenance:<service>:<environment>:<unit>:<target-sha256>` |

The recovery ledger and the maintenance record are two further schemas of the `32040` family. Both are `30900` events tagged `domain=runtime`, `t=runtime-instance-health`, `legacy_kind=32040`, `service`, `environment`, `deployment_unit` and `target`, with `entity=managed-instance-recovery` or `entity=managed-instance-maintenance`:

```json
{"schema":"bahia.state.managed-instance-recovery.v1","attempts":[{"id":"<uuid>","service_id":"<uuid>","environment_id":"<uuid>","deployment_unit_id":"<uuid>","runtime_target_name":"api","correlation_id":"<sha256>","requested_at":"<rfc3339>","result":"pending|success|degraded|failed|budget_exhausted|skipped_override","evidence":"<sanitized>"}]}
{"schema":"bahia.state.managed-instance-maintenance.v1","active":true,"override":{"id":"<uuid>","service_id":"<uuid>","environment_id":"<uuid>","deployment_unit_id":"<uuid>","runtime_target_name":"api","actor":"<sanitized>","reason":"<sanitized>","created_at":"<rfc3339>","expires_at":"<rfc3339, optional>"}}
```

The ledger is one replaceable record per instance holding its newest 100 attempts (a pending attempt is never dropped), so canonical state grows with the number of instances, not with the number of restarts; the immutable history of each attempt remains its `4903` audit facts. An attempt is identified by its `correlation_id`, which is derived from the failure generation: recording it again publishes nothing. A rewrite of either record is signed with a `created_at` strictly after the version it replaces. Clearing an override republishes the record with `active=false`. Consumers that list instance health must select `bahia.state.managed-instance-health.v1` (content carries `health`); the web operator view already does.

Both supervisors start their periodic work only after the bootstrapper's first relay catch-up of the local event store. On first start after an upgrade, an instance that has recovery attempts or an active override in PostgreSQL and no canonical record gets them published once (design Phase 3 §4.2); afterwards PostgreSQL is not read. With `nostr.publish_enabled=false` no canonical route-canary record exists, so route state lives in the process and, when configured, in the PostgreSQL index it then resumes from.

Blossom metadata is published once after a successful daemon-owned upload and once per configured owner at startup. Raw blob retrieval remains HTTP as required by Blossom. The UI's owner filter applies to already-published metadata; it does not issue a new server-side listing for arbitrary pubkeys. Soul policy is published at daemon startup and replaced only when a new daemon config is started.

Config Fabric does not add a family: the browser computes current desired/applied drift from signed kind `30000`/`30078` desired events tagged `config-fabric` and service-authored `30900` status tagged `config-status`, mirroring `ConfigDriftFromEvents`. NIP-01 addressable replacement means a cold relay provides only current desired and status events, not historical versions or receipts; the web cannot reconstruct the REST archive's full version/status history after a cold start.

## F74b canonical fleet-private state (bahia-irsry.74)

Five logical families share wire kind `30900`, `schema=bahia.cp-state.v1`, and
a `#t` topic; their `legacy_kind` discriminators are not emitted as wire kinds.

| Legacy discriminator | `#t` | Addressable `d` |
|---|---|---|
| `32030` package intent/claim/approval | `package-intent` | `package:intent:<request event ID>`, `package:claim:<request event ID>`, `package:approval:<UUID>`, or `package:signed-intent:<SHA-256(intent ID)>` |
| `32031` tool provisioning intent | `tool-provision-intent` | `tool:intent:<UUID>` |
| `32032` tool denylist policy | `tool-denylist` | `tool:denylist:<SHA-256(manager\0package)>` |
| `32033` tool profile | `tool-profile` | `tool:profile:<service UUID>:<environment UUID>` |
| `32034` notification delivery log window | `notification-log` | `notification:log:<channel UUID>` |

All content is fleet-OCK encrypted because requests may contain private source
URLs, tool entries encode operator policy, and delivery logs may contain
recipient details. Relay read auth classifies the topics as public **ciphertext**;
MCP decrypts with the daemon service key. Deletion uses the same coordinate with
`deleted=true`. The notification event is one bounded replaceable window per
channel (at most 50 attempts, 512 JSON bytes per payload, 256 error characters,
60 KiB plaintext), never one addressable event per append-only log line.
Mutation-bound repository decorators publish after successful persistence via
`publishControlState`; the legacy mutation path is not duplicated.

## Governed Soul Factory provisioning records (C-45, bahia-nfc95)

Governed provisioning keeps no local-file authority. Two `30900` families,
both authored by the daemon service key and read back from its local event
store (design §3.3, §3.6, §6.2), carry everything a daemon moved to a fresh
host needs to resume, operate or replay a run. The state directory
(`soul_factory.provisioning_state_dir`) holds only caches of these records,
the per-request lock files and the Signet enrollment state.

| Legacy discriminator | `#t` | Addressable `d` | Content |
|---|---|---|---|
| `32025` saga run | `soul-factory-saga-run` | `soul-factory:saga-run:<SHA-256(request event ID)>` | plain `bahia.state.soulfactory-saga-run.v1` (stage, resume stage, version, one-way resource references, compensations, sanitized failure, newest 16 transitions/failures with totals); protected topic |
| `32028` adapter ledger, request | `soul-factory-adapter-ledger`, `record=request` | `soul-factory:adapter-request:<SHA-256("request\0" + request event ID)>` | fleet-OCK `bahia.confidential.aead.v1`; event tag `schema=bahia.state.soulfactory-adapter-request.v1` |
| `32028` adapter ledger, identity | `soul-factory-adapter-ledger`, `record=identity` | `soul-factory:adapter-identity:<SHA-256("agent\0" + agent ID)>` | fleet-OCK `bahia.confidential.aead.v1`; event tag `schema=bahia.state.soulfactory-adapter-identity.v1` |

Every record is published outbox-first (a publish the outbox keeps for retry
counts) before the checkpoint or adapter step that produced it is reported
durable, with a `created_at` strictly after the record it replaces. Removal is
a tombstone at the same coordinate (`deleted=true`, empty content, `version`
tag) which retires every copy at or below that version; purging a saga run
tombstones the request's ledger record with it. Both ledger records carry a
`version` tag; the request content is refused before publish when it would
exceed the event store's 65535-byte content cap. Precedence on read is the
highest version, a tie going to the copy the process committed, then the
record, then the file cache; a pre-canonical file (version 0) is resumed from
once and the next save publishes version 1.

The adapter ledger's public tags are only `d`, `domain`, `schema`, `entity`,
`t`, `legacy_kind`, `deleted`, `record` and `version`: no agent id, request id,
run id or stage is readable without the fleet key. Confidentiality of the
request content, field by field (fleet-visible = OCK layer any fleet operator
decrypts; service-only = `service_inner`, NIP-44 to the service key):

| Field | Classification | Why |
|---|---|---|
| `request_id`, `run_id`, `agent_id`, `spec_hash`, `runtime`, `request_method`, `version`, `prepared`, `identity_created`, `active_soul_published`, `success_delivered`, `terminal_result_stage` | fleet-visible | correlation identifiers and progress flags; the ids are public event ids already |
| `request` (the resolved kind-5950 request) | fleet-visible | the public request, retained so a replay uses the exact input |
| `resolved` (template, draft, fleet-config snapshot, identity/persona/runtime/relay/workspace specs) | fleet-visible | names private workspace and runtime targets; the fleet-config validator refuses literal secret values (placeholders only); the Signet identity contract (bunker URL) is excluded from the payload |
| `soul` (Soul projection: pubkey, npub, NIP-05, persona markdown, permissions, asset references, runtime binding, readiness evidence) | fleet-visible | the same projection the public `31951` carries; `bunker_uri` is always empty |
| `soul.bunker_uri` | out of the record | one-time NIP-46 handoff secret; consumed by the runtime step and the Signet enrollment manager, never persisted |
| `service_id`, `environment_id`, `deployment_unit_id`, `deployment_intent_id`, `release`, `release_source`, `release_binding` | fleet-visible | internal registry identifiers and release provenance |
| `runtime_result` (kind-38386 envelope) | fleet-visible | the runtime's signed plaintext relay event, retained as evidence |
| `steps` (per-step observed resources) | fleet-visible | one-way resource references, ownership and correlation |
| `success_result_id` | fleet-visible | names the retained result |
| `success_result` (the signed kind-6950 event) | service-only | a signed, possibly undelivered event is deliverable by whoever holds it; delivery is the daemon's |

The identity record (`schema`, `agent_id`, `spec_hash`, `request_id`,
`run_id`, `created_at`, `version`) is wholly fleet-visible. Neither record
holds a secret value; keys, tokens and bunker URIs never enter the ledger.
Relay read auth treats both topics as protected (the ledger is ciphertext in
any case).

## Policy evaluation intent and build ownership (bahia-irsry.77)

For MCP evaluation, use a client-signed `30900` `domain=policy`, `op=evaluate`
intent with `d=evaluation:<artifact-uuid>:<environment-uuid>` and JSON content
`{"artifact_id":"<uuid>","environment_id":"<uuid>"}`. The daemon evaluates
its signature, SBOM, scan, and attestation repositories with the same
`PolicyService.Evaluate` semantics as the legacy ContextVM method. It emits a
requester-scoped, replaceable `30315` status at
`d=intent-status:<requester-pubkey>:<evaluation-coordinate>`; an accepted
status has `result=evaluated` and an `evaluation` object. The status payload
is capped at 16 KiB. A rejected evaluation is not an allow decision.

Build registration and status are daemon-authored from `build/request` and
trusted Hive-CI `5401`/`5402` evidence. MCP manual build writes are removed;
F1 removes the compatibility REST writes `POST /builds` and
`PATCH /builds/{id}/status`.
