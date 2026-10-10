# Raw service-key crypto census

Run `bahia-raw-key-crypto-census` **offline** before considering removal of
`nostr.private_key` from Bahia. Use a PostgreSQL account with `SELECT` only;
the command itself opens a repeatable-read, read-only transaction and never
decrypts, signs, publishes, or updates records. It makes no relay connection.

```sh
export BAHIA_CENSUS_DATABASE_URL='postgres://...read-only-account...'
bahia-raw-key-crypto-census \
  --service-pubkey <existing-64-character-hex-pubkey> \
  --max-rows 10000 > crypto-census.json
unset BAHIA_CENSUS_DATABASE_URL
```

Provide the **existing** service pubkey, not a new one. Supply the DSN only
through the environment; do not put it in command arguments or commit the
report. The command prints no event content, ciphertext, key material, row
identifiers, or SQL error details. A missing table, failed query, expired
deadline, or row count above the configured per-family bound causes a nonzero
exit and **no partial report**. Increase the bound only after reviewing the
database size and execution budget.

The report counts SQL-visible `service_secrets` and `secret_versions` by
encryption method; service-authored confidential cp-state OCK, legacy O1 and
NIP-44-shaped legacy N1 records; assistant transcripts and checkpoints by
recorded key reference/version; confidential `state_hash` tags; and signed
SBOM reference events by embedded key ID. `unknown` means the row cannot be
classified from bounded metadata. A NIP-44 shape or key reference does **not**
prove that the existing service key can decrypt the record. The command does
not inspect external SBOM blobs.

**Every family remains `unproven` even when its SQL count is zero.**
`nostr_events` is a derived index, whereas relays are canonical. The command
does not enumerate relay history, the daemon's local bbolt event store, or its
outbox. Obtain separate complete inventories of those sources and reconcile
them with this report before any key-custody cutover. In particular, a zero
SQL count must never be interpreted as absence of legacy O1/N1 or old
assistant ciphertext on relays. Stop the cutover on any `unknown` count or
unresolved `unproven` blocker; do not bypass it by disabling a feature.

The command is an inventory prerequisite, not a migration or cutover gate.
It never asks for or accepts the service nsec.
