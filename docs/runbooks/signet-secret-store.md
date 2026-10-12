# Signet secret store

Bahia keeps secret values in Signet's generic secret store and only
credential references in its records. Design and threat model:
[`docs/architecture/secret-store.md`](../architecture/secret-store.md).

Three identities are involved; keep them distinct:

- the **service identity** (`nostr.signer`), which never talks to Signet's
  credential plane;
- the **requester**: a key file on the Bahia host that Signet authorizes to
  receive deliveries;
- the **owner**: a Signet agent that owns Bahia's credentials and that no
  client ever connects to.

## Prerequisites

- `signetd` with its persistent store (`SIGNET_DB_KEY`) and a file-backed
  policy store (`[policy_defaults] policy_file`). Cache-only Signet answers
  credential operations with `no_store`.
- Relays both Signet and the daemon use (`[nostr] relays` in Signet), operated
  by you: the daemon refuses a well-known public relay in
  `secret_store.signet.relays`.
- `signetctl` configured with a provisioner key, and `nak` for key handling.

## Set up

1. Create the owner. Do not hand its bunker URI to anyone:

   ```bash
   signetctl provision bahia-vault
   ```

2. Create the requester key on the Bahia host (64 hex, a regular file that
   only its owner can read; the daemon refuses a symlink or a group- or
   world-accessible file):

   ```bash
   install -m 0400 /dev/null /run/secrets/bahia-secret-store.key
   nak key generate > /run/secrets/bahia-secret-store.key
   REQUESTER=$(nak key public < /run/secrets/bahia-secret-store.key)
   ```

   It must not be the service key; the daemon refuses to start if it is.

3. Authorize the requester. Signet accepts credential requests only from a
   provisioner, and releases the owner's credentials only to the single
   client named in the owner's delivery policy:

   ```bash
   signetctl grant-provisioner "$REQUESTER"
   signetctl set-policy bahia-vault \
     "{\"default\":\"deny\",\"allow_clients\":[\"$REQUESTER\"],\"allow_methods\":[\"credential.deliver\"]}"
   ```

   The policy must name exactly one client and exactly
   `credential.deliver`; anything wider is not a delivery grant.

4. Configure the daemon and restart (or reload) it:

   ```yaml
   secret_store:
     backend: signet
     signet:
       pubkey: "<signetd [nostr] bunker_pubkey>"
       relays: ["wss://signet-relay.example.com"]
       client_key_file: /run/secrets/bahia-secret-store.key
       owner: bahia-vault
   ```

5. Check `GET /health`: the `secret_store` check passes once the startup
   probe (`credential/list` for the owner) has answered.

6. Create the daemon's [system credentials](#system-credentials).

## Store a secret

Name every credential `<org-id>/<service-id>/<name>`: the label binds it to
its tenant. The daemon refuses (and never delivers) a credential whose label
is not in the secret's org and service, so a credential labelled for one
service cannot be referenced from another, even within the same org. The
label is unique per owner and Signet derives the credential id from it.

```bash
signetctl import-credential bahia-vault --type credential \
  --label "<org-id>/<service-id>/DATABASE_URL" --file /run/operator/database-url
signetctl list-credentials bahia-vault          # credential_id, version, status
bahia secrets set <service-id> DATABASE_URL \
  --credential-id cred_<64 hex> --credential-version 1
```

The intent carries only the reference. The daemon refuses it unless the
credential exists, is active and is at that version.

## System credentials

