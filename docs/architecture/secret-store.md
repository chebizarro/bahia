# Secret store

Secret values live in an external secret store: Signet's generic secret
store. Bahia's canonical records carry only a reference to the credential;
the daemon fetches a value at the moment an operation needs it, under a
short store-issued lease, holds it in memory and zeroizes it. A leaked
service key therefore exposes no secret value, and losing PostgreSQL loses
none either.

Code: `internal/secretstore` (the seam), `internal/adapters/signet/credential_store.go`
(the Signet implementation), `internal/app/secret_store.go` (wiring),
`internal/adapters/nostr/secret_canonical_view.go` (canonical reads),
`internal/controlplane/secret_intent_handler.go` (intents). Operator
procedures: [`docs/runbooks/signet-secret-store.md`](../runbooks/signet-secret-store.md).

## Model

The canonical secret record is the `30900` cp-state record with
`t=secret-registry` and `d=<secret-uuid>`, OCK-encrypted for the service's
org ([confidential state](confidential-state.md)). Its plaintext is metadata
plus the credential reference:

```json
{
  "id": "0192f1c4-…",
  "service_id": "0192f0aa-…",
  "environment_id": "0192f0ab-…",
  "name": "DATABASE_URL",
  "version": 3,
  "created_by": "<operator pubkey>",
  "created_at": "2026-10-11T12:00:00Z",
  "updated_at": "2026-10-11T12:30:00Z",
  "credential_ref": {"store": "signet", "credential_id": "cred_<64 hex>", "version": 2},
  "deleted": false
}
```

- `version` is the record's revision; `credential_ref.version` is the
  credential version in Signet the secret is pinned to.
- Values are never in PostgreSQL, on relays, in intents, in status events or
  in logs. Store-backed records have no `encryption_method`.
- The handler reads the current record from the daemon's local event store
  (`CanonicalSecretView`); PostgreSQL is not involved.

### The seam

```go
type SecretStore interface {
    // Resolve fetches exactly ref.Version under a short lease; the caller
    // owns the Value and must Zeroize it.
    Resolve(ctx context.Context, ref domain.SecretCredentialRef) (*secretstore.Value, error)
    // Inspect returns payload-free metadata (active version, status, owner).
    Inspect(ctx context.Context, credentialID string) (secretstore.Metadata, error)
    // Health performs an authenticated, payload-free round trip.
    Health(ctx context.Context) error
}
```

`Value.Bytes()` returns the value until `Zeroize()` overwrites it;
`Value.LeaseExpiresAt()` bounds how long the caller may use it. Formatting a
`Value` prints nothing secret. Every failure is one of these sentinels
(`errors.Is`), never a fallback value:

| Sentinel | Meaning |
|---|---|
| `ErrNotConfigured` | no `secret_store.backend` |
| `ErrUnavailable` | Signet did not answer, no relay accepted the request, rate limiting or an internal Signet failure |
| `ErrInvalidRef` | malformed reference or another backend |
| `ErrNotFound`, `ErrRevoked`, `ErrExpired` | the credential's own state |
| `ErrDenied` | Signet refused this requester (no delivery grant, not the owner, deny-listed) |
| `ErrVersionMismatch` | the active version is not the pinned one |

### Use-time values and process-held keys

Most consumers resolve per operation: a deploy that injects a credential
resolves it, uses it and zeroizes it, and resolves again next time. A data
key that encrypts or authenticates continuously (assistant transcripts,
confidential-state dedupe) is resolved at startup and held in memory for the
life of the process, then zeroized at shutdown; the delivery lease still
bounds the delivery, not the in-memory lifetime.

## Access path

### Identities

| Identity | Holds | Talks to Signet's credential plane |
|---|---|---|
| Service identity (`nostr.signer`) | signs canonical state | never ([service-signer.md](service-signer.md): no Signet-specific calls for the service identity) |
| Requester (`secret_store.signet.client_key_file`) | a dedicated key local to the daemon | yes: seals every credential request |
| Owner (`secret_store.signet.owner`) | a Signet agent that owns Bahia's credentials | never connects; its identity policy names the requester |

