# Identity-compromise recovery: operator procedure

This procedure is for an independent operator root, not the compromised service identity. Treat the old service signer as quarantined from containment onward. PostgreSQL is a derived index, never an inventory or recovery authority. Relay EOSE establishes catch-up on that relay; it never proves global absence.

## Before any declaration

- Obtain an independent operator signer and pin its public key as `nostr.operator_root_pubkey` and `identity_recovery.operator.public_key`. Mount signer client keys as files; never put secret values, private keys, or secret-bearing bunker URLs in arguments, receipts, or logs.
- Fix `nostr.service_id` to the deployment-stable identifier and explicitly pin `nostr.public_key` to the old service identity until the controlled cutover. The new identity and the quarantined old signer must have separate configured custody. The operator root must not equal either service identity.
- Restrict `identity_recovery.controlled_relays` to controlled relays. Record the full consumer roster, including daemon, relay sidecar, web, CLI, DNS, Loom, SoulFactory, HiveCI, and pinned runtimes. A missing enforcement acknowledgment is a blocker, not consent.
- Stop the old-key daemon before inventory and preserve its local event store, outbox, verified pre-compromise snapshots, relay audit evidence, and a separate writable bbolt recovery ledger. Review `accepted_event_ids` independently; old-key signatures and EOSE alone are insufficient.
- Obtain independent security review of the source, exact deployment configuration, signer custody, relay policy, recovery artifact, and rehearsal evidence. A test using substitute keys, relays, config, or build artifact does not satisfy the exact-artifact gate for live cutover.

The recovery config needs `nostr.service_id`, `nostr.operator_root_pubkey`, `nostr.public_key`, `nostr.local_store.path`, and the `identity_recovery` settings in [the base runbook](identity-recovery.md). Set `identity_recovery.purge_evidence_path` to a protected JSON file path before delete-and-purge. The file holds **identifiers only**, not backup contents or keys:

```json
{
  "archive-artifacts": "archive-artifact-digest",
  "pg-backups": "backup-artifact-digest",
  "wal": "wal-artifact-digest",
  "controlled-re-encryption": "reencryption-artifact-digest"
}
```

Each identifier must be a short safe token. The archive, PostgreSQL backup, and WAL purges are operator-executed outside Bahia; the recovery tool records their supplied evidence but does not delete backup media. Controlled re-encryption also requires its own evidence. A missing item blocks phase completion.

## Phase confirmations

Use one configuration file throughout. Review `plan` and `status` before **each** `apply`; after an interrupted publication, use `resume` with the same exact phase. Do not skip or reorder phases.

```bash
bahia-recover plan --config /etc/bahia/config.yaml
bahia-recover status --config /etc/bahia/config.yaml
bahia-recover apply --config /etc/bahia/config.yaml --confirm-phase declare
bahia-recover apply --config /etc/bahia/config.yaml --confirm-phase contain
bahia-recover apply --config /etc/bahia/config.yaml --confirm-phase inventory
bahia-recover apply --config /etc/bahia/config.yaml --confirm-phase provision-new-identity
bahia-recover apply --config /etc/bahia/config.yaml --confirm-phase cut-activate-trust
bahia-recover apply --config /etc/bahia/config.yaml --confirm-phase reassert-canonical-state
bahia-recover apply --config /etc/bahia/config.yaml --confirm-phase rotate-external-secrets --rotation-updates /run/bahia-recovery/rotation-evidence.json
bahia-recover apply --config /etc/bahia/config.yaml --confirm-phase delete-and-purge
bahia-recover apply --config /etc/bahia/config.yaml --confirm-phase verify
bahia-recover apply --config /etc/bahia/config.yaml --confirm-phase retire-old-identity
```

Example interruption handling: `bahia-recover resume --config /etc/bahia/config.yaml --confirm-phase delete-and-purge`. The engine republishes the same signed pending event and requires an OK from every cleanup relay for deletion (every controlled relay for other records). `status` must show a signed `completed` phase receipt, not merely `started` or `failed`.

The `contain` phase reads the daemon's durable `nostrout` containment marker next to the resolved outbox path and requires its incident ID to match the declared incident. The declaration itself is the trigger; the CLI does not publish a second containment command. If the old daemon is already unreachable and the marker is absent, the operator may use `--confirm-daemon-unreachable` on that phase; the receipt states that assertion instead of claiming observed kill-switch evidence. A malformed or wrong-incident marker is never waived by the flag. The rotation evidence file contains `RotationUpdate` entries with `ref_id`, either `new_ref` plus `source_rotation_evidence_id` or `waiver_evidence_id`; no credential values belong in it.

