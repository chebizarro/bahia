# Signet secret store

Bahia keeps secret values only in Signet's generic secret store and only
credential references in its records; every secret-dependent feature
(deploys, VM bootstrap, build credentials, reveal, run-log redaction, edge
routing, relay administration, package backends) resolves values through it
and fails closed without it. Design and threat model:
[`docs/architecture/secret-store.md`](../architecture/secret-store.md).

Three identities are involved; keep them distinct:

- the **service identity** (`nostr.signer`), which never talks to Signet's
  credential plane;
- the **requester**: a key file on the Bahia host that Signet authorizes to
  receive deliveries;
- the **owner**: a Signet agent that owns Bahia's credentials and that no
  client ever connects to.

## Prerequisites

- `signetd` 0.7.0 or later with its persistent store (`SIGNET_DB_KEY`) and a
  file-backed policy store (`[policy_defaults] policy_file`). Cache-only
  Signet answers credential operations with `no_store`.
- Clocks within two minutes of Signet's: Signet refuses management requests
  outside ±120 s (`request_time_skew`).
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

3. Authorize the requester with the owner's exact delivery grant. That
   grant is all it holds: with it, Signet lets it inspect, list (naming the
   owner) and deliver the owner's credentials, and nothing else. Do not make
   it a provisioner.

   ```bash
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

Values that went through a secret intent (NIP-44-encrypted to the leaked
service key and published) or the PostgreSQL store (sealed with a key
derived from it) are compromised; do not copy them, and Bahia never
decrypts them. Every consumer refuses such a secret, naming it, with
`secret holds a legacy value sealed with the retired service key`. For each
secret:

1. issue a new value at its source and revoke the old one there;
2. import the new value labelled `<org-id>/<service-id>/<name>` (above);
3. `bahia secrets set <service-id> <name> --credential-id cred_<64 hex> --credential-version 1`
   for the same service and name (and `--environment` for an
   environment-specific secret).

The daemon re-points the existing secret (same id) and scrubs its sealed
PostgreSQL value; deploys, reveal and the other consumers then resolve it
through the store. `bahia-raw-key-crypto-census` counts the legacy sealed
rows still in PostgreSQL by method; the refusals above name each secret. Run logs of deployments that ran before a secret was
re-pointed stay withheld: the store releases only the current version, so
redaction cannot remove the old value.

Adoption imports no secret values: a container whose environment carries
secret values inline is refused, naming the keys. Move those values out of
the container, or import them into the store and deploy the service through
Bahia.

Daemon credentials named in configuration by secret id
(`edge_routing.api_token_ref`,
`nostr.relay_administration.administrator_private_key_ref`, package backend
`auth_secret_ref`/`tls_secret_ref`/`secret_refs`) are service secrets too:
migrate them the same way. They resolve on first use and are held until the
store's delivery lease ends (60 s): the next use resolves again, so a re-point,
a revocation or an expiry takes effect within a minute, and a deleted or
unresolvable secret drops the held value.

### Migrations

- `000082_secret_access_audit_store_backed` makes
  `secret_access_audit.secret_version_id` nullable: a store-backed resolve
  has no PostgreSQL version row. Its down migration refuses once such audit
  rows exist (delete them first if you must roll back).
- `000083_drop_service_secret_data_keys` drops `service_secret_data_keys`.
  It held at most wrapped data keys of a format whose writer and rekey were
  never wired into the daemon: never a secret value, and no production
  secret was sealed with it. A row still marked `aes256gcm-v2` would be a
  legacy value every consumer refuses.

Access audit rows reference the secret's index row and its service row.
Deploys and VM bootstrap fail if their audit row cannot be written;
configuration credentials, text consumers and run-log redaction log the
failure and proceed (a store-backed secret may not be indexed yet).

## Rotate the requester key

Create the new key file, run step 3 for its pubkey (the policy names one
client, so this withdraws the old key's grant), point `client_key_file` at
it and restart or reload the daemon.

## Troubleshooting

| `secret_store` health message | Cause |
|---|---|
| `no Signet round trip yet` | the startup probe has not finished |
| `Signet did not answer … within …` | Signet or its relays are down, or the relays do not serve the requester's gift wraps (NIP-42) |
| `refused (request_time_skew)` | the daemon's clock is more than two minutes from Signet's |
| `no relay accepted the Signet request` | every relay refused the gift wrap (the message lists each relay's reason), or outbound admission refused it (`budget exhausted`, circuit breaker, kill switch) |
| `refused (no_capability)` | the owner's delivery policy does not name this requester, or the credential does not exist (Signet answers both alike) |
| `refused (not_owner)` / `credential is not owned by …` | the credential belongs to another owner |
| `credential is not labelled <org-id>/<service-id>/<name>` | the credential's label is not in the secret's org and service; import it under the right label |
| `… reply subscription is not caught up …; reconnecting` | every relay ended the reply subscription; it is reopened with backoff and requests fail closed until a relay serves it |

A deploy (or other consumer) failing with `secret holds a legacy value
sealed with the retired service key` names a secret to migrate (above).
`secret store is not configured` means `secret_store.backend` is empty:
every secret-dependent operation fails closed. `secret has no canonical
record` means the daemon has not received the secret's record yet (cold
start) or the secret was deleted.

Run logs are withheld, never returned unredacted, with `logs withheld
because they cannot be safely redacted` when a secret the run injected is
legacy, missing, unresolvable right now, or was re-pointed since the run
injected it (its canonical record revision changed), and for direct runs
from before the injected secrets were recorded. `run log redaction is rate
limited for this requester` clears within a minute; logs of a run read
recently are served from a short-lived cache.

Adoption refuses a container naming its secret environment keys; if a key
is not a secret (`PASSWORD_MIN_LENGTH`), list it with `bahia adopt import
--non-secret-env PASSWORD_MIN_LENGTH`.

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

A single resolve (reveal, a build's lone credential) publishes on the
outbound `priority` lane, shared with operator results and ContextVM
replies, and fails fast when the lane is exhausted. An operation that
resolves several secrets (a deploy, VM bootstrap, run-log redaction, a
build's credential pair) declares one outbound operation and is paced on
the `bulk` lane instead of refused; it waits behind any other active bulk
operation for up to `secret_store.signet.request_timeout`
([outbound admission](nostr-outbound-admission.md)). A resolve costs one
gift wrap once the daemon has inspected the credential and seen Signet
report a delivered version (Signet 0.7.0+), otherwise two or three.

Signet's audit chain records every delivery with the requester pubkey:
`signetctl verify-audit`.

## Live proof

`scripts/signet_secret_store_live.py --signet-repo <nostrc checkout> --build-dir <cmake build> --signet-commit <sha>`
builds `signetd`/`signetctl` from a clean nostrc commit, runs them on a
private loopback relay with a disposable store, sets up an owner, a
requester holding only the delivery grant and a credential exactly as
above, and runs Bahia's opt-in `TestLiveSignetCredentialStore` (`-tags
signetinterop`): resolve the pinned version, then resolve in one round trip,
refuse another tenant's credential, refuse a stale version after rotation,
resolve the new version alone and as a paced batch, fail closed after
revocation and without a delivery grant. It waits up to 90 s for the
loopback relay (`nak serve`) and for signetd to answer.
