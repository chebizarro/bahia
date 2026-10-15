# OCK key distribution: alternatives to "wrap the key to each member"

Status: research note updated by the isolated MLS spike (2026-10-13). Follows `org-membership-group-standards.md` (which chose OCK over NIP-29 / Communikeys / Concord) and NIP-CAS-0011. Question: is NIP-44-wrapping a symmetric key to every member the best we can do, or is there a more sophisticated, fit-for-purpose scheme (threshold crypto, delegation, group key agreement)?

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

Status moved since the earlier note: the Marmot protocol repo is marked **adopted** (MIP-era docs deprecated), with a Rust reference stack (MDK), `marmot-ts`, interoperable clients (White Noise, Pika, Vector, Amethyst PR), and several **pure-Go RFC 9420 implementations** (`thomas-vilte/mls-go`, `Deln0r/mls-go`, `tmc/mls`). Their coverage and interoperability differ; the prerequisite table below evaluates them separately. Local copy of the spec: `~/Documents/Dev/marmot` (foundation/, protocol-core/, transports/nostr.md).

What it gives, mapped to OCK: the org becomes an MLS group; the daemon is the committer; members join from published key packages (current Marmot kind 30443; the contained experiment uses the requested legacy kind 443); the **MLS exporter secret of each epoch is the OCK**. Records keep the exact same `bahia.confidential.aead.v1` envelope and coordinates — only key *distribution* changes.

| | OCK | MLS-backed OCK |
|---|---|---|
| Add/remove | O(1) / O(N) wraps | O(log N) commit, standard |
| Forward secrecy + PCS | none | yes (TreeKEM path secrets, epoch keys) |
| Rotation | manual/triggered republish | every commit is an epoch; "refounding" (bahia-irsry.81) is just re-encrypt-under-new-exporter |
| Bunker signers | native | MLS HPKE keys are local, but current marmot-ts requires NIP-44 as well as event signing for Welcome gift wraps — sign-only NIP-46 is not established |
| Offline read of current epoch | needs the wrap | needs the **persisted MLS group state** (ratchet tree + secrets) |
| Historical records | retain old OCK versions in memory | retain old exporter secrets per epoch (same shape) |
| Multi-device | one wrap per pubkey | one leaf per device (Marmot `features/multi-device.md`) |
| Concurrent membership changes | daemon serializes | Marmot `convergence.md` fork/tie-break rules handle relay reordering |

Costs: (a) **C1-R5 conflict** — MLS group state is key material and must persist per device; the browser would hold it in IndexedDB (encrypted at rest under a signer-derived key via NIP-44 self-encryption is possible with NIP-07/NIP-46, but it is still a persisted secret, which C1-R5 forbids today); (b) each member/device must publish a key package and keep it fresh; (c) daemon needs a Go MLS stack (the spike adds one only as an isolated dependency; it remains unaudited); (d) browser stack: `marmot-ts`/`ts-mls` (TypeScript, active); (e) protocol surface is large (commits, welcomes, proposals, convergence) for what is, today, "N wraps per rotation" with N < 100.

Verdict: **the only candidate that is genuinely more sophisticated *and* fit for purpose** — but as a v2, gated on relaxing C1-R5 to "encrypted at rest, never in plaintext storage" and on a Go MLS library proving out in a spike. Not worth adopting at <100 members with manual rotation working.

### 4. BeeKEM / Keyhive (Ink & Switch)

A CGKA for the server-less local-first setting: no central ordering, concurrent adds/removes merge under causal order, FS + PCS, DH + BLAKE3, logarithmic. Conceptually the best match for "a Nostr relay is not a sequencer" — but it is a Rust research crate (`beekem`, WASM demos), no Go, no Nostr integration, no interop ecosystem. Watch; do not build on.

### 5. Reader-set (OwnAuth's pattern) and Concord streams

Covered in the previous note: reader-set = N ciphertexts per record (no better than wraps); Concord's shared stream key = OCK with a different envelope and no coordinates. Neither closes the FS/PCS gap.

## Recommendation

