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

### Tenant binding

One Signet owner holds every tenant's credentials and Signet releases all of
them to the single requester, and credential ids are unkeyed digests of
(owner, type, label), so anyone can compute one. A credential is therefore
bound to its tenant by its **label**, which must be
`<org-id>/<service-id>/<name>`. Only a Signet provisioner can import a
credential and choose its label; Bahia org roles cannot. `Inspect` and
`Resolve` take a `secretstore.Scope{OrgID, ServiceID}` and refuse
(`ErrDenied`) any credential whose label is outside
`<org-id>/<service-id>/`:

- at intent time, with the intent's `org` (which must be the service's
  canonical org) and the secret's service;
- on every resolve and reveal, with the service's canonical org, inside the
  store, before anything is delivered.

The refusal never echoes the other tenant's label, and credential ids are
kept out of Info logs and of intent status text (status events are relay
records). A credential that does not exist and one that belongs to another
tenant (or that Signet refuses to this daemon) give the same public reason,
`credential is not available to this org and service`, so intent status is
no oracle for another tenant's credential ids; the detail is logged at Debug.
Errors that reach intent status and reveal responses name no relay: relay
refusals are logged, and callers see only that no relay accepted the request.

### The seam

```go
type SecretStore interface {
    // Resolve fetches exactly ref.Version of a credential of scope under a
    // short lease; the caller owns the Value and must Zeroize it.
    Resolve(ctx context.Context, ref domain.SecretCredentialRef, scope secretstore.Scope) (*secretstore.Value, error)
    // Inspect returns payload-free metadata (active version, status, owner,
    // label) of a credential of scope.
    Inspect(ctx context.Context, credentialID string, scope secretstore.Scope) (secretstore.Metadata, error)
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
| `ErrDenied` | Signet refused this requester (no delivery grant, not the owner, deny-listed, not a provisioner), or the credential is outside the caller's scope |
| `ErrVersionMismatch` | the active version is not the pinned one |

### Use-time values and process-held keys

Most consumers resolve per operation: a deploy that injects a credential
resolves it, uses it and zeroizes it, and resolves again next time. A data
key that encrypts or authenticates continuously (assistant transcripts,
confidential-state dedupe) is resolved at startup and held in memory for the
life of the process, then zeroized at shutdown; the delivery lease still
bounds the delivery, not the in-memory lifetime.

### System credentials

The daemon's own data keys are **system credentials**: 32 random bytes the
operator imports into Signet under the same owner, labelled
`bahia-system/<purpose>/<key-id>` (`internal/secretstore/system.go`).

| Purpose | Label | Used for |
|---|---|---|
| `assistant-transcript` | `bahia-system/assistant-transcript/<key-id>` | XChaCha20-Poly1305 key of assistant transcripts (`30316`) and execution checkpoints (`4903`) |
| `confidential-dedupe` | `bahia-system/confidential-dedupe/<key-id>` | HMAC-SHA256 key of the `state_hash` tag of confidential cp-state records |

- **Namespaces never cross.** A system scope (`secretstore.SystemScope`,
  constructible only inside `internal/secretstore`) admits exactly one label,
  `bahia-system/<purpose>/<key-id>`. A tenant scope admits only
  `<org-id>/<service-id>/<name>` and refuses the `bahia-system/` namespace
  outright; a tenant label starts with an org UUID, so it can never equal a
  system label. A secret intent naming a system credential's id is refused
  like any other tenant's credential, and a system key reference naming a
  tenant credential (or another purpose's or key id's credential) is refused
  with `ErrDenied` before delivery.
- **References live in configuration** (`secret_store.system_keys`), one list
  per purpose, each entry an explicit `key_id`, `credential_id` and `version`.
  The first entry of a purpose is its active key; later entries only decrypt
  data sealed under them.
- **Startup resolution.** The `system-keys` runner resolves every configured
  key once, all or nothing, each exactly 32 bytes, retrying with backoff
  (1s to 5m) while Signet is unreachable or refuses. Consumers wait for it
  within their own deadline and otherwise fail closed with `ErrUnavailable`;
  nothing falls back to a derived or cached key. The `system_keys` health
  check fails readiness until every key is resolved, and warns while no
  `confidential_dedupe` key is configured (confidential records are then not
  published). Keys are zeroized at shutdown and never leave the keyring except
  through `Keyring.Use`.
- Nothing about a system key is derived from, or wrapped to, the service key.

Ciphertext names the key that sealed it: an assistant event's `key_ref` is
the credential's label and `key_version` its Signet version, so a key is
never applied to data sealed under another. Signet delivers only a
credential's active version, so a transcript key is rotated by importing a
new key id, never with `rotate-credential`
([runbook](../runbooks/assistant-transcript-keys.md)).

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
  by the configured Signet pubkey, the rumor's author is the seal's, the
  rumor is kind 14 and its `p` tag is the requester; replies are matched to
  callers by JSON-RPC id, and a reply nobody waits for is dropped. Size,
  `p` tag, id and signature are checked before any ECDH.
- The subscription is supervised: requests wait (within their deadline)
  until it has reached EOSE; when every relay ends it for good it is
  reopened with exponential backoff (1s to 1m), and the `secret_store` health
  check reports it down and reconnecting meanwhile.
- Only the method and Signet's stable reason code (`error.data`) reach
  callers; Signet's free-text message does not.
- `credential/deliver` returns `{credential_id, agent_id, payload_b64,
  lease_id, lease_expires_at}`; Signet mints and burns a 60-second lease in
  the same audited transaction.
- Every request is one gift wrap on the process-wide outbound `priority`
  lane ([outbound admission](../runbooks/nostr-outbound-admission.md)): a
  `Resolve` costs three, an `Inspect` or `Health` one, plus the reply
  subscription's NIP-42 AUTH. Admission fails fast; an exhausted budget is
  `ErrUnavailable` and the request is not queued or retried.

### Pinning a version

`Resolve` first inspects the credential (owner, tenant label, status,
version), then calls `credential/deliver` with `expected_version`:

- A Signet that reports the delivered `version` (nostrc-pvj7b) settles the
  pin in that reply and refuses a mismatch with `version_mismatch`: two round
  trips.
- A Signet that does not (it ignores `expected_version`) is followed by a
  second inspect: rotation increments the version atomically and never
  reuses a number, so the same active version before and after proves the
  delivered value is the pinned one. Three round trips.

The store detects which by the reply; nothing is configured. Any mismatch
zeroizes the payload and returns `ErrVersionMismatch`. Likewise a Signet
that echoes the request id in its refusal of an unauthorized requester
(nostrc-edjyc) yields an immediate `ErrDenied` instead of a timeout, and a
Signet that authorizes delivery by the grant alone (nostrc-v13e0) needs no
change here: the requester then no longer has to be a provisioner. A Signet `restore-db` can bring an older version number
back with a different future; after a restore, re-check references.

## Threat model

| Event | Exposure | Bahia's response |
|---|---|---|
| Service key leaks | can sign as the service and decrypt anything NIP-44'd to it | no secret value: the requester is a different key and records hold references only |
| Requester key leaks | can request delivery of the owner's credentials (and, while Signet requires delivery requesters to be provisioners, Signet management) | revoke the delivery grant and the provisioner grant; Signet's audit names the requester on every delivery; the key file must be a regular owner-only file |
| An org's operator references another tenant's credential (ids are computable) | — | refused at intent time and on every resolve and reveal: the credential's label is outside `<org-id>/<service-id>/` |
| An org's operator references a system credential | — | refused the same way: tenant scopes refuse the `bahia-system/` namespace |
| Daemon memory (system keys) | the assistant-transcript and confidential-dedupe keys for the life of the process | the keyring's copies are zeroized at shutdown; the per-operation copy of the transcript key an AEAD call uses is left to the garbage collector; a leaked transcript key exposes assistant history sealed under it, so rotate it (new key id) |
| PostgreSQL leaks | nothing for store-backed secrets | legacy sealed values are deleted when a secret is re-pointed |
| Relay readers | gift-wrap sizes and timing; OCK ciphertext records | record names and service bindings are plaintext tags (tracked separately) |
| Signet unavailable | — | fail closed: intents are rejected with the reason, reveals refused, consumers stop; the `secret_store` health check warns; nothing falls back to PostgreSQL or a cached value |
| Forged reply | — | ignored unless sealed by the configured Signet key; the call fails closed at its deadline |
| Daemon memory | the resolved buffer | zeroized after use, as are the byte copies Bahia decodes the reply from. The NIP-44 plaintexts of the reply are Go strings and cannot be overwritten; they stay until garbage collection. Reveal copies the value into a Go string for its JSON response, which likewise cannot be zeroized. None of these is logged or persisted. |

Signet's own controls (SQLCipher store, per-agent envelope keys, audit chain,
backup/restore) are in nostrc `signet/docs/THREAT_MODEL.md`.

## Rotation and revocation

1. Rotate at the source, then `signetctl rotate-credential <credential-id> --stdin`
   (Signet archives the old version; the active version becomes N+1).
2. Re-point the secret: `bahia secrets set <service> <name> --credential-id <id> --credential-version <N+1>`.
   (Rotation keeps the credential's id and label.)
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
2. import it with its tenant label: `signetctl import-credential <owner> --type credential --label <org-id>/<service-id>/<name> --stdin`;
3. re-point the secret with a reference intent (`bahia secrets set … --credential-id … --credential-version 1`).
   The handler turns the PostgreSQL row into a value-less marker, scrubbing
   every retained legacy ciphertext of that id, then publishes the
   store-backed record.

Until bahia-cd0wr.4.7, deploys and other PostgreSQL consumers of a re-pointed
secret fail with `ErrSecretInStore`; plan re-pointing with that migration.

With `secret_store.backend` set, intents carrying `encrypted_value` or
`value` are refused; without it the legacy path keeps working until it is
removed.

## Consumers

- Reveal (`services/secrets-reveal`) takes the org from the service's
  canonical record, checks the requester's read permission in that org,
  resolves store-backed secrets through the seam in that org and service
  scope, and zeroizes Bahia's buffer once the response is built. It needs no
  PostgreSQL service row.
- Every other consumer still reads values from PostgreSQL and moves to the
  seam in bahia-cd0wr.4.7: runtime injection (`runtime_lifecycle.go`), the
  `secrets.Resolver` users (package registries, LLM provisioning, Gitea/Hive-CI
  build credentials, relay administration), VM bootstrap and VM plane
  secrets, build requests and run-log redaction. With PostgreSQL configured,
  a store-backed secret keeps a value-less index row
  (`encryption_method = store-ref`; every retained legacy ciphertext of that
  id scrubbed, access audit rows kept). Those consumers refuse it with
  `domain.ErrSecretInStore`, naming the secret and bahia-cd0wr.4.7, instead
  of deploying without it, and redaction refuses the logs rather than return
  them unredacted. Markers are written before the canonical publish and, after
  warm start, for every store-backed record the local store holds.
- **Run logs.** Run-log retrieval redacts every value of the secrets a
  deployment references before returning the logs. A store-backed secret's
  value is not in PostgreSQL, so redaction cannot know what to remove; the
  logs of a run that references a re-pointed or store-backed secret are
  refused (`ErrSecretInStore`) rather than returned unredacted, until
  redaction resolves values through the seam in bahia-cd0wr.4.7.
- **Name collisions.** PostgreSQL consumers pick a secret by service,
  environment and name. A store-backed secret whose name another index row
  already holds (a different secret id) would let them serve that row. A
  reference intent that would collide is refused with the remedy, changing
  nothing. A canonical store-backed record found colliding by the warm-start
  index pass blocks the other row (it becomes a scrubbed store-ref marker, so
  nothing serves it). The `secret_store_index` health check fails readiness
  with the secret's name until one of the two secrets is deleted, and the pass
  retries with backoff (1s to 5m). The check also fails, with the reason,
  while any store-backed secret cannot be indexed.
- In bahia-cd0wr.4.7 these consumers resolve the canonical record's
  `credential_ref` per operation; the HKDF store, `service_secret_data_keys`
  and the `encrypted_value` intent path are then deleted.
- The assistant transcript/checkpoint key and the confidential-state dedupe
  HMAC key are [system credentials](#system-credentials), resolved at startup
  and held for the life of the process.

## Services without a canonical record

A secret record is encrypted for its service's org, read from the service's
canonical record. A service created before relay-canonical state can exist
only in PostgreSQL; publishing or revealing its secrets then fails closed
with `service <id> has no canonical record: publish it with
bahia services update --org <org-id> --service <id>, then retry`.
Running that command publishes the record; nothing falls back to the
PostgreSQL row's org.

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
  system_keys:                        # the daemon's own data keys (system credentials)
    assistant_transcript:             # required when assistant.enabled; first entry is active
      - key_id: "2026-10"             # label bahia-system/assistant-transcript/2026-10
        credential_id: cred_<64 hex>
        version: 1
    confidential_dedupe:              # at most one; without it confidential records are not published
      - key_id: "2026-10"             # label bahia-system/confidential-dedupe/2026-10
        credential_id: cred_<64 hex>
        version: 1
```

`secret_store.signet.relays` is the store's own explicit relay list: relays
Signet and the daemon both use. A well-known public relay is refused at
configuration (`nostr.allowed_public_relays` does not apply), so secret
requests and deliveries never touch a public relay. These relays need not be
in `nostr.controlled_relays`: the store's pool authenticates as the
requester, never as the service, and publishes no control-plane event.

`secret_store` health reports the last round trip; a one-shot startup probe
(`credential/list` for the owner) runs as a non-required background runner.

## Tests

`internal/adapters/signet/credential_store_test.go` runs the store against an
in-process Signet credential plane on a real relay with NIP-42 recipient AUTH,
Signet's authorization order and reason codes, and replies backdated by 47
hours, including system scopes (`TestCredentialStoreSystemScope`).
`internal/secretstore/system_test.go` covers scope separation and the keyring.
`scripts/signet_secret_store_live.py` runs the opt-in live proof against a
disposable `signetd`, tenant and system credentials (see the runbook).
