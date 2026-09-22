# Virtual machines and execution planes

## Availability and lifecycle boundaries

The virtualization public surface exposes typed hosts, immutable pinned images,
persistent deployments, execution planes, coordinated checkpoints/exports, and
operations. It does not infer lifecycle from a VM name:

- `persistent_vm`: Bahia owns the desktop/service VM lifecycle.
- `loom_firecracker_job_microvm`: Loom owns each ephemeral Firecracker job.
- `loom_qemu_job_domain`: Loom owns each ephemeral QEMU/libvirt job.

Bahia manages execution-plane package/config/image pins, capacity and probes, not
Loom job domains. Desired capability lists are not capability grants. Only D's
fresh authenticated probe contribution may grant scheduling eligibility; failures,
expiry and session replacement retract it. Windows QEMU job capability is never
advertised. A returned probe-success flag is historical evidence, not eligibility.

**Integration status:** queries and public projections are composed in the app.
Mutation methods return `virtualization unavailable` until the orchestrator
supplies the C/D admission adapters in `VirtualizationDependencies.PersistentVM`
and `.ExecutionPlane`. There is no direct repository/provider or host-shell
fallback. VM host/image installation, desktop pilots and soak are separate work;
passing portable tests is not live-host acceptance.

## Reads and authorization

Every read requires an authenticated public-key principal and organization
`deployments:read` permission, including installations with global HTTP auth
disabled. Missing authorization dependencies fail closed.

| Resource | ContextVM reads | REST compatibility path under `/api/v1` |
|---|---|---|
| Host | `virtualization-host/list`, `/get` | `/virtualization-hosts[/{id}]` |
| Image | `vm-image/list`, `/get` | `/vm-images[/{id}]` |
| Persistent VM | `persistent-vm/list`, `/get` | `/persistent-vms[/{id}]` |
| Execution plane | `execution-plane/list`, `/get` | `/execution-planes[/{id}]` |
| Checkpoint | `vm-checkpoint/list`, `/get` | `/vm-checkpoints[/{id}]` |
| Export | `vm-export/list`, `/get` | `/vm-exports[/{id}]` |
| Operation | `vm-operation/get` | `/vm-operations/{id}` |

Supply `org_id` on every query, and `id` for ContextVM get. Lists accept `limit`
(default 50, maximum 100) and nonnegative `offset`. REST uses query parameters.
There are no virtualization REST mutations or unauthenticated inventory routes.

```json
{"jsonrpc":"2.0","id":"inventory","method":"persistent-vm/list","params":{"org_id":"<organization UUID>","limit":50,"offset":0}}
```

Responses use a versioned public envelope: `schema_version`, `id`, `org_id`,
`generation`, `resource_kind`, explicit `lifecycle_classes`, `updated_at`,
`deleted`, and the applicable `host`, `image`, `vm`, `plane`, `checkpoint`,
`export`, or `operation` detail. `actor`, when present, is a validated-format
public key. Nil runtime state means never observed, **not absent**. Desired power,
runtime state, observation availability, drift, ownership and guest health remain
separate. Artifact deletion is an explicit tombstone, not missing data.

## Mutation intents and approvals

Supported ContextVM intents are `vm-image/register`,
`persistent-vm/create|update|operate`, `execution-plane/create|update|reconcile`,
and `vm-operation/approve|cancel`. They require organization
`deployments:write`; C/D additionally authorize the exact action, expected
generation, idempotency key and approval tier. Host/quota governance is not exposed
as a bypassing create/update endpoint.

```json
{"jsonrpc":"2.0","id":"start-vm","method":"persistent-vm/operate","params":{"org_id":"<organization UUID>","id":"<VM UUID>","expected_generation":3,"idempotency_key":"<unique request key>","operation":"start","reason":"operator requested start"}}
```

Canonical `_meta.progressToken` is accepted and normalized to the service's
idempotency key; when also supplied, `idempotency_key` must match it. Transport
metadata is not forwarded as resource input.

Create/update bodies carry typed `vm`, `image`, or `plane` desired documents.
Nested tenant/creator spoofing and caller-supplied observations are rejected.
Resolved credentials are never valid bootstrap input: use authorized SecretRefs.
Tier-2 destructive approvals remain service-owned, bound to the exact request,
resource generation and provider fingerprint; a caller cannot self-grant a role
or approval by passing fields in the ContextVM body.

