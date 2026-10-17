# Identity-Compromise Recovery — Event Contract and Phase Model

Fleet task: `bahia-identity-compromise-recovery-workflow-20261015`. This document is
the shared contract for the recovery engine (`internal/identityrecovery`,
`cmd/bahia-recover`), daemon containment, and consumer trust-epoch enforcement.
The three recovery records below are authored by the **independent operator
trust root** (the FleetOps root the daemon and sidecar configure). Consumer
acknowledgments use their consumer-specific authorities and never authorize a
trust transition. An event signed only by a compromised service key never
authorizes any part of recovery.

## Records (kind 30900, CASControlState envelope, operator-root-signed)

### 1. Incident declaration — family 32051, topic `identity-incident`
- `d` = `identity-incident:<incident-id>` — incident-id is an author-minted UUIDv7.
- tags: `t=identity-incident`, `domain=identity`,
  `schema=bahia.identity-incident.v1`, `legacy_kind=32051`,
  `old=<old-service-pubkey-hex>`, `deleted=false`.
- content (JSON): `incident_id`, `old_pubkey`, `declared_at` (RFC3339),
  `reason_code` (stable machine code, no free-text secrets),
  `controlled_relays[]`, `phase` (current phase name), `aborted` (bool).
- Latest-wins per `d`; phase progression publishes a replacement with the new
  `phase`. `aborted=true` is terminal and only valid before trust cutover.

### 2. Trust transition — family 32052, topic `trust-epoch`
- `d` = `trust-epoch:<service-id>` where service-id is the stable deployment
  identifier (config `nostr.service_id`; NOT a pubkey — it must survive rotation).
- tags: `t=trust-epoch`, `domain=identity`, `schema=bahia.trust-epoch.v1`,
  `legacy_kind=32052`, `epoch=<int>`, `deleted=false`.
- content: `incident_id`, `epoch` (strictly monotonic integer),
  `old_pubkey`, `new_pubkey`, `controlled_relays[]`,
  `activation_barrier` (unix seconds: old-key events with created_at beyond this
  are hostile even if inventoried), `inventory_digest` (lowercase hex sha256 over
  the newline-joined, lexicographically sorted accepted pre-compromise event-id
  list), `inventory_count`, `consumer_roster[]` (stable consumer ids),
  `activated` (bool).
- Consumers persist the last accepted epoch and REFUSE regressions or replays
  with a lower epoch, or a different event at the same epoch. Verification
  includes the independent operator-root signature, exact tags and schema,
  service-id coordinate, key chain, and monotonicity.

### 3. Phase receipt — family 32053, topic `identity-recovery-receipt`
- `d` = `identity-incident:<incident-id>:phase:<phase>`.
- tags: `t=identity-recovery-receipt`, `domain=identity`,
  `schema=bahia.identity-recovery-receipt.v1`, `legacy_kind=32053`,
  `incident=<incident-id>`, `deleted=false`.
- content: `incident_id`, `phase`, `status` (`started|completed|failed`),
  `input_digest`, `evidence_ids[]` (event ids / commit SHAs / artifact digests),
  `completed_at`. A completed cut receipt also carries `waiver_event_ids`, a
  consumer-id to operator-signed waiver-event-id map, and `waiver_details`,
  binding each waived consumer to its reason code and evidence event IDs.
  Latest-wins; a resumed
  phase reuses the same `d`.

## Phases (ordered; each restart-safe, idempotent, receipt-bearing)
`declare` → `contain` → `inventory` → `provision-new-identity` →
`cut-activate-trust` → `reassert-canonical-state` → `rotate-external-secrets` →
`delete-and-purge` → `verify` → `retire-old-identity`.
- `abort` is permitted strictly before `cut-activate-trust` completes; afterwards
  recovery is forward-only and trust in the old identity can never be restored.
- Old-signer quarantine from `contain` onward: the compromised key may sign ONLY
  e-tag NIP-09 deletions of its own events and perform decryption needed for
  controlled re-encryption; never 30900/30315/intent/status publications.

## Enforcement rules (consumers)
- With an activated trust-epoch for their service-id, consumers accept the new
  pubkey as the service identity and treat old-key events as hostile unless the
  event id is in the accepted inventory AND created_at ≤ activation_barrier.