1. **Keep OCK as the v1 mechanism.** Nothing cheaper closes its gaps, and its gaps (no FS/PCS, O(N) rotation) are acceptable at fleet scale where the distributor already authors the plaintext.
2. **Adopt FROSTR for the service signing key**, not for OCK: a k-of-n service identity (daemon share, operator share, cold share) removes the single hot key that signs all canonical state. OwnAuth's FROST DKG and bifrost are reusable. File as a hardening issue.
3. **Consider "MLS-backed OCK" as v2, not an approved migration**: daemon-side Go MLS group per org, exporter secret as the OCK, Marmot Nostr transport for key packages/welcomes, same record envelope. Prerequisites to decide first: (a) decide whether C1-R5 can permit encrypted-at-rest device state; (b) complete cross-client interop and security review of the Go library; (c) resolve marmot-ts Welcome gift-wrap NIP-44 requirements and the kind-443 versus kind-30443 transport mismatch. Trigger for revisiting adoption: orgs > a few hundred members, or a requirement for forward secrecy on confidential state.
4. Keep NIP-26, NIP-46-as-KMS, PRE and BeeKEM out of scope.

## Spike decision (2026-10-13, bahia-dt4w7)

**Decision: retain the existing OCK distributor.** The contained code at `spikes/mls-ock/` proves a pure-Go MLS exporter can supply the existing `bahia.confidential.aead.v1` record format. It does not prove Marmot interoperability, durable group-state recovery, browser safety, canonical relay publication, or a production migration. No daemon path imports the spike.

### Prerequisite evaluation

