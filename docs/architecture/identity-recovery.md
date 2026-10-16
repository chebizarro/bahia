# Identity-Compromise Recovery — Event Contract and Phase Model

Fleet task: `bahia-identity-compromise-recovery-workflow-20261015`. This document is
the shared contract for the recovery engine (`internal/identityrecovery`,
`cmd/bahia-recover`), daemon containment, and consumer trust-epoch enforcement.
The three recovery records below are authored by the **independent operator
trust root** (the FleetOps root the daemon and sidecar configure). Consumer
acknowledgments are the sole service-signed exception and never authorize a
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
  `completed_at`. Latest-wins; a resumed phase reuses the same `d`.

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
  runtimes. The last five are registered as `enforcement=pending` and verify
  enumerates each as missing; no acknowledgment is fabricated for them.

## Acknowledgments
- Kind `30900`, receipt family `32053`, `t=identity-recovery-receipt`,
  `domain=identity`, `schema=bahia.trust-epoch-ack.v1`, `legacy_kind=32053`,
  `epoch=<int>`, `deleted=false`.
- `d=trust-epoch-ack:<service-id>:<consumer-id>` is stable per consumer.
  Content is `service_id`, `consumer_id`, `epoch`, `epoch_event_id`. The daemon
  and sidecar sign their acknowledgment with the **new** service identity only
  after they start under the activated epoch. The verifier checks the signed
  new-key envelope and exact epoch event ID; enqueue or an unsigned receipt is
  not evidence of relay acceptance. Web and CLI have no service signer and
  therefore do not fake one: verify must prove their enforcement separately or
  report them missing.

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
operator-signed intent. Until a SQL candidate reporter exists, its absence is
*not* evidence that there are no SQL-only candidates.

Security state uses the fleet OCK (`SecurityCanonicalPublisher.publishConfidential`)
and attribution edges use the subject organization's OCK
(`SecurityCanonicalPublisher.PublishAttribution`). Both call
`EncryptConfidential` with no service-only plaintext, so those records have
no service-key-derived `service_inner`. Their existing ciphertext can be
preserved byte-for-byte when their recovery adapter is implemented; this does
not apply to other confidential families that may carry `service_inner`.
SBOM attestations are excluded from canonical reassertion and are regenerated
under `bahia-4f10q.3.3`.