- When no epoch has ever been accepted and the configured source has no epoch
  record, the configured service pubkey remains in legacy mode. A relay EOSE
  alone is not proof that no epoch exists. Once an epoch has been accepted, a
  missing head is a regression and fails closed. A PRESENT but unverifiable
  record fails closed, including malformed tags, content, author, signature,
  or a broken key chain. Absent or invalid inventory fails closed for old-key
  replay admission.
- The digest and count do not authorize old-key replay. A consumer needs the
  full engine-exported inventory artifact, checks the exact digest and count,
  then accepts only listed event IDs at or before the activation barrier.
- `nostr.service_id` and `nostr.operator_root_pubkey` are explicit deployment
  settings; `nostr.public_key` explicitly pins the legacy identity even when
  the signer has already rotated. The root never defaults to the service key. The web deployment
  bootstrap carries the same pair; CLI reads use `--service-id` and
  `--operator-root-pubkey` with explicit relay URLs when epoch mode is enabled.
- Missing/garbled epoch state fails CLOSED (no fallback to the old key).
- The old-key daemon contains itself as soon as it verifies an operator-root
  incident declaration naming its own service pubkey. It persists the incident
  head in the durable local outbox store and keeps a separate containment
  marker beside the outbox. Outbound EVENT/AUTH admission, old-key signing,
  mutating intents, assistant async/mutation tools, and evidence-retention
  passes remain fenced across process restarts. Reads remain available.
- A verified `aborted=true` replacement lifts daemon containment only before
  `cut-activate-trust` and only after the incident subscription catches up on
  every configured relay. EOSE is not absence proof: a missing declaration
  never clears a durable marker. An activated trust-epoch does not lift the
  old-key daemon's containment; recovery deploys a daemon under the new
  identity instead.
- Registered consumers: daemon bootstrap trust, relay sidecar read-auth/NIP-42,
  web bootstrap seed, CLI fleet pools, DNS, Loom, SoulFactory, HiveCI, pinned
  runtimes. DNS, Loom and HiveCI now have production epoch guards. SoulFactory
  remains `enforcement=pending`: its CLI Nostr client still uses an optional
  static factory-author pin and does not maintain an operator-root epoch
  subscription across its result waits. Pinned runtimes remain pending: the
  edge image-admission script pins Git/image provenance but has no Nostr epoch
  source, durable high-water mark, or service-key rejection at the image floor.
  Neither pending consumer is counted as acknowledged by verify.

Before `cut-activate-trust`, `bahia-recover attest-readiness --consumer ID
--evidence-ids ID[,ID]` publishes a per-consumer operator-root-signed
`bahia.trust-epoch-readiness.v1` verification record binding service-id,
consumer-id, incident-id, epoch and enforcing pubkey. If capability cannot be
proved, `--waiver-reason consumer_disabled|consumer_isolated|consumer_decommissioned`
and required `--evidence-ids ID[,ID]` publish an explicit per-consumer waiver
in the same signed envelope. A reason alone is invalid; evidence must identify
a shutdown, isolation, or decommissioning record showing the consumer cannot
ingest old-key events. Cutover re-reads all records through controlled
relays and the local store; every consumer must have a valid verification
attestation or waiver, and each waiver's event ID, reason, and evidence IDs
enter the signed completed phase receipt. A pending roster entry still blocks the post-cut verify phase even
when a waiver authorized activation.

## Acknowledgments
- Kind `30900`, receipt family `32053`, `t=identity-recovery-receipt`,
  `domain=identity`, `schema=bahia.trust-epoch-ack.v1`, `legacy_kind=32053`,
  `epoch=<int>`, `deleted=false`.
- `d=trust-epoch-ack:<service-id>:<consumer-id>` is stable per consumer.
  Content binds `service_id`, `consumer_id`, `epoch`, `incident_id`,
  `enforcing_pubkey`, and the exact `epoch_event_id`. Operator verification
  attestations also bind `attested_by` to the operator pubkey. The authority
  for every consumer is pinned by `identity_recovery.acknowledgment_authorities`;
  an unknown, absent, shared component, or mismatched authority fails closed.

| Consumer | Acknowledgment authority |
|---|---|
| daemon | Activated **new** service key, after its trust guard starts |
| sidecar | Dedicated relay acknowledgment key from `nostr.sidecar.ack_private_key_file`, distinct from the service and operator keys |
| CLI | Operator signer, after verifying CLI enforcement |
| DNS | DNS agent's own configured signing identity |
| Loom | Operator-signed verification attestation (`attested_by`); its guard is daemon-hosted and the optional projection signer is not its authority |
| SoulFactory | Operator-signed verification attestation (`attested_by`); enforcement remains pending |
| HiveCI | Operator-signed verification attestation (`attested_by`); its guard is daemon-hosted and has no independent signer |
| web | Operator-signed verification attestation (`attested_by`) |
| pinned-runtimes | Operator-signed verification attestation (`attested_by`) |

