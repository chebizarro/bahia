# Operator identity recovery

`bahia-recover` is an offline operator process. It never runs in the Bahia daemon and its bbolt ledger must not be the daemon's event store or outbox. Stop the daemon before the inventory phase so the operator can inspect the local event store. Controlled relays and that durable store are the canonical sources; PostgreSQL is not an inventory source.

Configure `nostr.service_id` as a stable deployment identifier (default `bahia-service-v1`). It must not change when the service pubkey rotates. The `identity_recovery` block requires a separate ledger path, the old pubkey, a machine-readable reason code, controlled relay URLs, a consumer roster, a reviewed list of accepted pre-compromise event IDs, and distinct operator and new-identity signers. The operator signer is NIP-46 or NIP-55L only. Mount NIP-46 client keys as files; do not put key material in YAML or command arguments. The new identity may use any `servicesigner` custody method with its required production transport and fencing. An optional old signer is permitted only through the delete-and-purge phase's quarantined NIP-09/decryption wrapper.

```yaml
nostr:
  service_id: bahia-service-v1
  local_store:
    path: /var/lib/bahia/daemon.bolt
identity_recovery:
  ledger_path: /var/lib/bahia-recovery/incident.bolt
  old_pubkey: <old-pubkey-hex>
  reason_code: compromised
  controlled_relays: [wss://controlled.example]
  consumer_roster: [daemon, relay-sidecar, web, dns, loom, soulfactory, hiveci]
  accepted_event_ids: [<reviewed-event-id-hex>]
  operator:
    public_key: <operator-root-pubkey-hex>
    signer:
      method: nip46
      bunker_uri: <bunker-uri>
      client_secret_key_file: /run/secrets/recovery-operator-client
  new_identity:
    public_key: <new-service-pubkey-hex>
    signer:
      method: nip55l
```

`accepted_event_ids` is an operator-reviewed allowlist from pre-compromise evidence, not an automatic trust decision based on old-key signatures or timestamps. The inventory scans all old-key events from every configured relay to EOSE plus the local store, deduplicates by event ID, and refuses an approved ID absent from those sources. Its digest is SHA-256 of the newline-joined, lexicographically sorted approved IDs. An empty reviewed list is represented by `accepted_event_ids: []`.

```bash
bahia-recover plan --config /etc/bahia/config.yaml
bahia-recover apply --config /etc/bahia/config.yaml --confirm-phase declare
bahia-recover status --config /etc/bahia/config.yaml
bahia-recover resume --config /etc/bahia/config.yaml --confirm-phase declare
bahia-recover abort --config /etc/bahia/config.yaml --confirm-abort
```

`plan` and `status` do not create a ledger or contact signers or relays. `apply` and `resume` require the exact next phase; a pending signed event is republished unchanged after interruption. Every controlled relay must acknowledge publication. Aborting is refused once an activated trust transition has been signed or observed, even if its phase receipt was interrupted. After cutover the workflow is forward-only.

Containment, domain reassertion, secret rotation, purge receipts, and consumer acknowledgments require registered production adapters. Without them the relevant phase fails closed and no completion receipt is emitted. The operator must not interpret a signed `started` or `failed` receipt as completion. A `verify` failure lists consumers whose acknowledgment evidence is missing. Inspect signed receipts and the activated trust-epoch head before retirement.

For the phase-by-phase operator checklist, purge evidence file, staging rehearsal, and live promotion gates, see [identity-compromise recovery](identity-compromise-recovery.md).
