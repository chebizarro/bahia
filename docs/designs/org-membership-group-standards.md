# Org membership: group-standard evaluation for bahia-irsry.71

Status: proposal (2026-10-04). Resolves the open design question in `.71` (Phase 4 §5.4 C1-R3 / C1-R4).

## The requirement being served

Bahia's org domain is **confidential addressable control-plane state**, not chat:

- Records: organization, member (role ∈ viewer/deployer/admin/owner), invite, secret *metadata*, notification channel config — all kind-30900 cp-state with a `d` coordinate, `domain`/`schema`/`t` tags, replaceable semantics, projected by the daemon, warm-started and deduped from local history, read store-first by web/CLI/MCP.
- Readers: current org members through a browser signer (NIP-07 or NIP-46 bunker — only `nip44.encrypt/decrypt` is available, no raw key), the daemon, fleet operators for fleet-scoped resources.
- Writers: the daemon only (intents are gift-wrapped to the service).
- Revocation: a removed/downgraded member must not read records published after removal.
- Scale: < 100 members per org; thousands of records.
- Must not require trusting the relay for confidentiality (relays are dumb storage; NIP-42 read-auth is defence in depth, default `warn`).

Today this is met by the **OCK scheme** (phase3 §1.7.1): one random 32-byte key per org and version, XChaCha20-Poly1305 with AD bound to the record coordinate, the key NIP-44-wrapped to each member + the service as kind-32010 envelopes (`org-key:<org>:v<n>:<handle>`), rotation on removal/downgrade, wrap-only on add. Implemented: `internal/controlplane/confidential_encryptor.go`, `org_content_key_manager.go`, `ock_member_source.go`; web `lib/nostr/confidential.js`.

## Candidates

### NIP-29 (relay-based groups)
Relay enforces membership on `h`-tagged events; relay signs `39000/39001/39002` metadata/admin/member lists; moderation via `9000–9020`; `private` = relay refuses reads to non-members (NIP-42). **No encryption anywhere.** The relay is the authority and sees everything; forks/migrations are a feature. Designed for chat/moderation; its state model (relay-generated lists) does not map onto daemon-published 30900 coordinates.

*Fit:* fails the "no relay trust for confidentiality" requirement outright; duplicates what Bahia's sidecar read-auth (B5) already provides at the topic level. Bahia already uses NIP-29 where relay-enforced scoping is the desired property (SoulFactory agent groups). **Not suitable for .71.**

### Communikeys V2 (NIP-CAS-0007 / flotilla `Communikeys.md`)
Public community plane: kind-32222 owner-signed definition, kind-30000 NIP-51 grant lists (`p` tags = membership, union of shards), kind-30222 targeted publications. The spec is explicit that the members-only read hint "does **not** enable relay enforcement, prove membership, or provide encryption". OwnAuth uses exactly this (plus plain NIP-51 `group.members` lists) for its *public* group/ACL plane, and Bahia uses it for SoulFactory write grants.

*Fit:* membership is plaintext by design; it is a roster/ACL standard, not a confidentiality standard. **Not suitable as the mechanism for .71**, but see "roster source" below.

### Concord (CORD-01…08, adopted as NIP-CAS-0008)
E2EE communities: a stream is a shared secp256k1 keypair; kind-1059 wraps signed by the stream key; `community_root` possession *is* membership; per-channel keys; epochs; removal = Rekey/Refounding (CORD-06); invites as NIP-44 bundles / NIP-59 direct invites. Semantically the closest to Bahia's need — and notably its key model (shared root key, epoch rotation, re-encrypt after removal) is the same shape as OCK.

*Costs:* (1) everything, including record identity, lives inside wraps — addressable `d`/`t` coordinates, replaceable semantics, topic read-auth, projector dedupe, warm-start and store-first queries would all have to be rebuilt on Concord's Control-Plane folding; (2) owner key is unrecoverable, no succession (CORD-02 §1) — unacceptable for a fleet control plane; (3) CORD-04/06 are not fleet-core; (4) NIP-CAS-0008 rules 5–6 already say Concord is a conversation plane, never a control-plane transport, and its authority never composes with Cascadia authorization. **Not suitable for cp-state.** Keep as the optional private *coordination chat* plane it already is.

