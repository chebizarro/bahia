# Identity-Compromise Recovery — Event Contract and Phase Model

Fleet task: `bahia-identity-compromise-recovery-workflow-20261015`. This document is
the shared contract for the recovery engine (`internal/identityrecovery`,
`cmd/bahia-recover`), daemon containment, and consumer trust-epoch enforcement.
Nothing here is signed by the service key: **every record below is authored by the
independent operator trust root** (the FleetOps root the daemon and sidecar already
configure). An event signed only by a compromised service key never authorizes any
part of recovery.

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
  with a lower epoch. Epoch verification = operator-root signature + monotonicity.

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
  runtimes. Each either publishes an acknowledgment or is verified fail-closed
  by the `verify` phase.

## Sources of truth during recovery
Audited relay events, the durable local Nostr store, and verified pre-compromise
snapshots only. PostgreSQL and its outbox are never canonical; SQL-only
candidates surface as unsigned review drafts that require operator-signed
intents to re-enter canonical state.
