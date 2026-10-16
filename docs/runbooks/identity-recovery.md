# Operator identity recovery

`bahia-recover` is an offline operator process. It never runs in the Bahia daemon and its bbolt ledger must not be the daemon's event store or outbox. Stop the daemon before the inventory phase so the operator can inspect the local event store. Controlled relays and that durable store are the canonical sources; PostgreSQL is not an inventory source. Cleanup relays are historical audit/deletion targets, not canonical publish or quorum targets.

Configure `nostr.service_id` as a stable deployment identifier (default `bahia-service-v1`). It must not change when the service pubkey rotates. The `identity_recovery` block requires a separate ledger path, the old pubkey, a machine-readable reason code, controlled relay URLs, cleanup relay URLs (including every controlled relay and any historical public relay), the complete consumer roster and per-consumer acknowledgment authorities, a reviewed list of accepted pre-compromise event IDs, and distinct operator and new-identity signers. The operator signer is NIP-46 or NIP-55L only. Mount NIP-46 client keys as files; do not put key material in YAML or command arguments. The new identity may use any `servicesigner` custody method with its required production transport and fencing. An optional old signer is permitted only through the delete-and-purge phase's quarantined NIP-09/decryption wrapper.

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
  cleanup_relays: [wss://controlled.example, wss://relay.damus.io]
  consumer_roster: [daemon, sidecar, web, cli, dns, loom, soulfactory, hiveci, pinned-runtimes]
  acknowledgment_authorities:
    daemon: <new-service-pubkey-hex>
    sidecar: <dedicated-relay-ack-pubkey-hex>
    web: <operator-root-pubkey-hex>
    cli: <operator-root-pubkey-hex>
    dns: <dns-agent-pubkey-hex>
    loom: <loom-component-pubkey-hex>
    soulfactory: <soulfactory-component-pubkey-hex>
    hiveci: <hiveci-component-pubkey-hex>
    pinned-runtimes: <operator-root-pubkey-hex>
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

`accepted_event_ids` is an operator-reviewed allowlist from pre-compromise evidence, not an automatic trust decision based on old-key signatures or timestamps. The inventory scans old-key events on every cleanup relay to per-relay EOSE plus the local store, records each relay as EOSE-complete or incomplete with a reason, deduplicates by event ID, and refuses an approved ID absent from those sources. Its digest is SHA-256 of the newline-joined, lexicographically sorted approved IDs. An empty reviewed list is represented by `accepted_event_ids: []`.

```bash
bahia-recover plan --config /etc/bahia/config.yaml
bahia-recover apply --config /etc/bahia/config.yaml --confirm-phase declare
bahia-recover status --config /etc/bahia/config.yaml
bahia-recover resume --config /etc/bahia/config.yaml --confirm-phase declare
bahia-recover abort --config /etc/bahia/config.yaml --confirm-abort
bahia-recover attest-readiness --config /etc/bahia/config.yaml --consumer dns --evidence-ids <verified-event-id>
bahia-recover attest-readiness --config /etc/bahia/config.yaml --consumer pinned-runtimes --waiver-reason no_epoch_aware_image_floor
bahia-recover attest-ack --config /etc/bahia/config.yaml --consumer web
```

`plan` and `status` do not create a ledger or contact signers or relays. `apply` and `resume` require the exact next phase; a pending signed event is republished unchanged after interruption. Every controlled relay must acknowledge canonical publication. The quarantined old-key kind-5 e-tag deletion alone is sent to cleanup relays; every cleanup relay must return an OK (or protocol duplicate) for each deletion batch. OK-false, unresolved AUTH/CLOSED, and unreachable relays remain incomplete in the ledger and block phase completion. EOSE proves only that a relay finished its stored-event answer, never that old-key events were absent. Aborting is refused once an activated trust transition has been signed or observed, even if its phase receipt was interrupted. After cutover the workflow is forward-only.

Containment, domain reassertion, canonical credential rotation, and purge receipts have registered production adapters, and the production acknowledgment reader feeds verify and the activation gate. Each consumer still needs an independently verified signed readiness/acknowledgment record (or an explicit operator waiver recorded in the cut receipt); without them the relevant phase fails closed and no completion receipt is emitted. Use `--confirm-daemon-unreachable` only when the old daemon has already died and its containment marker is absent; the receipt records an operator assertion, not observed containment. Supply `--rotation-updates FILE` with identifier-only source evidence or waivers. The operator must not interpret a signed `started` or `failed` receipt as completion. A `verify` failure lists consumers whose acknowledgment evidence is missing. Inspect signed receipts and the activated trust-epoch head before retirement.

For the phase-by-phase operator checklist, purge evidence file, staging rehearsal, and live promotion gates, see [identity-compromise recovery](identity-compromise-recovery.md).