### NIP-EE / MLS (considered because OwnAuth vendors the draft)
Forward secrecy and post-compromise security for groups. OwnAuth documents it but does not use it in code. MLS needs persisted per-device group state (conflicts with C1-R5 "content key never persisted"), an MLS stack in browser and daemon, and FS actively works against "a new device must read all current records". Over-engineered for <100-member control-plane state. **Not now.**

### OwnAuth's actual pattern (for comparison)
Public roster = NIP-51 `group.members` list + Communikeys V2 branch; confidential payloads = NIP-70 protected events whose content is **the payload NIP-44-encrypted once per reader** (`libs/nostr/protected/encrypt.go`, `EncryptJSON(readers…)`); writers = FROSTR org key. This is the "reader-set" model: N ciphertexts per record, re-publish every record on any roster change. OCK is the "sender-key" model: 1 ciphertext per record, N envelopes per key version. For thousands of records and a changing roster, OCK scales better; for a handful of secrets with a stable reader set, OwnAuth's is simpler. Both have identical post-removal exposure of records that existed while the member was a reader.

## Comparison

| | NIP-29 | Communikeys | Concord | MLS | OwnAuth reader-set | OCK (ours) |
|---|---|---|---|---|---|---|
| Confidential from relay | no | no | yes | yes | yes | yes |
| Membership hidden from relay | no | no | yes | yes | partly | yes (handles random, no `p` tags) |
| Works with NIP-46 bunker | yes | yes | yes* | no | yes | yes |
| Keeps 30900 coordinates / store-first | n/a | yes | **no** | no | yes | yes |
| Daemon restart recovery | n/a | n/a | from Community List | persisted state | n/a | service-wrapped envelope |
| Removal revokes future reads | relay | no | rekey | yes | re-publish all | rotate |
| Owner succession | relay | owner key | **none** | n/a | FROSTR | daemon service key |
| Already implemented in Bahia | yes (SF) | yes (SF) | yes (SF, invites) | no | no | **yes, all surfaces** |

\* Concord needs raw key material for stream signing; bunker-only web clients would need a delegated key.

## Recommendation

1. **Keep OCK as Bahia's mechanism and promote it to a Cascadia draft (`NIP-CAS-0011: Org Content Keys — confidential addressable state for closed groups`)** rather than leaving it an internal convention. Nothing in the three named standards solves .71 better: two don't encrypt, and Concord's answer is the same key model with an incompatible envelope. Writing the NIP costs a day and gives OwnAuth/SoulFactory a shared vocabulary. Align terms with Concord where they coincide (`key_version` ≙ epoch; rotation ≙ rekey; re-encryption ≙ refounding).
2. **C1-R3 — close as satisfied.** The Phase 4 text assumed per-member gift-wrapped copies; the shared-key design makes every membership record readable by every current member with one ciphertext. Per-member copies only buy roster hiding *between members*, which is not a Bahia requirement (the UI shows the roster) and which none of OwnAuth, Communikeys or NIP-29 provide either.
3. **C1-R4 — adopt Concord's "refounding" as an explicit, optional operation.** Current behaviour (rotate on removal; old versions stay readable for old records) is the standard trade-off and equals Concord's semantics until a Refounding lands. Add `org/rekey` (intent, owner/admin only): rotate, then re-publish every confidential record of the org under the new version in batches; web/CLI drop superseded versions once no record references them (track versions seen per org). Run it automatically after a removal if the org has `strict_revocation: true`; otherwise on demand. This reuses the republish machinery `.65` is already waiting to soak. Wrap-only-on-add stays (rotating on add has no security value).
4. **Roster source is orthogonal — keep it pluggable.** `OCKMemberSource` already abstracts "who are the members". When OwnAuth becomes the IdP, its NIP-51 `group.members` list (or Communikeys grant list) can be the roster *authority* while OCK remains the key distribution. Do not couple the two.
5. Leave NIP-29 and Concord where they are (SoulFactory relay-scoped groups; optional private coordination chat).

## Follow-ups if accepted
- `.71` → close R3; file `org/rekey` refounding slice (daemon + web) — one Codex slice each, after `.65`'s soak so the re-publish path is proven.
- cascadia-nips: draft NIP-CAS-0011 from phase3 §1.7.1 (kind 32010, `org-key-envelope`, AD binding, handle scheme, fleet scope).
- Phase 4 doc §5.4: replace R3/R4 text with the above.