Approval points: independently approve the declaration and accepted inventory; approve the new signer and consumer deployment **before** `cut-activate-trust`; verify the activated operator-signed epoch, barrier, full inventory artifact, and consumer enforcement before reassertion; review every source-rotation or signed waiver and each purge/re-encryption artifact before delete-and-purge; independently approve old-signer retirement only after verification. Before cutover, `bahia-recover abort --config /etc/bahia/config.yaml --confirm-abort` may publish an operator-signed abort. At or after cutover, abort is refused and recovery is forward-only.

A phase receipt proves only what its signed evidence identifiers and audited relay OKs support. `inventory` records the reviewed inventory digest and count plus per-relay pagination evidence (URL digest, pages, unique IDs, timestamp range, NIP-11 and used limits, final empty verification page); EOSE is only an end-of-page signal. Missing reviewed IDs receive a direct by-ID relay fetch before the phase fails closed with their count and a sample. `cut-activate-trust` records the epoch; `reassert-canonical-state` records reasserted event IDs; `rotate-external-secrets` requires a source-service rotation evidence ID for each inventoried `{store, credential_id, version}` or an explicit operator-signed waiver; `delete-and-purge` records kind-5 deletion IDs, per-cleanup-relay OK identifiers, and the four separate purge checklist identifiers. Source rotation occurs at the source service; Bahia never fetches a credential value for rotation. Retirement refuses missing deletion, purge, or controlled re-encryption evidence.

## Staging rehearsal and live gate

Run the throwaway in-process relay and fresh-bbolt rehearsal without Docker or sleeps:

```bash
go test -tags=integration ./internal/identityrecovery -run '^TestStagingIdentityRecoveryRehearsal$' -count=1 -v
```

The in-process harness calls the same `NewProductionRuntime` constructor as `bahia-recover`, with test-held signers and two ephemeral relays: one controlled and one additional cleanup relay. It runs the real registry, backup, security, notification re-encryption, relay settings, hostname, SBOM, docs, discovery, SQL-draft (absent optional PostgreSQL, which yields zero reviewed projection rows but never proves SQL-only rows absent elsewhere), credential-rotation, containment-marker, cleanup-deletion, and acknowledgment adapters. Its relays advertise a seven-event per-REQ limit and hold more reviewed old-key events than that limit, including same-second boundaries and events absent from the local store, plus an unaccepted old-key event held only on the cleanup relay. It checks the full inventory and signed per-relay pagination evidence, deletion OK, decrypts the old notification inner payload and the re-encrypted new payload, restarts the complete runtime at each completed phase boundary, checks signed receipts and inventory digest equivalence, rejects a post-barrier old-key event, and refuses post-cutover abort. The real readiness reader blocks cutover until every consumer has signed readiness or an evidence-bearing isolation waiver; the harness proves a missing DNS readiness record blocks activation. It exercises the production acknowledgment construction and authority checks with generated daemon, sidecar, and DNS identities and controlled-relay publication, proves a missing DNS acknowledgment remains missing, and uses operator attestations for daemon-hosted Loom/HiveCI and other operator-authority consumers. The real acknowledgment reader then blocks `verify` at SoulFactory and pinned runtimes because those consumers remain `pending` in the production enforcement roster; signed acknowledgments do not override that policy. The rehearsal must leave `verify` incomplete and must not claim retirement.

The **operator-run exact-artifact staging rehearsal** is a separate promotion gate. Pin the built release image as `repository@sha256:<64 lowercase hex>` (the independent review's built-image digest convention), record that immutable digest alongside the reviewed config and signer/relay custody identifiers, and run the *same image* and `bahia-recover` command path against staging copies of the actual local store, recovery ledger, cleanup-relay topology, and optional PostgreSQL projection. Preserve its signed per-phase receipts, per-relay EOSE/OK results, deletion batches, and output artifact hashes under the image digest. Do not substitute a source-built test binary or throwaway signers/relays for this step. Do not promote while `verify` or any consumer enforcement remains pending; complete SoulFactory and pinned-runtime enforcement and obtain independent security sign-off first.

**Do not execute a live cutover from the current CLI yet.** Production adapters are registered for containment inspection, the required canonical domains, canonical credential rotation, and purge receipts, and the production consumer-acknowledgment reader is wired into verify and activation gating. What remains before a live cutover: SoulFactory and pinned-runtime enforcement are registered pending (activation requires an explicit operator waiver or signed readiness for each, while verification still refuses pending enforcers), the exact-artifact staging rehearsal, and the independent security re-review. Use `--confirm-daemon-unreachable` only when the old daemon has already died and its containment marker is absent; the resulting receipt records an operator assertion, not observed containment. Supply `--rotation-updates FILE` with identifier-only source evidence or waivers. No manual receipt or PostgreSQL query substitutes for missing signed evidence.

Live promotion requires all production adapters registered, the full consumer roster enforcing the activated epoch, independently reviewed exact build/config/key/relay artifacts, an exact-artifact staging rehearsal with durable receipts, and independent security sign-off. Preserve the signed ledger and relay receipts as handoff evidence; never claim production acceptance from this throwaway harness alone.