The daemon's own data keys live in Signet under the same owner, labelled
`bahia-system/<purpose>/<key-id>` ([design](../architecture/secret-store.md#system-credentials)):
`assistant-transcript` (required when `assistant.enabled`) and
`confidential-dedupe` (without it confidential records such as orgs, members,
secrets and payments are not published). Each is 32 random bytes; pick a key
id that names the key, for example the month it was created.

```bash
umask 077
head -c 32 /dev/urandom > /run/operator/assistant-transcript.key
signetctl import-credential bahia-vault --type credential \
  --label bahia-system/assistant-transcript/2026-10 --file /run/operator/assistant-transcript.key
head -c 32 /dev/urandom > /run/operator/confidential-dedupe.key
signetctl import-credential bahia-vault --type credential \
  --label bahia-system/confidential-dedupe/2026-10 --file /run/operator/confidential-dedupe.key
shred -u /run/operator/assistant-transcript.key /run/operator/confidential-dedupe.key
signetctl list-credentials bahia-vault          # credential_id and version of each
```

Reference them in the daemon's configuration and restart it:

```yaml
secret_store:
  system_keys:
    assistant_transcript:
      - {key_id: "2026-10", credential_id: cred_<64 hex>, version: 1}
    confidential_dedupe:
      - {key_id: "2026-10", credential_id: cred_<64 hex>, version: 1}
```

The daemon resolves them once at startup and holds them until shutdown. The
`system_keys` health check fails readiness until both are resolved; a label
that does not match the entry's purpose and key id is refused.

**Changing the dedupe key.** Import a new `bahia-system/confidential-dedupe/<key-id>`
credential (or `rotate-credential` the current one and bump `version`), point
`confidential_dedupe` at it and restart. Every digest changes, so each
confidential record is republished once, when its producer next publishes
it, through the outbox and the `state` lane's budget, and deduplicated again
from then on; nothing is republished in bulk. Revoke the old credential.

**Assistant transcript keys** are rotated by adding a new key id, never with
`rotate-credential` (Signet delivers only the active version, so history
sealed under the old version would become unreadable). See
[`assistant-transcript-keys.md`](assistant-transcript-keys.md).

| `system_keys` health message | Cause |
|---|---|
| `resolving system keys from the secret store` | startup resolution has not finished |
| `system keys are not resolved … retrying: …` | Signet is unreachable or refused one of the keys: `credential is not labelled bahia-system/<purpose>/<key-id>` (wrong credential or label), `version does not match` (configured version is not the active one), `is N bytes, want 32` (not a 32-byte key), `refused (no_capability)` (delivery grant) |
| `confidential records are not published: … confidential_dedupe is not configured` | no dedupe key; confidential publishes fail closed |

## Rotate

```bash
signetctl rotate-credential cred_<64 hex> --file /run/operator/database-url.new
bahia secrets set <service-id> DATABASE_URL --credential-id cred_<64 hex> --credential-version 2
```

Between the two commands every use of the secret fails closed with a
version mismatch: run them together.

## Revoke and delete

```bash
signetctl revoke-credential cred_<64 hex>       # every use fails closed
bahia secrets delete <service-id> <secret-id>   # tombstones the record
signetctl delete-credential cred_<64 hex> --confirm
```

## Migrate an existing secret

Values that went through a secret intent or the PostgreSQL store are
compromised; do not copy them. Until bahia-cd0wr.4.7, a re-pointed secret
can be revealed but deploys that inject it fail loudly. For each secret: issue a new value at its
source and revoke the old one there, import the new value (above), then run
`bahia secrets set … --credential-id … --credential-version 1` for the same
service and name. The daemon re-points the existing secret and deletes its
sealed PostgreSQL value. Once `secret_store` is configured, intents that
carry values are refused.

## Rotate the requester key

Create the new key file, run step 3 for its pubkey, point
`client_key_file` at it and restart or reload the daemon, then
`signetctl revoke-provisioner <old-pubkey>`.

## Troubleshooting

| `secret_store` health message | Cause |
|---|---|
| `no Signet round trip yet` | the startup probe has not finished |
| `Signet did not answer … within …` | Signet or its relays are down, the relays do not serve the requester's gift wraps (NIP-42), or the requester is not a provisioner (Signet's refusal carries no request id, so it surfaces as a timeout) |
| `no relay accepted the Signet request` | every relay refused the gift wrap (the message lists each relay's reason), or outbound admission refused it (`budget exhausted`, circuit breaker, kill switch) |
| `refused (no_capability)` | the owner's delivery policy does not name this requester |
| `refused (not_owner)` / `credential is not owned by …` | the credential belongs to another owner |
| `credential is not labelled <org-id>/<service-id>/<name>` | the credential's label is not in the secret's org and service; import it under the right label |
| `… reply subscription is not caught up …; reconnecting` | every relay ended the reply subscription; it is reopened with backoff and requests fail closed until a relay serves it |

A deploy (or other consumer) failing with `secret value is held by the
secret store and this consumer still reads PostgreSQL` names a store-backed
secret whose consumer moves to the secret store in bahia-cd0wr.4.7; until
then such secrets can be revealed but not injected. Run logs of a deployment
that references such a secret are refused for the same reason: redaction
cannot remove a value it cannot read, and the logs are never returned
unredacted.

| `secret_store_index` (fails readiness) | Cause and fix |
|---|---|
| `… another secret already holds this service, environment and name …; rows [...] are blocked` | two secrets share an effective name; the other row is blocked so nothing serves it. Delete one of the two (`bahia secrets delete`); the pass retries and the check passes |
| any other reason | the warm-start pass could not index a store-backed secret (PostgreSQL unreachable, unreadable record); it retries with backoff |

A reference intent rejected with `another secret already holds this service,
environment and name … re-point that secret instead` changed nothing: run
`bahia secrets set` for the existing secret (it reuses its id) instead of
creating a second one.

`credential is not available to this org and service` means the credential
does not exist, is labelled for another org or service, or Signet refused it
to this daemon; the daemon's Debug log has the detail.

`service <id> has no canonical record` names a service created before
relay-canonical state: run `bahia services update --org <org-id> --service
<id>` once, then retry.

Each secret resolve publishes three gift wraps on the outbound `priority`
lane, shared with operator results and ContextVM replies; size
`nostr.outbound.lanes.priority` for the number of secrets an operation
resolves at once ([outbound admission](nostr-outbound-admission.md)).

Signet's audit chain records every delivery with the requester pubkey:
`signetctl verify-audit`.

## Live proof

`scripts/signet_secret_store_live.py --signet-repo <nostrc checkout> --build-dir <cmake build> --signet-commit <sha>`
builds `signetd`/`signetctl` from a clean nostrc commit, runs them on a
private loopback relay with a disposable store, sets up an owner, a
requester, a tenant credential and the two system credentials exactly as
above, and runs Bahia's opt-in `TestLiveSignetCredentialStore`
(`-tags signetinterop`): resolve the pinned version, resolve the system keys
and refuse every system/tenant crossover and a key configured under the
wrong purpose, refuse a stale version after rotation, resolve the new
version, fail closed after revocation and without a delivery grant.
