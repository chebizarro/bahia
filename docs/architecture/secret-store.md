# Secret store

Secret values live in an external secret store: Signet's generic secret
store. Bahia's canonical records carry only a reference to the credential;
the daemon fetches a value at the moment an operation needs it, under a
short store-issued lease, holds it in memory and zeroizes it. A leaked
service key therefore exposes no secret value, and losing PostgreSQL loses
none either.

Code: `internal/secretstore` (the seam, and `Resolver`/`TextResolver` that
consumers use), `internal/adapters/signet/credential_store.go`
(the Signet implementation), `internal/app/secret_store.go` (wiring and
process-held configuration credentials),
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
| `ErrDenied` | Signet refused this requester (no delivery grant, not the owner, deny-listed), or the credential is outside the caller's scope |
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
- `credential/deliver` returns `{credential_id, agent_id, version,
  payload_b64, lease_id, lease_expires_at}` and accepts `expected_version`;
  Signet mints and burns a 60-second lease in the same audited transaction.
- Signet refuses management requests whose timestamp is more than 120 s
  from its clock (`request_time_skew`, `ErrUnavailable`).
- Every request is one gift wrap ([outbound
  admission](../runbooks/nostr-outbound-admission.md)): an `Inspect` or
  `Health` costs one, a `Resolve` one to three (below), plus the reply
  subscription's NIP-42 AUTH. A lone `Resolve`, `Inspect` or `Health` goes
  on the process-wide `priority` lane and fails fast: an exhausted budget is
  `ErrUnavailable` and the request is not queued or retried. `ResolveAll`
  declares one bounded operation (three gift wraps per request at most) and
  is paced on the `bulk` lane instead; its requests are sent one at a time,
  each after the previous one's relay OKs.

### Pinning a version

`Resolve` calls `credential/deliver` with `expected_version`. Signet
(0.7.0+) reports the delivered `version` and refuses a mismatch with
`version_mismatch`, which settles the pin in that reply. The tenant must be
proven before anything is delivered, and the deliver reply carries no
label, so:

- The first resolve of a credential inspects it first (owner, tenant label,
  status, version): two round trips. The store then keeps the label of
  every credential an inspect admitted. A credential id is Signet's digest
  of (owner, type, label), so an id's label never changes.
- Every later resolve of that credential in a scope its label is in is one
  `credential/deliver`: one round trip. A scope the cached label is not in
  takes the full path and is refused before delivery.
- A Signet that does not report the version (it ignores
  `expected_version`) is followed by a second inspect: rotation increments
  the version atomically and never reuses a number, so the same active
  version before and after proves the delivered value is the pinned one.
  Three round trips. A one-round-trip delivery that comes back without a
  version is discarded and the resolve repeats on this path.

The store detects which by the reply; nothing is configured. Any mismatch
zeroizes the payload and returns `ErrVersionMismatch`. Signet echoes the
request id in its refusals, so an unauthorized requester gets an immediate
`ErrDenied`; it answers an ungranted and a missing credential alike
(`no_capability`). A Signet `restore-db` can bring an older version number
back with a different future; after a restore, re-check references.

## Threat model

| Event | Exposure | Bahia's response |
|---|---|---|
| Service key leaks | can sign as the service and decrypt anything NIP-44'd to it | no secret value: the requester is a different key and records hold references only |
| Requester key leaks | can inspect, list and request delivery of the owner's credentials, nothing else (it holds only the owner's exact delivery grant) | replace the delivery grant; Signet's audit names the requester on every delivery; the key file must be a regular owner-only file |
| An org's operator references another tenant's credential (ids are computable) | — | refused at intent time and on every resolve and reveal: the credential's label is outside `<org-id>/<service-id>/` |
| PostgreSQL leaks | nothing for store-backed secrets; legacy sealed values not yet re-pointed (already compromised) | legacy sealed values are scrubbed when a secret is re-pointed and never decrypted meanwhile |
| Relay readers | gift-wrap sizes and timing; OCK ciphertext records | record names and service bindings are plaintext tags (tracked separately) |
| Signet unavailable | — | fail closed: intents are rejected with the reason, reveals refused, consumers stop; the `secret_store` health check warns; nothing falls back to PostgreSQL or a cached value |
| Forged reply | — | ignored unless sealed by the configured Signet key; the call fails closed at its deadline |
| Daemon memory | the resolved buffer | zeroized after use, as are the byte copies Bahia decodes the reply from. The NIP-44 plaintexts of the reply are Go strings and cannot be overwritten; they stay until garbage collection. Reveal, runtime environments, redaction and text consumers (`TextResolver`) copy the value into Go strings, which likewise cannot be zeroized; process-held configuration credentials stay for the life of the process. None of these is logged or persisted. |

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

Rotating the requester key: create the new key file, name it in the owner's
delivery grant (which withdraws the old key's), then restart or reload the
daemon with the new `client_key_file`.

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

Bahia never decrypts a legacy value. A secret whose canonical record has no
`credential_ref`, or whose PostgreSQL index row is not a `store-ref` marker,
is refused by every consumer with `domain.ErrLegacySecretValue`, naming the
secret and the remedy above. Secret intents never carry a value: an intent
with `encrypted_value` or `value` is refused.

## Consumers

Consumers resolve values through `secretstore.Resolver` at use time:

1. the secret's canonical record (`CanonicalSecretView`, the local event
   store, no PostgreSQL) supplies `credential_ref`;