The operator publishes CLI, web, Loom, HiveCI, SoulFactory and pinned-runtime post-cut receipts with
`bahia-recover attest-ack --config FILE --consumer ID` only after verifying
their enforcement. The command refuses a non-operator authority or a missing
activated epoch. Sidecar and DNS publish their own receipts under their
configured component signing keys; the recovery CLI cannot impersonate those
identities. An operator receipt does not override a pending roster entry.

The recovery acknowledgment reader queries scoped subscriptions on **every**
controlled relay and the durable local store, verifies the exact signed
envelope against the authority table, and reports missing consumers through
`trustepoch.MissingAcknowledgments`. EOSE completes a backfill; it is not
evidence that a missing event never existed. The Go and web fixture asserts
the daemon acknowledgment byte-for-byte; web verifies this envelope, while
web, Loom, HiveCI, SoulFactory and pinned-runtime readiness is operator-attested rather than self-signed.

## Sources of truth during recovery
Audited relay events, the durable local Nostr store, and verified pre-compromise
snapshots only. PostgreSQL and its outbox are never canonical; SQL-only
candidates surface as unsigned review drafts that require operator-signed
intents to re-enter canonical state.

## Domain reassertion safety

The engine passes only the new-identity `servicesigner` keyer to registered
`DomainRecoveryAdapter` implementations. A domain inventories accepted old-key
heads from EOSE-complete controlled-relay reads and the durable local store,
rebuilds each head with its production typed publisher, and checks exact tags
and content before publishing. An accepted old-key event beyond the trust
activation barrier is rejected even if its ID appears in the inventory.
New-key heads with the same coordinate and
equivalent tags/content are not re-signed. Verification reads the new-key head
back from both surfaces and emits a sorted-coordinate SHA-256 equivalence
digest. Service and environment registry recovery includes service tombstones,
environment tombstones and the environment's explicit deployment-unit set;
adoption bindings use the existing live binding publisher. An accepted binding
tombstone has no production publisher and therefore fails closed.

The CLI declares all domain groups required before `reassert-canonical-state`
or `verify` can complete. A partially registered build cannot issue a
completed phase receipt. PostgreSQL-only rows are never passed to a recovery
publisher: they require an unsigned review draft followed by a separate
operator-signed intent.

Security state uses the fleet OCK (`SecurityCanonicalPublisher.publishConfidential`)
and attribution edges use the subject organization's OCK
(`SecurityCanonicalPublisher.PublishAttribution`). Both call
`EncryptConfidential` with no service-only plaintext, so those records have
no service-key-derived `service_inner`. The registered security adapter
validates the public typed envelope, OCK scope and associated-data coordinate,
requires no `service_inner`, and preserves the accepted ciphertext and keyed
`state_hash` byte-for-byte through the production projector signing path. It
does not possess OCK keys or decrypt the payload; provenance is the accepted
old-key inventory and exact new-key read-back. This does not apply to other
confidential families that may carry `service_inner`.
SBOM attestations are excluded from canonical reassertion and are regenerated
under `bahia-4f10q.3.3`.

Relay settings and the four advertised relay sets are recovered from accepted
old-key heads, not from SQL or regenerated effective policy. Recovery validates
the typed policy against the operator-controlled relay guard and requires the
production serializers to reproduce the old tags and content exactly. The
recovery relay pool is bootstrapped from `controlled_relays` in the recovery
configuration; relay URLs carried by the signed policy or relay sets never
select a destination for the reassertion itself. A historical settings head
whose envelope or normalized content cannot be reproduced fails closed rather
than silently changing its authority or topology.

Hostname reservation and withdrawal-receipt recovery uses only the relay-first
canonical record publisher. It decodes and validates the original typed plan,
revision, deletion flag and withdrawal phase, then requires exact wire
equivalence before signing. It never enters the hostname intent handler or
route manager, so reassertion cannot re-run DNS, tunnel, Caddy or TLS effects.