| Candidate | RFC 9420 surface and API fit | Maintenance/toolchain/license | Result |
|---|---|---|---|
| [`thomas-vilte/mls-go` v1.6.0](https://github.com/thomas-vilte/mls-go/tree/v1.6.0) | High-level `Client` exposes KeyPackage, Add/Remove commits, Welcome join, and `Export`; suite 0x0001 works in the spike. Its bundled working-group vector tests cover key schedule, Welcome, TreeKEM, framing, and other surfaces. | Tagged 2026-07-10; Go 1.26; MIT. [Security policy](https://github.com/thomas-vilte/mls-go/blob/v1.6.0/SECURITY.md) lists unaudited/remaining validation and best-effort in-memory erasure. | **Spike recommendation**, not production approval. |
| [`Deln0r/mls-go`](https://github.com/Deln0r/mls-go#roadmap) | Add/Commit/Welcome exist, but its roadmap still lists full transcript/framing, application `PrivateMessage`, and test-vector conformance. No demonstrated exporter API for this experiment. | Go 1.26.3, MIT; small/early project. | Not adequate for exporter-backed OCK yet. |
| [`tmc/mls`](https://github.com/tmc/mls#state) | Broad claimed RFC 9420 coverage (all seven suites, proposals, commits, Welcome, exporter) and working-group vectors. | MIT with BSD-licensed portions; requires Go 1.27 for `crypto/hpke`, while Bahia pins Go 1.26.3. | Strong future candidate; incompatible with this branch's toolchain. |

Interop evidence: all 14 JSON vectors bundled by v1.6.0 compare byte-for-byte with the corresponding files in the local read-only `~/Documents/Dev/mls-implementations/test-vectors/` corpus; `go test ./group ./ciphersuite ./keypackages ./messages ./treesync -count=1` passes inside that module. The local gRPC harness has no built client/protobuf adapter in this checkout, so **cross-implementation interop was not run**. The upstream library's published mlspp/OpenMLS claims are not local proof.

Marmot compatibility: the current [marmot-ts getting-started guide](https://github.com/marmot-protocol/marmot-ts/blob/master/docs/getting-started.md#initialize-the-client) explicitly requires signer `nip44` encrypt/decrypt as well as `signEvent`, because it gift-wraps Welcomes. NIP-46 `sign_event` alone therefore does **not** satisfy that documented client contract; no browser/NIP-46 integration was tested. The guide's [key-package section](https://github.com/marmot-protocol/marmot-ts/blob/master/docs/getting-started.md#publish-a-key-package) uses kind **30443**, not the requested legacy **443**. The spike deliberately publishes kind 443 on a local relay to satisfy this experiment's input and does not claim to be a Marmot transport implementation. A production protocol decision must settle the kind, signer capabilities, credential-to-Nostr binding, Welcome wrapping, and relay semantics first.

Phase 4 C1-R5 remains a **product/security decision**, not a spike decision. MLS state contains epoch and private leaf secrets; persisting it plaintext in browser storage violates C1-R5. Encrypting IndexedDB state under a device key or NIP-44 self-wrap would preserve offline reads after unlock but still persists recoverable key material, adds signer availability/unlock and rollback/replay/recovery risks, and changes the current no-device-secret guarantee. Keeping it only in memory preserves C1-R5 but loses offline/restart reads and forces rejoin. Neither policy change is adopted here.

### Local measurements and relay projection

Run `go run ./spikes/mls-ock` (Go 1.26.3, macOS arm64; one daemon, one member, suite 0x0001, local in-process khatru relay). Bytes below are from one successful run; randomized signatures/keys can alter a serialized size. The program verifies relay `OK`, signed key-package integrity and EOSE before inviting, matching 32-byte exporter values at epoch 1, round-tripping one existing confidential AEAD envelope, then an epoch-2 remove commit and cryptographic failure of the old exporter on a new record (with the version metadata held equal).

| Measured item | Bytes/events |
|---|---:|
| Raw MLS KeyPackage | 292 B |
| Signed kind-443 key-package Nostr event | 736 B |
| MLS Welcome | 768 B |
| MLS add commit | 466 B |
| MLS remove commit | 351 B |
| Projected events per join / leave | 3 / 1 |

The event counts assume one newly published KeyPackage + one commit + one Welcome per join, and one remove commit per leave. Only the KeyPackage event was actually sent through the local relay; commits and Welcome were handed directly between the simulated clients. Existing prepublished KeyPackages reduce per-invite publication to two events. Real Marmot Welcome gift wraps, relay replication, receipts, and resends are **not measured**.

For a deliberately explicit **illustrative projection**, let `d = ceil(log2 N)`, scale the measured 2-member commit payloads by `d`, and scale the measured Welcome payload by `N/2` as a full-tree serialization approximation. Assume 344 B of signed-event JSON overhead (measured from the key-package event) plus base64 length `4*ceil(payload/3)` for each MLS payload. These are **not** protocol bounds or measured 10/100/1000-member payloads; actual Welcome/tree encoding and gift-wrap overhead require a large-group run. The 1,000-member illustrative Welcome event exceeds common relay event-size budgets, so relay NIP-11 limits and an admissible delivery shape are an explicit unresolved feasibility check.

| Org members | Join events | Projected join traffic / relay | Leave events | Projected leave traffic / relay |
|---:|---:|---:|---:|---:|
| 10 | 3 | 9,032 B | 1 | 2,216 B |
| 100 | 3 | 56,976 B | 1 | 3,620 B |
| 1,000 | 3 | 519,640 B | 1 | 5,024 B |

Production recommendation remains **no-go pending** cross-client Marmot interop, signer/transport decisions, encrypted browser-state policy, persistent state/recovery design, and security review. The measured experiment establishes only the cryptographic lifecycle with the existing record envelope and an isolated local relay.

## Sources
- FROSTR: https://frostr.org/ , https://github.com/frostr-org/bifrost , https://github.com/hasky00/cinderella (ECDH/DM gap: https://github.com/hasky00/cinderella/pull/2)
- Marmot: https://github.com/marmot-protocol/marmot , https://nostrcompass.org/en/topics/marmot/ , Amethyst Marmot PR https://github.com/vitorpamplona/amethyst/pull/4073
- Go MLS: https://github.com/thomas-vilte/mls-go , https://pkg.go.dev/github.com/Deln0r/mls-go , https://github.com/tmc/mls
- Keyhive/BeeKEM: https://www.inkandswitch.com/keyhive/notebook/02/ , https://docs.rs/beekem/latest/beekem/
- NIP-26 status: nips repo `26.md` (`unrecommended`)

## FROSTR service-custody stage 1 decision (bahia-4m4b8)

**Decision: a contained 2-of-3 signer experiment is viable; production key
rotation is not approved.** The group x-only public key is the service npub.
`internal/servicesigner` accepts opt-in `method: frostr` only with a pinned
`nostr.public_key`, an owner-only mounted daemon share and transport key, two
public peer shares/identities, a round coordinator and a single-writer fence.
The daemon assembly supplies neither a relay coordinator nor a durable fence,
so selecting the method in a deployed config currently fails closed. The
existing `local`, `nip46`, and `nip55l` defaults are unchanged.

### Library evaluation

| Candidate | Standard BIP-340 under group x-only key? | Key generation / ECDH | Maintenance and license | Verdict |
|---|---|---|---|---|
| [`bytemare/frost`](https://github.com/bytemare/frost) + [`bytemare/dkg`](https://github.com/bytemare/dkg) | **No**. RFC 9591 secp256k1 uses different key/challenge conventions; its signatures are not BIP-340 without a separately specified adaptation. | Dealerless DKG; opaque scalar/point operations. | Active 2026 tags, MIT. | Reject for direct Bahia use despite strong DKG. Curve choice alone is insufficient. |
| [`taurusgroup/multi-party-sig`](https://github.com/taurushq-io/multi-party-sig/blob/main/protocols/frost/frost.go) `v0.7.0-alpha-2025-01-28` | **Yes**, `KeygenTaproot` + `SignTaproot` explicitly implement BIP-340. | Dealerless DKG and exposed `PrivateShare`/verification shares permit a separate threshold-ECDH implementation; no high-level ECDH operation. | Last tagged version January 2025; Apache-2.0; upstream warns that further testing/auditing is needed. | Viable alternate for a later DKG ceremony, not selected for this contained signer seam. |
| [`fiatjaf.com/promenade/frost`](https://pkg.go.dev/fiatjaf.com/promenade/frost) `v0.4.4` | **Yes**. Its challenge is `BIP0340/challenge`, it normalizes even-Y shares/nonces, and Bahia's test verifies the result with independent `btcec/schnorr` under the group x-only pubkey. | **Trusted dealer only** (`TrustedKeyDeal`); `CreateECDHShare` and `AggregateECDHShards` expose Lagrange-weighted ECDH. | Tagged June 2026, Go 1.25, Unlicense, pre-v1, no claimed audit. | **Stage-1 choice.** Its explicit NIP-44-relevant ECDH surface keeps the experiment small. Trusted dealing and an audit are rollout blockers. |
| FROSTR-ORG [`bifrost`](https://github.com/FROSTR-ORG/bifrost) / [`bifrost-rs`](https://github.com/FROSTR-ORG/bifrost-rs), OwnAuth's FROSTR work | BIP-340 implementations exist, but the published Bifrost libraries are TypeScript/Rust, not a reusable pure-Go dependency. | Bifrost supports threshold ECDH; OwnAuth's dealerless ceremony is a protocol reference, not imported code. | Bifrost MIT; Rust/TypeScript do not meet the pure-Go requirement. | Transport convention/reference, not a Go crypto dependency. |
| `LLFourn/secp256kfun` and `bancaditalia/secp256k1-frost` | BIP-340-capable modes exist. | Different ecosystems. | Rust and C respectively. | Reject: not pure Go. |

The chosen `promenade` dependency is not cryptographically interoperable with
the Bifrost share package by assertion. Only the final signature and ECDH
result are checked against standard Nostr primitives. A production ceremony
must document how all three shares are generated, verified independently,
distributed, backed up, and destroyed at the dealer; replacing the dealer
with Taurus DKG is preferable if independently validated.

### Stage-1 behavior and costs

The in-process transport exercises actual 2-of-3 commitments and partial
signatures. A fake-relay test exercises the Nostr transport seam. The wire
skeleton uses Bifrost's [kind `20004`](https://github.com/FROSTR-ORG/bifrost/blob/master/src/const.ts), a dedicated transport identity per peer, a `p` tag, NIP-44-encrypted phase/session payloads, signed events, and a continuously supplied inbound event stream. Its relay adapter contract requires a scoped long-lived REQ and verified `OK true`; no live-relay or Bifrost wire-interoperability claim is made. Two publishes and two peer replies are needed per signature (commit and partial); threshold ECDH takes one exchange. Neither enqueue nor local signing is proof of relay acceptance.

Measured with `go test ./internal/servicesigner -bench BenchmarkFrostrSignInProcess -benchtime=500x -run '^$' -count=3` on Darwin arm64 / Apple M4 Max, Go 1.26.3. Three local runs were 1.210, 1.477 and 1.415 ms/op; the table uses the median (1.415 ms), with 167 allocations/op. Simulated RTT is a **virtual transport accounting model**, not a sleep or a live network measurement; it adds two serial RTTs to the measured local median. A live multi-relay and approval-path tail-latency test remains necessary.

| Operation/model | Local crypto median | Added RTT | Estimated per-publish signer cost |
|---|---:|---:|---:|
| In-process 2-of-3 signature | 1.415 ms | 0 | 1.415 ms |
| 25 ms peer/relay RTT | 1.415 ms | 2 × 25 ms | 51.415 ms |
| 100 ms peer/relay RTT | 1.415 ms | 2 × 100 ms | 201.415 ms |

Recommended production budget: reserve **at least 250 ms p95 for signing**
only if controlled relays and the operator share can demonstrate <=100 ms p95
per round exchange; otherwise do not put this custody method on the canonical
publish path. This excludes relay `OK` publication latency and human approval.
Every canonical event incurs the two signing exchanges; outbox retries that
reuse an already signed event do not sign again.

Threshold NIP-44 derives the conversation key without rebuilding the service
scalar: each holder computes `lambda_i * share_i * lift_x(peer)`; the daemon
adds two contributions, extracts the X coordinate and applies NIP-44 v2 HKDF
(`nip44-v2`). Tests compare that key against `nip44.GenerateConversationKey`
using a **test-only reconstructed key** for both daemon+operator and
daemon+cold subsets, and exercise binary ciphertext round trips. The library
does not expose a DLEQ proof for an ECDH contribution; a malicious peer can
cause a wrong derived key (decryption then fails), so authenticated
contribution proofs are a production hardening requirement.

### Recovery, fencing, and stage 2

The normal pair is daemon + operator. The cold share remains offline. If the
daemon is lost, an operator + cold-share ceremony can recover the existing
group identity and issue a replacement daemon share under a controlled
operator process. The running Bahia signer does **not** reconstruct the key,
does not load the cold share, and does not implement that ceremony. Losing two
shares is terminal. Compromising two shares gives complete signing and
decryption authority, including historic NIP-44 ciphertexts.

Single-writer authorization is a signer obligation. `frostrKeyer` serializes
local operations and acquires the injected `FrostrFence` for both signing and
threshold ECDH. A
process mutex is not a cross-process lease; the production fence must be
durable, identity-scoped, generation-aware, and enforced by peer policy before
any nonce commitment or ECDH contribution. A reload must not admit overlapping
writers. The stage-1 fake fence proves API enforcement only, not distributed
exclusion.

Stage 2, operator-gated under **h9n7f**, must provide: audited group-key
generation or DKG and secure daemon/operator/cold share provisioning; an
authenticated, replay-safe, admission-controlled persistent relay adapter and
real peer responders; externally enforced single-writer fencing and approval
policy; DLEQ or equivalent ECDH-share validation; controlled-relay RTT/tail
measurements; offline operator+cold recovery rehearsal; NIP-44/OCK access and
historical ciphertext migration planning; and explicit approval to rotate
canonical kind-30900 authorship from the old service pubkey to the new group
pubkey. This stage changes no production identity or canonical records.