2. the service's canonical org and the secret's service form the scope;
3. the store delivers the pinned version under its lease;
4. the consumer uses the value and zeroizes the store's buffer.

`Resolve` serves one secret; `ResolveAll` serves the secrets of one
operation, all or nothing, as one declared outbound operation paced on the
`bulk` lane (it waits behind another active bulk operation for up to
`request_timeout`, then fails with `ErrUnavailable`). Without a store every
resolve fails with `secretstore.ErrNotConfigured`; nothing falls back.

| Consumer | Resolves | Without a resolvable value |
|---|---|---|
| Direct runtime deploys (`runtime_lifecycle.go`) | the service's effective secrets (listed from the PostgreSQL index), one batch per apply | the deploy fails, naming the secret; nothing is injected |
| Runtime action error scrubbing | the same set | error details are withheld |
| VM bootstrap (`vm_bootstrap.go`) | the bindings, one batch; the guest receives the store's buffer | delivery fails; admission refuses legacy secrets |
| VM plane secrets (`virtualization_policy.go`) | nothing (admission check) | legacy secrets are refused |
| Hive-CI build requests and initiation (`encrypted_build_handlers.go`, `adapters/gitea`) | the upstream and mirror-read credentials, one batch | the initiation stays `claimed`; legacy credentials are refused up front |
| Reveal (`services/secrets-reveal`) | the one secret, in its service's canonical org | refused |
| Run-log redaction (`deployments/run-logs-get`) | every secret the run's desired state references, one batch | the logs are withheld, never returned unredacted |
| Edge routing API token, relay administrator key, package backend credentials | on first use, then held for the process (below) | the feature's calls fail closed |
| LLM gateway/health headers (`adapters/llm`) | per call, through `secretstore.TextResolver` | the call fails |

Consumers whose APIs take text (HTTP headers, Git credentials, registry
passwords) use `secretstore.TextResolver`, which copies the value into a Go
string and zeroizes the store's buffer. With PostgreSQL configured every
resolve is also recorded in `secret_access_audit` (actor, reason, credential
version; no PostgreSQL version row exists for a store-backed secret).

- **Run logs.** The store releases only the version a secret is pinned to
  now. Redaction refuses the logs of a run that references a legacy secret,
  a secret without a record, or a secret re-pointed after the run began
  (its record's `updated_at` is later than the run's `created_at`): the run
  may contain a value redaction cannot know (`ErrRunLogsUnredactable`).
- **Process-held configuration credentials.** `edge_routing.api_token_ref`,
  `nostr.relay_administration.administrator_private_key_ref` and package
  backend `auth_secret_ref`/`tls_secret_ref`/`secret_refs` name service
  secrets. They resolve on the feature's first use, not at startup, so the
  daemon starts while Signet or the canonical record is unavailable; a failed
  resolve is not cached. The edge routing token and the relay administrator
  key follow a re-point on their next use (the canonical reference is
  compared locally on every use); a package backend holds its credentials
  until restart.
- **Adoption** imports no values: a container whose environment carries
  secret values inline is refused, naming the keys (Bahia cannot write into
  the store, and only a store administrator chooses labels).
- **PostgreSQL index.** With PostgreSQL configured, a store-backed secret
  keeps a value-less index row (`encryption_method = store-ref`; every
  retained legacy ciphertext of that id scrubbed, access audit rows kept),
  which deploys use to list a service's effective secrets. Markers are
  written before the canonical publish and, after warm start, for every
  store-backed record the local store holds. A consumer refuses an index
  row that disagrees with its canonical record (service or name).
- **Name collisions.** Deploys pick a secret by service, environment and
  name. A store-backed secret whose name another index row already holds (a
  different secret id) would let them serve that row. A reference intent
  that would collide is refused with the remedy, changing nothing. A
  canonical store-backed record found colliding by the warm-start index pass
  blocks the other row (it becomes a scrubbed store-ref marker, so nothing
  serves it). The `secret_store_index` health check fails readiness with the
  secret's name until one of the two secrets is deleted, and the pass retries
  with backoff (1s to 5m). The check also fails, with the reason, while any
  store-backed secret cannot be indexed.
- The assistant transcript/checkpoint data key (bahia-cd0wr.4.8) and the
  confidential-state dedupe HMAC key (bahia-cd0wr.4.9) are random 32-byte keys
  the operator imports as Signet credentials; the daemon resolves them as
  process-held keys. They are never wrapped to the service pubkey.

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
  backend: signet                     # empty: no store; every secret-dependent operation fails closed
  signet:
    pubkey: <64-hex>                  # Signet's management identity ([nostr] bunker_pubkey)
    relays: [wss://signet-relay.example]
    client_key_file: /run/secrets/bahia-secret-store.key   # requester key, 64 hex, mounted file
    owner: bahia-vault                # Signet agent that owns Bahia's credentials
    request_timeout: 30s              # per Signet round trip
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
hours. `scripts/signet_secret_store_live.py` runs the opt-in live proof against
a disposable `signetd` (see the runbook).
