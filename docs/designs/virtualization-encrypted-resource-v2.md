# Encrypted canonical virtualization resource v2

Status: design contract, **not an enabled runtime path**. The signed v1
`vm-operation` ingress continues to reject, and the SQL-journal projector and
provider remain disconnected. This extends the
[signed-intent cutover](virtualization-signed-intent-cutover.md), not the public
v1 projection in [the event specification](../event-spec.md#virtualization).

## Authority and wire shape

An operator registers or updates an author-minted UUIDv7 resource by creating
a kind-`30900` **unsigned NIP-59 rumor** with `domain=virtualization`,
`schema=bahia.intent.virtualization-resource.v2`, `op=register|update|delete`,
`t=bahia-intent`, `t=virtualization`, `intent_id=<uuidv7>`, and
`d=virtualization-resource:<kind>:<resource-id>:<intent-id>`. The **entire**
rumor is bound to the operator by a **signed NIP-59 seal**, then stored as a
`1059` gift wrap to the service key; only the outer `p=<service-pubkey>` is
relay-indexable. A signed inner event is not a valid NIP-59 rumor in Bahia:
`IntentGiftWrapIngress.UnwrapIntent` explicitly rejects a nonzero rumor
signature. The inner content is strict, size-bounded JSON (unknown fields
and trailing data rejected):

```json
{
  "schema": "bahia.virtualization-resource-request.v2",
  "intent_id": "<uuidv7>", "org_id": "<uuidv7>",
  "resource_kind": "host|image|persistent_vm|execution_plane",
  "resource_id": "<uuidv7>", "expected_generation": 0,
  "expected_state_event_id": null, "expected_state_digest": null,
  "resource_version": 1, "resource_digest": "sha256:<hex>",
  "descriptor": {}, "execution_spec": {},
  "operator_reason": "<bounded text>"
}
```

`register` requires generation 0 and no predecessor. `update`/`delete`
require the exact current **service-signed v2** state event ID, generation and
`expected_state_digest`; the new `resource_version` and generation are each
exactly predecessor + 1 (both start at 1 on registration). The request's
digest is SHA-256 of domain-separated, RFC 8785 canonical JSON of
`{schema, op, org_id, resource_kind, resource_id, resource_version,
descriptor, execution_spec}`. It is checked *after* unwrapping and never a
public tag.
The rumor's `org` tag, if retained by the shared parser, must equal content
`org_id`; neither it nor resource IDs escape in outer tags. The ingress must
introduce a v2-only `VerifiedWrappedVirtualizationIntent` provenance object
created **only** after `nip59.GiftUnwrap` verifies outer ID/signature, recipient,
seal signature, seal-author/rumor-author binding and rumor ID. It carries the
outer ID, rumor ID, verified seal author and immutable rumor bytes into a
dedicated v2 validator; the validator does **not** call v1
`validateVirtualizationOperationIntent` or pretend the rumor has a Schnorr
signature. Trust-set and org RBAC checks use the verified seal author, and
reject any mismatch with the rumor pubkey or body. Neither
`ProcessUnwrappedIntent`'s signed-inner branch nor direct `ProcessInProcess`
admits v2. A request is eligible only after its exact wrap is durably observed
from the relay subscription; an in-process/SQL request is not equivalent
authorization. NIP-40 expiration limits new admission, not replay of an
already accepted operation.

The full serialized rumor is capped at **16,384 bytes before wrapping** and
the final stored `1059` ciphertext at **60,000 bytes**, below the relay's
65,535-byte content limit after both NIP-44 padding layers. Reject before
publish if either bound fails; never substitute ephemeral `21059`, which
cannot be replayed after a crash. Bound `components` to 32 entries,
`bootstrap` and `image_pins` to 16 each, labels to 32 bounded key/value
pairs, and UTF-8 reason to 512 bytes; fixed-length SHA-256 digests and UUIDs
only, with no arbitrary provider JSON. Apply the same size checks to v2
operation and approval rumors, and test actual Go and web wrappers at the
limits rather than assuming plaintext size predicts ciphertext size.
The service also bounds each resulting OCK-plus-`service_inner` state event
content to 60,000 bytes before signing; overlong state is rejected, not
fragmented into an incomplete executable record.

`descriptor` is an allowlisted **org-readable** summary: kind, provider class,
host/image/deployment references, desired power, capacity, digests and
non-secret display metadata. Do not reuse the v1 public DTO as the execution
record: it omits provider identity, component storage refs, configuration and
bootstrap bindings. `execution_spec` is the validated, complete private
snapshot needed after restart: exact `VMResourceIdentity` (installation,
org, host, deployment, provider-resource IDs and lifecycle class), image
manifest/component digest and opaque storage refs, host/endpoint/trust-policy
refs, network/TPM/firmware/config inputs, and secret **version references**
for bootstrap. It has no plaintext secret value, credential, absolute host
path, URI, provider XML or executable. The installation's local, validated
configuration resolves refs to provider binaries, storage pools, paths and
endpoints; missing/mismatched local configuration suspends execution. The
provider validates image provenance and every resolved input before work.
All fields are explicit per kind, with no open-ended `map[string]any` provider
payload. Registration, update and deletion are operator-authorized; provider
observations cannot alter desired resource bytes.

The v2 `execution_spec` schema is a discriminated union, not the JSON of a
repository model with observation and diagnostic fields:

| Kind | Required service-only snapshot (all IDs/ref values validated against the seal-authenticated org) |
|---|---|
| `host` | `installation_id`, `provider`, `execution_location`, `management_endpoint_ref`, `trust_policy_ref`, enabled/lifecycle classes, architecture, capacity/quota and operation limits. Its provider URI, paths, network names and binary remain local installation policy. |
| `image` | `manifest_digest`, format/architecture/OS/firmware, exact component `{kind, storage_ref, digest, size_bytes}` array, release ref, provenance event ID/signer and allowed profiles. Revalidate the signed provenance event, not a cached `verified` boolean. |
| `persistent_vm` | Exact `VMResourceIdentity`, host/image IDs and pinned image manifest digest, desired power, allocation, storage-pool ref, network/passthrough refs, firmware/TPM, config digest, bootstrap `{target_key, secret_id, secret_version}` bindings, maintenance/checkpoint/access policy. Resolve secret versions at effect time or fail; no secret bytes enter the event. |
| `execution_plane` | Host ID, worker pubkey, management endpoint ref/author, package digest/provenance, configuration revision/network/secret-version bindings, pinned image IDs/digests, capacity/concurrency, desired lifecycle classes/capabilities/state and probe policy. |

`delete` retains the signed predecessor identity and digest, but carries no
new provider inputs (`descriptor` and `execution_spec` are empty); the daemon
obtains cleanup inputs from the ACKed
predecessor and refuses if that snapshot cannot be decrypted.

The daemon writes service-signed, addressable kind-`30900` v2 resource state
through the local outbox, using a **new** `CPStateFamily` discriminator and
`t=virtualization-resource-v2`, `d=virtualization-v2:resource:<kind>:<id>`.
Allocate its unused `32xxx` family number at implementation time, register
`cpStateFamilies` and `kinds.gen.js`, and keep its coordinate distinct from
v1 `d` values. Tags carry only family/topic/schema, opaque coordinate and
`deleted`; no `org`, `p`, generation, digest, provider, host, image, approval
or journal tags. OCK-encrypt `{descriptor, org_id, resource_version,
generation, resource_digest, request_rumor_id, request_outer_id, previous_state_event_id,
deleted}` with the **org OCK** and AEAD associated data from the verified
event's family, `d` and `t`. Place the exact `execution_spec` plus the same
identity/version/digest/request binding in `service_inner`, NIP-44-encrypted
to the service key. The OCK is wrapped to current org members and service;
they can read metadata, but only the service can recover execution inputs.
Existing OCK envelopes expose `key_org` and version in ciphertext metadata;
the **accepted relay-visible metadata budget** is therefore: service pubkey,
outer wrap size/time and ephemeral author for requests; for state, family
topic/resource kind, UUIDv7 resource or operation ID (including its minting
time), service author, OCK `key_org` UUID and key epoch, ciphertext length,
and `deleted` boolean. No provider class, host/image refs, action, reason,
resource digest/version/generation, operator identity or approval ID appears
in plaintext tags/content. The OCK scheme does **not** hide org identity from
relays; stronger org/coordinate unlinkability requires a separately versioned
key-routing and coordinate design, not a claim that v2 already provides it.
Refounding and key rotation must re-encrypt **both** layers without changing
resource version or digest; failure to recover the inner layer is not a
reason to execute from SQL or a v1 DTO.

For operations, extend the seal-authenticated, wrapped request to
`schema=bahia.intent.virtualization.v2` with immutable
`org_id`, `operation_id=<uuidv7>`, `resource_state_event_id`,
`resource_version`, `resource_digest`, `expected_generation`, `action`,
`idempotency_key` (`intent_id`), bounded reason, expiry and action-specific
options. Its **request digest**, distinct from the resource-content digest,
is SHA-256 of `bahia:virtualization-operation-request:v2` plus RFC 8785 JSON
of **the full validated rumor envelope** (kind, `created_at`, `d`, `t`,
`domain`, `schema`, `op`, `org`, `intent_id`), every strict content field
including reason/options/expiry, and the verified seal-author pubkey,
omitting only the digest field itself. The service
recomputes it and records the rumor ID and digest together; duplicate
`operation_id`/`intent_id` with differing full digest is a conflict.
It must name an ACKed, non-tombstoned v2 resource state and match its decrypted
identity and authorized org. A separate service-signed, OCK-encrypted
`virtualization-operation-v2` state family at
`d=virtualization-v2:operation:<operation-id>` binds rumor ID and request
digest, resource state event ID/digest/version, action, approval, phase, stable
provider correlation ID, fence epoch and sanitized outcome. No provider
effect occurs merely because a `30315` intent status says `accepted`.
Destructive actions require a **separate** seal-authenticated kind-`30900`
`approve` rumor (`domain=virtualization`,
`schema=bahia.intent.virtualization-approval.v2`, `op=approve`,
`intent_id=<approval-uuidv7>`,
`d=virtualization-approval:<approval-id>`, `t=bahia-intent`,
`t=virtualization`) with `org_id`, `operation_id`, exact operation rumor ID and request
digest, resource state event ID/digest, action, decision, approver pubkey,
approval UUIDv7 and expiry. Its **approval digest** is SHA-256 of
`bahia:virtualization-operation-approval:v2` plus RFC 8785 JSON of its
complete validated rumor envelope, all strict content fields except the
digest itself, and the verified seal-author pubkey. The daemon recomputes
both digests,
requires the independently verified seal author to equal `approver_pubkey`
and **differ** from the request seal author, and checks each signer's current
org permission and the appropriate VM-operator/approval tier at admission
and again before effect. The approval
must be observed from relay ingress and its own immutable ID/digest recorded
in accepted operation state; neither a claimed `approval_id` in the request
nor one principal using two wraps satisfies two-person approval. A rejected,
expired, stale-resource or superseded approval cannot authorize execution.
v1's start/stop/reboot allowlist is not silently widened.

## Acceptance, ordering and execution fence

The service first validates outer and seal NIP-01 IDs/signatures, rumor ID and
seal-author binding,
authorization (`PermWriteDeployments` **and** installation VM-operator policy),
schema, UUIDs, predecessor, digest, OCK and service-inner recoverability.
It signs the new resource state or accepted operation state into the local
outbox. A provider effect needs the **exact** current state event's retained
control-plane outbox row with `Delivered=true`, quorum satisfied, at least one
relay `OK accepted`, matching signed event ID/author/target, and no failed or
undelivered marker. `ErrPublishIncomplete`, a local event, a SQL row, a
`30315`, and one `OK` without quorum are not acceptance. Pin the delivery
receipt until the resource is superseded/tombstoned or the operation reaches
a durable terminal state; a predecessor still referenced by a nonterminal
operation stays pinned even after replacement. Ordinary outbox pruning must
not erase proof.
After a daemon move, reconstruct delivery proof only from per-relay
observations of that exact signed event through long-lived subscriptions
that have reached EOSE on the required configured write-relay quorum; an
attestation from the old daemon alone does not transfer an `OK`. If the
configured relay policy or receipt cannot be proved, fail closed.

Relay latest-winner selection is NIP-01 `(created_at, lowest id on ties)`;
the decrypted `resource_version`, `generation`, predecessor event ID and
digest then enforce semantic monotonicity. Because relays do **not** provide
compare-and-swap, two daemons signing competing successors is forbidden by
the execution topology, not resolved by choosing whichever relay event won.
An out-of-order, equal-version-different-digest, missing predecessor, or
cross-org state is a conflict. A tombstone is the next encrypted version on
the same coordinate, never NIP-09; its receipt and provider cleanup phase
remain independently auditable. No later intent may reuse a deleted resource
ID. An operation ID maps to one request digest and one provider correlation
ID forever; replay attaches/inspects before retry and cannot dispatch a
different action or input snapshot under that ID.

**One writer is an admission and signing precondition, not just an executor
lock.** Before v2 ingress is admitted, the daemon acquires an exclusive
installation-wide virtualization writer fence. It holds that fence across
EOSE-complete latest-state/predecessor check, per-coordinate serialization,
signature creation, durable outbox insertion, exact relay-quorum ACK and any
provider effect through terminal-state ACK. Pending/abandoned delivery blocks
a successor sign/effect on that coordinate; on restart a new holder first
replays outbox and relay state and resolves ambiguity before admission. No
second daemon may use the service signer to create a competing virtualization
successor during this interval. A relay winner or `30315` status cannot repair
two independently signed successors.

The initial enforceable topology is one local installation with sole custody
of the service signing key and an exclusive process-lifetime installation
lock, plus a host-local effect lock for `VMExecutionLocal`. Effect subprocesses
must inherit the host lock or be terminated with their supervisor so an orphan
cannot continue a mutation after lock release. Neither lock is a PostgreSQL
advisory lock. On crash/restart the new holder inspects the provider ownership
marker and operation correlation ID before any retry. Remote hosts, copied
service keys, two installations targeting one provider, or automatic failover
stay unavailable until **one linearizable epoch fence gates both signing and
provider work**: the signer refuses stale epochs and the provider rejects a
stale epoch before each side effect. That epoch is bound in signed operation
state and ownership marker. A relay claim or time-based lease alone is not
this fence. Host admission must prove local installation identity matches
canonical identity and approved policy; otherwise no effect. `flock` alone
cannot fence a second host with independent storage.

Persist phase transitions (`accepted`, `prepared`, `executing`, `observed`,
terminal) as signed operation states through the outbox before advancing a
derived index. At each crash cut point, recover the exact accepted request,
resource state, receipt, fence and provider correlation; inspect existing
effects before replay. Provider errors publish bounded error codes, not raw
diagnostics or guest data. A failed outcome publish leaves the operation
unsettled and blocks a new conflicting effect.

## SQL-only cutover and phased implementation

SQL virtualization documents/journal rows and v1 public events are
**inventory evidence**, not v2 resources or operation approvals. An explicit
quiesced operator command keyset-pages them, records counts, digests and
conflicts, and compares v1 signed/local coordinates and pending outbox entries.
For each selected resource an authorized operator must inspect private
provider/storage/host-image inputs, supply missing values from trusted local
configuration, and sign a **new** v2 registration referencing the legacy row
digest inside the encrypted request. No boot/backfill/reconnect path promotes
SQL, and divergent or incomplete rows remain blocked. Preserve historical
v1 events and SQL evidence; never re-sign either as v2. Cutover is complete
only when every needed v2 coordinate and operation has exact delivery proof,
and SQL can be removed without changing replay or provider behavior.

Implementation slices (no slice enables provider work ahead of the prior
proofs):

1. **Wire and read model:** add v2 schemas/families/topics in
   `internal/kinds/{tags,cp_state_family}.go`,
   `internal/adapters/nostr/{projector,control_state_contract}.go`,
   `web/src/lib/nostr/kinds.gen.js`, `docs/event-spec.md`, and confidential
   client readers. Add strict v2 structs/validators in `internal/domain/` and
   a provenance-preserving v2 route in
   `internal/controlplane/{intent_giftwrap_ingress,intent_processor,virtualization_intent_handler}.go`
   and `encrypted_transport.go`; keep v1 refusal and the signed-inner route
   separate. Enforce both pre-wrap and stored-wrap size bounds in Go and web.
2. **Acceptance and receipts:** use `internal/controlplane/confidential_encryptor.go`
   and `internal/adapters/nostr/publisher.go` for encrypted state creation;
   add exact, retention-pinned delivery verification and EOSE-aware replay
   alongside `internal/controlplane/backup_run_receipts.go`. Wire only after
   OCK rotation/refounding and trust-set recovery are proven DB-less.
3. **Fenced executor:** replace SQL admission in
   `internal/app/virtualization{,_admission,_runtime,_config}.go` and
   `internal/service/persistent_vm_service.go` with a canonical resource and
   operation store; gate predecessor validation and the signer under the same
   installation writer fence, then add host-local lifetime lock/provider
   marker enforcement in `internal/adapters/runtime/vm/`. Re-enable only
   actions whose input, approval and idempotency contracts are complete.
4. **Governed migration:** implement a separate explicit CLI path under
   `cmd/bahia-migrate/`; keep `internal/readmodel/virtualization.go` and
   `internal/repository/pg_virtualization.go` disconnected from production
   publication. The CLI emits an inventory and operator-signing material,
   never service-signed v2 state from a SQL row.

Tests must cover forged/duplicate wraps, signed-rumor rejection, forged
seal-author proof, wrong org/author, stale predecessor,
equal-time NIP-01 ties, OCK rotation/refounding and unavailable service-inner,
plaintext-tag/body leak scans, maximum component counts and actual nested-wrap
size, missing/partial/abandoned/pruned ACKs, multi-relay EOSE handoff,
backdated `1059` (up to NIP-59's six hours) delivered after a newer cursor
across restart within the existing twelve-hour `ProcessSync.ReconcileKinds`
window (NIP-77 and paged fallback), backdated live-only
REQ delivery and outer-ID/rumor-ID dedupe in
`internal/adapters/nostr/process_sync_test.go` and
`internal/controlplane/intent_giftwrap_web_fixture_test.go`; crash at every
predecessor/sign/outbox/provider phase, same-host
contending daemons, stale fence epoch, tombstone/replay, DB absent/unavailable,
and SQL-only/divergent cutover. Keep `TestNoAutomaticSQLToCanonicalPromotion`,
`TestNoNewTestOnlyExports`, kind/drift tests and `make lint-arch` green.

## Product/operator decisions before enabling effects

Choose the exact org-readable descriptor fields versus service-only fields;
whether VM display names or host/provider class are sensitive; receipt pinning
and write-relay quorum retention policy; explicit per-host single-writer
operating model versus a provider-enforced remote fence; and the human
approval/export ceremony for SQL-only private inputs. Until these are resolved
and exercised, the v1 refusal remains the safe behavior.