The requester is never the service key; startup refuses a key file whose
pubkey is the service pubkey, and refuses the key from an environment
variable. Signet holds the owner's key and the credentials; nobody pairs a
client with the owner.

### Channel: Signet's encrypted ContextVM management plane

Signet exposes credential values on three paths (nostrc `signet/`):

| Path | Who it serves | Why Bahia uses it or not |
|---|---|---|
| ContextVM `credential/deliver` (`src/mgmt_protocol.c`, `SIGNET_MGMT_OP_DELIVER_CREDENTIAL`) | a requester holding an exact `credential.deliver` grant for the owner (`policy_store.c` `signet_policy_store_has_exact_delivery_grant`) | **used**: authenticated by the seal signature, encrypted end to end (NIP-59 gift wrap), relay-routed so the daemon need not share a host with Signet, audited, one-use lease |
| D-Bus `net.signet.Credentials.GetToken` (`src/dbus_common.c`) | the owning agent itself | not used: the Unix bus needs the daemon on Signet's host with an `SO_PEERCRED` UID mapping; the TCP bus is unencrypted GDBus (SASL ANONYMOUS) and its challenge must be signed by the owner's identity key |
| NIP-46 | — | Signet's NIP-46 server has no credential method |

Every path goes through `signet_credential_access_acquire`
(`src/credential_access.c`), which decides in order: deny list, capability
(for delivery, the exact grant), rate limit, ownership and status from
metadata before any decryption, type rules, lease, decrypt, then a mandatory
hash-chained audit entry; a success that cannot be audited is rolled back.

ContextVM is interactive RPC here, as AGENTS.md allows: the daemon asks for a
value and gets it back; no state is read through it.

### Wire contract

- Request: a kind-14 rumor `{"jsonrpc":"2.0","id":"<random>","method":"credential/inspect"|"credential/deliver"|"credential/list","params":{…}}`
  from the requester, sealed (kind 13, signed by the requester) and
  gift-wrapped (kind 1059, ephemeral key) to `secret_store.signet.pubkey`.
  Publication must be accepted by at least one relay or the call fails at
  once with `ErrUnavailable`.
- Reply: Signet gift-wraps a JSON-RPC result or error (`error.data` is the
  stable reason code, mapped to the sentinels). Signet backdates gift wraps
  by up to two days (NIP-59), so the store keeps one long-lived subscription
  `{"kinds":[1059],"#p":[<requester>],"since":now-49h}` and reissues it with
  the same reach after a reconnect. A reply counts only if its seal is signed
  by the configured Signet pubkey and the rumor's author is the seal's;
  replies are matched to callers by JSON-RPC id.
- `credential/deliver` returns `{credential_id, agent_id, payload_b64,
  lease_id, lease_expires_at}`; Signet mints and burns a 60-second lease in
  the same audited transaction.

### Pinning a version

`credential/deliver` releases the active version and does not report which
one. `Resolve` therefore runs `inspect` → `deliver` → `inspect`: Signet's
rotation increments the version atomically and never reuses a number, so the
same active version before and after proves the delivered value is the
pinned one. Any other outcome zeroizes the payload and returns
`ErrVersionMismatch`. A Signet `restore-db` can bring an older version number
back with a different future; after a restore, re-check references.

## Threat model