Backup recovery covers the recipe, policy, repository and definition registry
records plus backup run-state heads. The signed `org` tag must equal content
`org_id` for every head, including tombstones; tombstone-only tag fields are
reconstructed before exact comparison. An embedded run verification is taken
from the accepted run head, not looked up in PostgreSQL. The normal canonical
publishers sign these records with no backup execution or scheduling source
attached. Restore, standalone verification and retention-run families are not
part of this registered group and require a separate adapter before they can
be considered reasserted.

Bahia-docs long-form topics (30023 with `t=bahia-docs`) are separately
registered. Recovery uses the production topic builder with the accepted
`published_at` tag and markdown, and fails if title, category, summary or
content hash would drift. Notification channels and system discovery remain
separate required domains. Notification recovery keeps the OCK ciphertext,
nonce, key metadata and associated data unchanged, decrypts `service_inner`
only with the quarantined old signer, and encrypts that same plaintext to the
new identity before the normal notification cp-state publisher signs it.
Verification decrypts both inner layers and checks the unchanged OCK-visible
envelope on relay and local heads; each coordinate and new event ID enters the
phase receipt. A missing quarantined decryptor fails closed.
Delete-and-purge requires those exact coordinate receipts in the signed
reassertion receipt and carries them into its own receipt; retirement checks
them again against the current relay/local new-key heads in addition to the
operator's external controlled-re-encryption checklist evidence.

SBOM reference heads (`30078`, `t=sbom-reference`) are re-signed with the
production typed builder and identical content/tags. Availability heads
(`30004`, `t=sbom-availability`) use the production builder too. Their
author-dependent `a` tags and `referencePubkey`/`attestationId` content fields
must point to accepted, newly signed reference coordinates; those fields are
the sole allowed difference from the old availability head. The adapter
checks every old and new reference linkage on both audited surfaces before
completing. It does not regenerate attestation statements; that work belongs
to `bahia-4f10q.3.3`.

System discovery is a **regeneration** domain, not an equivalence domain.
The adapter inventories accepted old `31974` or current `11316` announcement
heads, then invokes the production discovery announcement publisher from
current deployment configuration under the new identity. Verification
requires a well-formed new-key `11316` head in both relay and local state.
Content equivalence to the old head is intentionally not required because
capabilities, advertised relays and observations are live-generated.

The rotation source scans accepted canonical heads from the EOSE-complete
controlled relays and durable store for nested `credential_ref`
`{store, credential_id, version}` triples. It decrypts OCK-visible content
using accepted old-key key-envelope heads and uses the quarantined signer to
decrypt service-only inner content. Backup repositories/definitions/run
snapshots, runtime desired-state secrets, and the secret registry are covered
without consulting PostgreSQL; HiveCI mirror, LLM/ML provider, notification
and deployment opaque secret IDs resolve through those canonical secret
records. The tracker also includes configured system keys, hashes each
stable label and credential version into a rotation ID, and deduplicates.

`bahia-recover report-drafts --config FILE [--draft-report FILE]` writes an
unsigned, mode-0600 JSON artifact without using a service signer or publishing
an event. It enumerates projection identifiers in a read-only repeatable-read
PostgreSQL snapshot, compares them with EOSE-complete accepted old-key and
validated new-key canonical heads in the relay and durable store, and reports
missing heads (including embedded deployment units and rows lacking an SBOM
coordinate) as manual-review candidates. The artifact contains identifiers,
not projection payloads or secrets. A changed SQL snapshot or incomplete relay
read fails verification; absence is never inferred from a failed source.

## Historical cleanup relays

`identity_recovery.cleanup_relays` includes every controlled relay and each
historical relay that may retain old-key events; it may include public relays
such as `wss://relay.damus.io`. The cleanup set is not a publish/quorum set.
Only old-key inventory REQs and quarantined, inventory-scoped kind-5 e-tag
EVENTs use it. Operator records, trust epochs, reasserted state, readiness and
acknowledgment publications use controlled relays only. The dedicated cleanup
pool rejects every other kind and signer, even on a cleanup relay that is also
controlled. The normal public-relay control-plane denylist is unchanged.

The ledger records EOSE or an incomplete outcome for every cleanup relay's
inventory scan, and OK, OK-false, or incomplete outcome for every cleanup
relay and signed deletion batch. Signed phase receipts carry relay-URL digests
and outcome classes, not relay-provided text or secrets. An incomplete relay
blocks inventory/deletion phase completion; EOSE is a catch-up boundary, not
proof of absence.
