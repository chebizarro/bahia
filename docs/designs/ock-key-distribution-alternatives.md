# OCK key distribution: alternatives to "wrap the key to each member"

Status: research note (2026-10-06). Follows `org-membership-group-standards.md` (which chose OCK over NIP-29 / Communikeys / Concord) and NIP-CAS-0011. Question: is NIP-44-wrapping a symmetric key to every member the best we can do, or is there a more sophisticated, fit-for-purpose scheme (threshold crypto, delegation, group key agreement)?

## What OCK is, in the literature's terms

OCK is a **sender-keys** scheme: one symmetric content key per group and epoch, distributed by one authority (the daemon) through pairwise encrypted channels (NIP-44 to each member), rotated by republishing. It is the same shape as Signal's Sender Keys, Matrix Megolm (minus the hash ratchet), Concord's `community_root`, and Keybase team keys. Properties:

| Property | OCK today |
|---|---|
| Independent read by every member, offline, with only `nip44.decrypt` (NIP-07/NIP-46) | yes |
| Works with bunker signers (no raw key in the browser) | yes |
| Add member | wrap current key, O(1) |
| Remove member | rotate: new key wrapped to N remaining members, O(N) |
| Forward secrecy (a key compromised later does not expose earlier records) | no — the OCK for an epoch decrypts everything in that epoch, forever |
| Post-compromise security (a compromised member who is *not* removed is healed) | no |
| Trust in the distributor | total — the daemon mints and sees every key (it also authors every record, so this is not an added trust) |
| Daemon restart | recovers its own wrap from history |
| Discovery cost | trial-decrypt O(N) per epoch |

The two gaps a "more sophisticated" scheme could close are **forward secrecy / post-compromise security** and **O(N) rotation**; the two things that must not be lost are **independent offline reads with a bunker signer** and **no per-device persisted key material** (Phase 4 C1-R5).

## Candidates

### 1. FROSTR (FROST threshold Schnorr over Nostr: bifrost, igloo, cinderella)

What it does: splits one Nostr private key into k-of-n shares; signing and **ECDH** (`node.req.ecdh(pubkey)`) are computed collaboratively, the full key is never reconstructed. NIP-44 to/from the threshold key therefore requires k share-holders online for every decrypt (cinderella notes exactly this: ECDH has no per-event policy, and a hot share plus one online node decrypts every DM). TypeScript primary (`@frostr/bifrost`), a Rust port exists; OwnAuth already runs a dealerless FROST DKG for org signing keys.

Fit for OCK: **wrong tool for member reads.** OCK's purpose is that each member reads independently, offline, from a browser; threshold ECDH makes every read an online k-of-n ceremony and gives all members the *same* read capability (there is no "this member can, that one cannot" — removal means re-dealing shares). It is the **right tool for a different problem**: custody of the key that *signs* every canonical record (the Bahia service key) and of org owner identities. A single hot service key today signs all 30900 state; a 2-of-3 FROSTR key for the service identity (daemon share + operator share + offline share) would remove the single point of compromise. Recommend as a separate hardening track, not as an OCK replacement.

### 2. Delegation NIPs

- **NIP-26** (delegated event signing): `unrecommended` upstream ("adds unnecessary burden for little gain"), covers *signing authority*, not decryption; nothing in the NIP corpus delegates read capability. Dead end.
- **NIP-46 as a key server**: a bunker that holds the OCK and performs `nip44_decrypt` for authorized members. This recentralizes reads onto an online service, defeats offline-first, and is strictly weaker than today (the bunker becomes a decryption oracle). No.
- **Proxy re-encryption** (Umbral-style on secp256k1): lets an untrusted proxy transform a ciphertext for the authority into one for a member without seeing plaintext. The benefit (untrusted proxy) is moot here — the daemon authors the plaintext. It would buy nothing over wrapping. No.

### 3. MLS via Marmot (NIP-EE lineage)

Status moved since the earlier note: the Marmot protocol repo is marked **adopted** (MIP-era docs deprecated), with a Rust reference stack (MDK), `marmot-ts`, interoperable clients (White Noise, Pika, Vector, Amethyst PR), and several **pure-Go RFC 9420 implementations** (`thomas-vilte/mls-go`, `Deln0r/mls-go`, `tmc/mls`; interop-tested against OpenMLS/mlspp). Local copy of the spec: `~/Documents/Dev/marmot` (foundation/, protocol-core/, transports/nostr.md).