| Event | Exposure | Bahia's response |
|---|---|---|
| Service key leaks | can sign as the service and decrypt anything NIP-44'd to it | no secret value: the requester is a different key and records hold references only |
| Requester key leaks | can request delivery of the owner's credentials (and, while Signet requires delivery requesters to be provisioners, Signet management) | revoke the delivery grant and the provisioner grant; Signet's audit names the requester on every delivery |
| PostgreSQL leaks | nothing for store-backed secrets | legacy sealed values are deleted when a secret is re-pointed |
| Relay readers | gift-wrap sizes and timing; OCK ciphertext records | record names and service bindings are plaintext tags (tracked separately) |
| Signet unavailable | — | fail closed: intents are rejected with the reason, reveals refused, consumers stop; the `secret_store` health check warns; nothing falls back to PostgreSQL or a cached value |
| Forged reply | — | ignored unless sealed by the configured Signet key; the call fails closed at its deadline |
| Daemon memory | the resolved buffer | zeroized after use; Go strings produced while decoding the reply cannot be overwritten and stay until garbage collection; they are never logged or persisted |

Signet's own controls (SQLCipher store, per-agent envelope keys, audit chain,
backup/restore) are in nostrc `signet/docs/THREAT_MODEL.md`.

## Rotation and revocation

1. Rotate at the source, then `signetctl rotate-credential <credential-id> --stdin`
   (Signet archives the old version; the active version becomes N+1).
2. Re-point the secret: `bahia secrets set <service> <name> --credential-id <id> --credential-version <N+1>`.
   Between the two steps resolves fail with `ErrVersionMismatch`: the record
   names the value in force, and the store no longer releases it.
3. `signetctl revoke-credential <id>` makes every resolve fail with
   `ErrRevoked`; Signet expiry gives `ErrExpired`.

Rotating the requester key: create the new key file, grant it (provisioner
and the owner's delivery grant), restart or reload the daemon with the new
`client_key_file`, then revoke the old pubkey.

## Migration

Every value that passed through a secret intent (NIP-44-encrypted to the
leaked service pubkey and published on relays) or the PostgreSQL store
(sealed with a key derived from the leaked nsec) is compromised. Nothing is
migrated as-is and no tool reads old values to import them. For each secret:

1. issue a new credential at its source and revoke the old one there;
2. import it: `signetctl import-credential <owner> --type credential --label <label> --stdin`;
3. re-point the secret with a reference intent (`bahia secrets set … --credential-id … --credential-version 1`).
   The handler publishes the store-backed record and deletes the legacy
   sealed value from PostgreSQL.

With `secret_store.backend` set, intents carrying `encrypted_value` or
`value` are refused; without it the legacy path keeps working until it is
removed.

## Consumers

- Reveal (`services/secrets-reveal`) resolves store-backed secrets through the
  seam after the usual RBAC check and zeroizes the value once the response is
  built.
- Deploy-time injection, package registry, Hive-CI, adoption, direct
  runtime and relay-admin credentials (bahia-cd0wr.4.7) resolve the
  canonical record's `credential_ref` per operation instead of reading
  PostgreSQL; the HKDF store, `service_secret_data_keys` and the
  `encrypted_value` intent path are then deleted.
- The assistant transcript/checkpoint data key (bahia-cd0wr.4.8) and the
  confidential-state dedupe HMAC key (bahia-cd0wr.4.9) are random 32-byte keys
  the operator imports as Signet credentials; the daemon resolves them as
  process-held keys. They are never wrapped to the service pubkey.

## Configuration

```yaml
secret_store:
  backend: signet                     # empty: no store; reference intents refused
  signet:
    pubkey: <64-hex>                  # Signet's management identity ([nostr] bunker_pubkey)
    relays: [wss://signet-relay.example]
    client_key_file: /run/secrets/bahia-secret-store.key   # requester key, 64 hex, mounted file
    owner: bahia-vault                # Signet agent that owns Bahia's credentials
    request_timeout: 30s              # per Signet round trip
```

`secret_store` health reports the last round trip; a one-shot startup probe
(`credential/list` for the owner) runs as a non-required background runner.

## Tests

`internal/adapters/signet/credential_store_test.go` runs the store against an
in-process Signet credential plane on a real relay with NIP-42 recipient AUTH,
Signet's authorization order and reason codes, and replies backdated by 47
hours. `scripts/signet_secret_store_live.py` runs the opt-in live proof against
a disposable `signetd` (see the runbook).
