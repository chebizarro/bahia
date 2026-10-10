# Confidential control-plane state (OCK)

Org membership, invites, secret metadata, notification channel configuration,
payment records, security state, the Soul Factory adapter ledger and the
operator allowlists are **confidential addressable state**: kind `30900`
cp-state records like every other family, but with encrypted content. Relays
are dumb storage and never see plaintext; NIP-42 read auth on the sidecar
(`read_auth_mode`, default `enforce`) is defence in depth, not the
confidentiality mechanism. The scheme is published as Cascadia
**NIP-CAS-0011 — Org Content Keys**; this page is Bahia's implementation of it.

Implementation: `internal/controlplane/org_content_key.go` (AEAD, associated
data), `org_content_key_manager.go` (lifecycle), `confidential_encryptor.go`
(publisher bridge), `ock_member_source.go` (roster), the `*_canonical_publisher.go`
files in `internal/adapters/nostr`, and `web/src/lib/nostr/confidential.js` plus
`stores/auth-roles.svelte.js` in the browser.

## Org content key

- One random 32-byte symmetric key per **key scope** and version. Scopes are
  org UUIDs, plus the well-known `fleet` scope (`kinds.FleetOCKScope`) for
  resources with no org: payments, security findings/schedules/targets/runs,
  fleet-scoped notification channels, the Soul Factory ledger and the operator
  allowlists.
- Content is encrypted with XChaCha20-Poly1305. The AEAD associated data binds
  the ciphertext to the record's coordinate identity
  `{schema, key_org, key_ref, key_version, legacy_kind, d, t}`, so a ciphertext
  cannot be replayed onto another coordinate.
- All NIP-44 operations go through the `Keyer` signer interface, so bunker
  signers (NIP-46) that offer `nip44_encrypt/decrypt` work without raw key
  material.

Envelope (`bahia.confidential.aead.v1`):

```json
{
  "schema": "bahia.confidential.aead.v1",
  "algorithm": "xchacha20-poly1305",
  "key_org": "<org-id|fleet>",
  "key_ref": "ock:<org-id|fleet>",
  "key_version": "v<n>",
  "nonce": "<base64, 24 bytes>",
  "ciphertext": "<base64>",
  "associated_data": {"schema": "...", "key_org": "...", "key_ref": "...", "key_version": "...", "legacy_kind": "<family>", "d": "<d>", "t": "<topic>"},
  "service_inner": "<nip44 ciphertext to the service pubkey, optional>"
}
```

`service_inner` carries the fields members must never read — secret values and
channel credentials (webhook URLs, signing secrets, API keys) — NIP-44-encrypted
to the service key. Members decrypt the AEAD layer (metadata) only; secret
value reveal remains a ContextVM request (`services/secrets-reveal`) with
permission checks.

## Key distribution: key envelopes (family 32010)

The OCK is NIP-44-wrapped to **each current member of the scope and to the
service pubkey**, and every wrapped copy is published as an addressable
cp-state record through the normal `cpStateFamilies` → `controlStateEnvelope`
pipeline with `legacy_kind=32010`, `t=org-key-envelope` and
`d = org-key:<scope>:v<version>:<random-16-byte-hex-handle>`. The recipient
never appears in tags, `d` or content; members find their envelope by
trial-decrypting all envelopes for their scope and version (O(N) at N < 100
members). The daemon recovers its own key after a restart from the
service-wrapped envelope in history. The fleet-scope OCK is wrapped to
`nostr.authorized_pubkeys`, `bootstrap_owners` and the service pubkey
(`TrustSetMemberSource`).

The roster source (`OCKMemberSource`) is pluggable and independent of key
distribution; today it is the trust set (relay membership → Postgres →
config).

## Lifecycle

All membership-driven key operations run through
`OrgCanonicalPublisher.PublishMember`, the single choke point for member
mutations, and the rekey **precedes** the canonical member record so a failed
rekey cannot commit a revocation:

| Event | Action |
|---|---|
| Member added or updated | `WrapForRecipient` — wrap the current key to the new member (rotating on add has no security value) |
| Member removed | `RotateKeyExcluding` — new version wrapped to the remaining members only |
| Role downgrade | `RotateKey` — new version (audit boundary; the member still receives it) |
| First confidential record of a scope | `EnsureKey` creates and distributes v1 |