What it gives, mapped to OCK: the org becomes an MLS group; the daemon is the committer; members join from published key packages (kind 443 in Marmot's Nostr transport); the **MLS exporter secret of each epoch is the OCK**. Records keep the exact same `bahia.confidential.aead.v1` envelope and coordinates — only key *distribution* changes.

| | OCK | MLS-backed OCK |
|---|---|---|
| Add/remove | O(1) / O(N) wraps | O(log N) commit, standard |
| Forward secrecy + PCS | none | yes (TreeKEM path secrets, epoch keys) |
| Rotation | manual/triggered republish | every commit is an epoch; "refounding" (bahia-irsry.81) is just re-encrypt-under-new-exporter |
| Bunker signers | native | Nostr key signs the key package only; HPKE keys are MLS-local — works with sign-only NIP-46 |
| Offline read of current epoch | needs the wrap | needs the **persisted MLS group state** (ratchet tree + secrets) |
| Historical records | retain old OCK versions in memory | retain old exporter secrets per epoch (same shape) |
| Multi-device | one wrap per pubkey | one leaf per device (Marmot `features/multi-device.md`) |
| Concurrent membership changes | daemon serializes | Marmot `convergence.md` fork/tie-break rules handle relay reordering |

Costs: (a) **C1-R5 conflict** — MLS group state is key material and must persist per device; the browser would hold it in IndexedDB (encrypted at rest under a signer-derived key via NIP-44 self-encryption is possible with NIP-07/NIP-46, but it is still a persisted secret, which C1-R5 forbids today); (b) each member/device must publish a key package and keep it fresh; (c) daemon needs a Go MLS stack (available, but young — none is a year old and none is in Bahia's dependency graph); (d) browser stack: `marmot-ts`/`ts-mls` (TypeScript, active); (e) protocol surface is large (commits, welcomes, proposals, convergence) for what is, today, "N wraps per rotation" with N < 100.

Verdict: **the only candidate that is genuinely more sophisticated *and* fit for purpose** — but as a v2, gated on relaxing C1-R5 to "encrypted at rest, never in plaintext storage" and on a Go MLS library proving out in a spike. Not worth adopting at <100 members with manual rotation working.

### 4. BeeKEM / Keyhive (Ink & Switch)

A CGKA for the server-less local-first setting: no central ordering, concurrent adds/removes merge under causal order, FS + PCS, DH + BLAKE3, logarithmic. Conceptually the best match for "a Nostr relay is not a sequencer" — but it is a Rust research crate (`beekem`, WASM demos), no Go, no Nostr integration, no interop ecosystem. Watch; do not build on.

### 5. Reader-set (OwnAuth's pattern) and Concord streams

Covered in the previous note: reader-set = N ciphertexts per record (no better than wraps); Concord's shared stream key = OCK with a different envelope and no coordinates. Neither closes the FS/PCS gap.

## Recommendation

1. **Keep OCK as the v1 mechanism.** Nothing cheaper closes its gaps, and its gaps (no FS/PCS, O(N) rotation) are acceptable at fleet scale where the distributor already authors the plaintext.
2. **Adopt FROSTR for the service signing key**, not for OCK: a k-of-n service identity (daemon share, operator share, cold share) removes the single hot key that signs all canonical state. OwnAuth's FROST DKG and bifrost are reusable. File as a hardening issue.
3. **Plan "MLS-backed OCK" as v2 with a spike**: daemon-side Go MLS group per org, exporter secret as the OCK, Marmot Nostr transport for key packages/welcomes, same record envelope. Prerequisites to decide first: (a) relax C1-R5 to encrypted-at-rest device state; (b) choose a Go MLS library and run it against the MLS interop harness (`~/Documents/Dev/mls-implementations`); (c) confirm `marmot-ts` works with sign-only NIP-46. Trigger for doing it: orgs > a few hundred members, or a requirement for forward secrecy on confidential state.
4. Keep NIP-26, NIP-46-as-KMS, PRE and BeeKEM out of scope.

## Sources
- FROSTR: https://frostr.org/ , https://github.com/frostr-org/bifrost , https://github.com/hasky00/cinderella (ECDH/DM gap: https://github.com/hasky00/cinderella/pull/2)
- Marmot: https://github.com/marmot-protocol/marmot , https://nostrcompass.org/en/topics/marmot/ , Amethyst Marmot PR https://github.com/vitorpamplona/amethyst/pull/4073
- Go MLS: https://github.com/thomas-vilte/mls-go , https://pkg.go.dev/github.com/Deln0r/mls-go , https://github.com/tmc/mls
- Keyhive/BeeKEM: https://www.inkandswitch.com/keyhive/notebook/02/ , https://docs.rs/beekem/latest/beekem/
- NIP-26 status: nips repo `26.md` (`unrecommended`)
