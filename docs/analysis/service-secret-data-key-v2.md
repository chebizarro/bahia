# Service-secret data-key v2: dormant migration slice

**No production rekey command is included or enabled.** This change adds a
package-private offline transformer exercised by disposable PG16 tests and a
raw-key-free package-private v2 resolver, but does not replace every production secret
reader or writer. Current app startup still constructs the legacy raw-key
`Encryptor`; direct control-plane and runtime consumers also decrypt through
it. Running the transformer before those paths are changed would make
migrated secrets unreadable to the deployed daemon. A self-asserted operator
flag cannot prove compatibility.

## Format and invariants

- Migration 000080 adds `service_secret_data_keys`. The package-private
  transformer generates a random 32-byte AES key and NIP-44-wraps it to
  **the existing service pubkey**; only the wrapper is stored in SQL. Tests
  use a disposable synthetic keyer. A future production caller must pass the
  epoch-fenced Signet keyer; no caller is wired in this commit. Signet does
  not derive this data key or perform arbitrary AES operations. The encrypted
  plaintext envelope binds the purpose/domain, key UUID and service pubkey to
  the key bytes; unwrapping rejects SQL row and cross-purpose substitution.
- `aes256gcm-v2` ciphertext contains a version byte, key UUID, random GCM
  nonce and tag. GCM AAD binds secret UUID, version and key UUID. Retained
  `secret_versions` and current `service_secrets` are transformed together.
- The offline operation locks all three tables and uses a serializable
  transaction. It rejects existing key rows, unknown/already-v2 methods,
  mismatched current and retained versions, orphan versions, unreadable rows,
  and a per-table `--max-rows` bound. Any error rolls the transaction back.
  Output is counts only; no plaintext, key or ciphertext is reported.
- The loader refuses zero or multiple wrapped keys. Key rotation will need an
  explicit multi-key lookup by ciphertext key UUID, not a “latest key” guess.

## Preconditions before exposing a future offline runner

1. Wire and test all production SQL and app readers **and** writers for v2,
   including direct control-plane/runtime consumers, and prove remote mode
   cannot fall back to a raw nsec. That work is **not** in this slice.
2. Independently back up and verify the database, inventory all rows, and
   stop the daemon and every SQL writer. The command performs no backup.
3. Have a live, unexpired WriterLease for a dedicated Signet owner and the
   existing service pubkey. Keep the legacy nsec only in an isolated offline
   environment; never put it in arguments, logs or the remote runtime.
4. Implement a reviewed one-shot runner with exact service-key identity
   checks, bounded census reconciliation, strict quiescence, rollback and
   post-migration verification. No such runner exists in this commit. The
   package-private transformer refuses a second key generation.

The command does not sign, publish, change app signer mode, migrate assistant
transcripts/checkpoints, migrate confidential-state HMAC, or erase the legacy
nsec. Those are separate cutover blockers. The DSSE and NIP-44 Signet interop
gate and complete raw-key-crypto census remain independent prerequisites.

Disposable PostgreSQL 16 integration gate:

```sh
BAHIA_REKEY_TEST_DATABASE_URL='postgres://.../disposable_db' \
  go test -tags integration ./internal/adapters/secrets -run '^TestRekeyStoredSecretsPG16$' -count=1
```

The test creates and drops a dedicated schema, executes the real migration
000080 up SQL for the wrapped-key table, and checks row bounds, rollback,
historical/current value continuity, and idempotency refusal.