Old versions stay readable for records published under them; new publishes use
the current version. A rotation whose envelope publish is still pending is
tracked (`PendingRotations`) and surfaced by the `ock_rotation` readiness check
(`warn`).

### Refounding

Rotation alone leaves older records readable to a removed member who kept the
old key. **Refounding** re-publishes every confidential record of the scope
under the newest version: the `org` intent `op=rekey` (`bahia.intent.org.v1`,
content `{"org_id": "<org-uuid>|fleet", "reason"?: "..."}`), authorized for
owners/admins of the org or, for the `fleet` scope, fleet operators. The status
event reports `key_version` and `records_republished`. Organizations with
`strict_revocation: true` (a field of the org record) refound automatically
after every removal or downgrade; otherwise refounding is on demand. Clients
drop a superseded key version once no cached record references it.

## What readers do

The browser (`auth-roles.svelte.js`) and the CLI/MCP confidential readers
derive their access from the same store they render from:

1. Query key-envelope records (`30900`, `t=org-key-envelope`) and trial-decrypt
   them with the signer's NIP-44 to recover the OCKs the user holds.
2. Decrypt confidential records (`isConfidentialEnvelope`) with the matching
   `key_org`/`key_version`.
3. Derive the signed-in user's **role per org** from the decrypted org-member
   records — roles gate mutation affordances, not rendering.

Rules the clients keep: the content key lives **in memory only** (never
IndexedDB or localStorage); a signer without NIP-44 degrades to relay-public
state and no sensitive mutations; a user with no membership sees relay-public
state only.

## Operator allowlists

The daemon publishes its operator lists as confidential records of family
`32029` (`CPStateFamilyOperatorAllowlist`) on `d = operators:<scope>`:
`operators:continuity` mirrors `nostr.authorized_pubkeys` (signers of
continuity definitions `31400-31404`, failover/recovery commands
`38430/38431` and continuity heartbeats `30315`) and `operators:soul-factory`
mirrors `soul_factory.authorized_pubkeys` (SoulFactory drafts `31952`, actions
`1950`, fleet configuration `31953`). The pubkeys are only in the fleet-OCK
encrypted content, so a browser holding the fleet key can trust other
operators' documents without the relay learning who the operators are.

## Why this scheme

OCK is a sender-keys scheme (one symmetric key per group and epoch, distributed
over pairwise NIP-44, rotated by republishing). It was chosen over the
alternatives because each must keep two properties — **independent offline
reads with only a bunker signer's `nip44.decrypt`** and **no persisted key
material per device** — and keep the `30900` coordinates and store-first reads:

- NIP-29 and Communikeys (NIP-CAS-0007) do not encrypt; the relay is the
  authority. Bahia uses them only where relay-enforced scoping is the point
  (SoulFactory groups and write grants).
- Concord (NIP-CAS-0008) has the same key model but folds record identity into
  wraps and has no owner succession; it stays an optional coordination-chat
  plane.
- MLS (Marmot/NIP-EE) adds forward secrecy and O(log N) rotation but requires
  persisted per-device group state. It is the designated successor if orgs grow
  to hundreds of members or forward secrecy on confidential state becomes a
  requirement; the record envelope would not change, only key distribution.
- Threshold Schnorr (FROSTR) is the wrong tool for member reads (every read
  becomes an online ceremony); it is the right tool for custody of the service
  signing key, tracked separately.

## Legacy record re-encryption

After relay warm-start, the daemon scans the retained, service-authored
org, member, invite, secret and notification-channel cp-state families and
re-publishes records still using O1 or N1 encryption under the OCK envelope.
The checked migration result accounts for each local record and fails if a
query is truncated, a record cannot be decrypted or re-published, or the
service identity is unavailable. Failed or canceled checked attempts can be
retried in the same daemon without re-publishing records already accepted by
the projector. The startup hook logs an incomplete result as an error rather
than declaring success; it does not itself schedule a retry.

A successful checked result covers only the bounded local retained history.
It does not prove older relay or backup inventory is empty, and an outbox-queued
publish may not yet have reached relay quorum. It is not a key-erasure or
remote-signer cutover authorization.