Acknowledgments contain `status=accepted`, `resource_id`, `operation_id`,
`generation`, signer `author`, `org_id`, `state_kind`, `audit_kind`, `state_d_tag`,
and a VM `operation_d_tag` where applicable. This is durable admission, **not
provider completion**. Plane and image-registration operation IDs are admission correlation IDs, not
Loom job IDs or persistent-VM operation coordinates. Subscribe before submitting and follow canonical state/audit truth.

## Public connection metadata, not credentials

Only validated protocol/address/port/username/resource-ID connection metadata is
returned. Addresses cannot contain URI userinfo, paths, queries or fragments;
connections must match the VM resource ID. Console connections carry only the
resource reference. No console stream content is part of this API.

Public DTOs, state and audit content omit bootstrap bindings, arbitrary labels,
raw provider diagnostics, management endpoint configuration, storage references,
file paths, command lines, tokens and private keys. Digest fields are validated
SHA-256 values. Checkpoint/export component sizes and digests are public; storage
locations and access-policy configuration are not. Raw evidence requires a
separate authorized service path and is not exposed by these queries.

## Canonical observables and recovery

No new numeric kind is allocated:

- State: `30900`, `schema=bahia.state.virtualization.v1`.
- Audit: `4903`, `schema=bahia.audit.virtualization.v1`.
- Both: `domain=virtualization`, explicit `entity`, `org`, `generation`, `sequence`
  and one `lifecycle_class` tag per supported class. Operation facts carry a UUID
  `correlation`; valid public actors carry `p`.
- State `d`: `virtualization-host:<uuid>`, `vm-image:<uuid>`,
  `persistent-vm:<uuid>`, `execution-plane:<uuid>`, `vm-checkpoint:<uuid>`,
  `vm-export:<uuid>`, or `vm-operation:<uuid>`.
- Audit has `type`, `state=<coordinate>`, `protected=true` and no `d`.

The envelope has `schema`, journal `sequence`, `change_type`, `occurred_at`,
optional approval ID, and the public `resource`. Audit preserves every journal
fact; state coalesces to the newest coordinate revision per replay page.

C/D publish `events.VirtualizationChange` after committed mutations using
`EventVirtualizationResourceChanged`, `EventVMOperationTransitioned`, or
`EventExecutionPlaneProbeChanged`. A detected gap emits
`EventVirtualizationProjectionGap`. These are wakeups, not authority. The
projector drains `ListChanges` at startup and on every signal, including zero or
unknown sequence signals. Its durable cursor uses the existing cursor store in
an author/organization-specific `virtualization:` namespace. There is no DB
LISTEN/NOTIFY producer and no periodic journal polling.

Signing/outbox persistence precedes cursor advancement. Relay rejection leaves
the same signed event pending for the established durable publisher; replay
recognizes that event instead of signing another. Same-coordinate state updates
are serialized and rate-limited across second boundaries to avoid NIP-01 tie
ordering. A deployment runs one active virtualization projector per signer;
active-active projection requires externally serialized ownership.

Clients should scope subscriptions by kind, author, `#d` (state) or `#state`
(audit), and organization. Validate event IDs, signatures, timestamps and schema;
deduplicate by event ID, reject older journal sequences/generations, use EOSE for
catch-up, and handle CLOSED/AUTH plus reconnect resubscription. Relay tags are
filters, not access control: all projected data is deliberately public-safe.

## Metrics

`bahia.virtualization.*` OTel instruments are mirrored as
`bahia_virtualization_*` on the existing metrics endpoint. Repository-derived
aggregates refresh at startup and committed-change signals: desired/observed
counts, allocations/reservations/free capacity, quota headroom, measured usage,
observed ownership/orphans, guest/probe freshness, plane drift and checkpoint
size/age. Removed aggregate series receive zero. Freshness ages describe the
collection instant; use `observation_timestamp_seconds` and scrape time for
elapsed-age alerts (the value is the oldest observation in each label group).

C/D must call `RecordVirtualization` for operation counts/durations, admission
rejections, live inventory failures, probe outcomes/latency, effective capability
counts/retractions, console streams/bytes/failures, and checkpoint/export outcomes.
Use `EndVirtualizationOperation` instead of passing raw provider errors to
`EndOperation`. Labels use closed provider/class/state/operation/result/reason
categories; no UUID, digest, endpoint, path or error text labels are accepted.
Capacity vCPU is cores, memory/storage are bytes; checkpoint age and freshness
are maximum ages, not sums. Public probe success is not counted as effective
capability: D owns that metric's verified eligibility input.
