# Fenced service-key NIP-44 adapter

`internal/adapters/signet.EpochSigner` is a dormant `nostr.Keyer` adapter for
the **existing** Bahia service pubkey. Bahia's runtime does not select it yet;
the service-key cutover requires separate data-key migration and startup
policy work. Constructing the adapter requires a real Signet bunker URI, an
explicit dedicated NIP-46 owner client key, the expected existing service
pubkey, and a live `WriterLeaseSource`. The adapter checks the connected
bunker's pubkey against that pinned identity before each request.

The NIP-44 operations call only Signet's epoch-fenced NIP-46 methods. Every
request carries `[peer_pubkey_hex, input, decimal_writer_epoch]`:

| Operation | Method | Input/result |
|---|---|---|
| Text encrypt | `nip44_encrypt` | UTF-8 plaintext / NIP-44 v2 payload |
| Text decrypt | `nip44_decrypt` | NIP-44 v2 payload / UTF-8 plaintext |
| Binary encrypt | `nip44_encrypt_b64` | base64 plaintext bytes / NIP-44 v2 payload |
| Binary decrypt | `nip44_decrypt_b64` | NIP-44 v2 payload / base64 plaintext bytes |

The adapter rejects absent, expired, wrong-owner, or stale leases; verifies
the lease and connection again after the RPC; and rejects empty or malformed
results. It never retries through a no-epoch method or raw service key. NIP-04
is unsupported on this service-key adapter. An epoch-configured `Client` routes
its public text NIP-44 and binary-encrypt calls through the same fenced path.
The binary-decrypt helper remains internal until a production caller needs it.
Text operations reject embedded NUL and invalid UTF-8, matching Signet's text
contract; callers with arbitrary bytes must use the binary methods. Ciphertext
and binary-decrypt plaintext are each capped at 16 MiB of base64 before
decoding, including untrusted RPC results.

The local check is not the authorization boundary: Signet must authenticate
the dedicated NIP-46 client and atomically validate its writer lease epoch for
each method. The adapter has no app startup wiring or automatic activation.
Unit tests use a fake RPC session, not a running Signet. Activation additionally
requires an authenticated Signet-v3 interoperability test for all four methods
with a live writer lease, including wrong/expired epoch rejection and empty or
error result handling.
