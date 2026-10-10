# Fenced service-key Signet signer

`internal/adapters/signet.ServiceSigner` is a `nostr.Keyer` adapter for the
**existing** Bahia service pubkey whose key is held by Signet. Bahia's
runtime selects it only for the read-only assistant wrapped-key startup
(`assistant.wrapped_keys.mode=wrapped_read_only`); the general service-key
cutover still requires separate data-key migration and startup policy work.
Constructing the adapter requires a real Signet bunker URI, an explicit
dedicated NIP-46 client key that differs from the service key, and the
expected existing service pubkey. The adapter checks the connected bunker's
pubkey against that pinned identity before each request and checks that the
connection is unchanged before accepting each result.

Every request is standard NIP-46 with standard parameters only:

| Operation | Method | Params / result |
|---|---|---|
| Sign | `sign_event` | `[unsigned_event_json]` / signed event JSON |
| Text encrypt | `nip44_encrypt` | `[peer_pubkey_hex, plaintext]` / NIP-44 v2 payload |
| Text decrypt | `nip44_decrypt` | `[peer_pubkey_hex, ciphertext]` / UTF-8 plaintext |
| Binary encrypt | `nip44_encrypt_b64` | `[peer_pubkey_hex, base64(plaintext)]` / NIP-44 v2 payload |

`nip44_encrypt_b64` is Signet's general binary-safe NIP-44 method (documented
in Signet's README for Concord CORD-06 rekey blobs, whose fixed width is a
format signal). Only the transport is base64; the result is an ordinary
NIP-44 v2 payload over the exact bytes.

## The fence

Signet is the authorization boundary. A fenced service identity accepts
`sign_event` and NIP-44 requests only from the authenticated NIP-46 client
pubkey that a provisioner assigned with the management operation
`agent/writer-acquire`. Writer client keys are single-use: once a key has been
a writer for any identity it can never be assigned again, so a displaced
writer cannot be reinstated and its delayed requests are refused. There is no
epoch on the wire, no lease expiry and no renewal. Bahia's side of the
contract is to use a dedicated, explicit client key and to pin the service
pubkey; it carries no lease state.

The adapter verifies `sign_event` results (unchanged kind, timestamp, content
and tags; pinned author; valid NIP-01 id and signature) and rejects empty or
malformed NIP-44 results. It never retries through a raw service key. NIP-04 is
unsupported. Text operations reject embedded NUL and invalid UTF-8, matching
Signet's text contract; callers with arbitrary bytes must use the binary
method. Ciphertext is capped at 16 MiB of base64 before decoding, including
untrusted RPC results.

A `Client` configured with `ExpectedServicePubkey` routes `Sign`,
`NIP44Encrypt`, `NIP44Decrypt` and `NIP44EncryptBytes` through this signer and
never falls back to its unpinned path. Such a client is an assigned writer,
not a Signet provisioner: it does not open the provisioner management relay
pool, and its agent-management methods fail closed. A separate
provisioner-backed client, distinct from the fenced service identity, retains
the bunker-signed management NIP-42 AUTH, signed seals and reply
subscription. The writer pubkey must differ from both the service pubkey and
every Signet provisioner pubkey.

Unit tests use a fake RPC session. The live proof against a real `signetd` is
`scripts/signet_live_interop.py`; see
[`docs/runbooks/signet-interop.md`](../runbooks/signet-interop.md).
