# Service signer

Bahia's service identity is one `nostr.Keyer` built by
`internal/servicesigner.Open` from `nostr.signer`. Bahia does not care what
holds the key; it only requires the signer to report the configured service
pubkey. Operator configuration is in
[`docs/runbooks/service-signer.md`](../runbooks/service-signer.md).

| Method | Key custody | Transport |
|---|---|---|
| `local` (default when only `nostr.private_key` is set) | process memory | none |
| `nip46` | any NIP-46 bunker | kind-24133 requests through `nostrout.Bunker` (outbound admission, signer lane) |
| `nip55l` | a local NIP-55L signer | D-Bus, through an injected constructor |

## Identity pinning

`Open` reads the signer's pubkey once at startup and fails if it differs from
`nostr.public_key` (or, for `local` without `nostr.public_key`, from the
private key's own pubkey). This preserves the existing identity across a
custody change: switching method must never mint a new service pubkey. A
remote method never coexists with `nostr.private_key`; configuration rejects
that mixed mode, and no code path falls back to a raw key when a remote signer
refuses or is unreachable.

## Single-writer fencing belongs to the signer

Bahia authenticates to a remote signer with its own dedicated client identity
(`nostr.signer.client_secret_key` for NIP-46, which must differ from the
service key) and sends only standard requests. Deciding which client may use
the service key, and refusing everyone else, is the signer's job. Bahia never
calls signer-specific or management methods for its own identity.

## NIP-46 request contract

Every request uses standard NIP-46 parameter shapes:

| Operation | Method | Params / result |
|---|---|---|
| Identity check (startup) | `get_public_key` | `[]` / pubkey hex |
| Sign | `sign_event` | `[unsigned_event_json]` / signed event JSON |
| Text encrypt | `nip44_encrypt` | `[peer_pubkey_hex, plaintext]` / NIP-44 v2 payload |
| Text decrypt | `nip44_decrypt` | `[peer_pubkey_hex, ciphertext]` / UTF-8 plaintext |
| Binary encrypt (optional) | `nip44_encrypt_b64` | `[peer_pubkey_hex, base64(plaintext)]` / NIP-44 v2 payload |
| Binary decrypt (optional) | `nip44_decrypt_b64` | `[peer_pubkey_hex, ciphertext]` / base64(plaintext) |

`sign_event` results must keep the requested kind, timestamp, content and
tags, carry the pinned author and a valid NIP-01 id and signature; anything
else is discarded and the caller's event is unchanged. Text operations reject
embedded NUL and invalid UTF-8. NIP-44 payloads are capped at 16 MiB of base64
before decoding, including untrusted results. NIP-04 is unsupported.
`nostr.signer.timeout` bounds the connect handshake and every request; the
session lives as long as the context `Open` received. The session owns its
bunker relay pool: ending the session, or a failed connect, closes the bunker
relay connections.

## Reload

`SIGHUP` builds a complete candidate application before stopping the running
one (`cmd/server`), so an invalid config never takes the daemon down. The
service signer session is handed across that swap instead of duplicated:

- **Comparison.** `servicesigner.SameSigner(a, b)` is true when the effective
  method, the trimmed `nostr.private_key`, the case-insensitive
  `nostr.public_key` and every `nostr.signer` setting are equal. It does no
  I/O, so a config that still names `client_secret_key_file` is never the same
  signer: the path alone cannot show a rotated key. `internal/app` resolves the
  file to the key it holds at reload (`servicesigner.ResolveClientKeyFile`),
  compares that, and opens exactly the key it compared. Rotating the file's
  content and reloading opens a session with the new key; moving the same key
  between inline and file keeps the session.
- **Unchanged.** The candidate (`app.New(cfg, app.Replacing(running))`) takes
  a hold on the running session; no second `connect` is sent. Each
  application releases its hold last in its shutdown and the session closes
  on the final release, so the replaced application never closes a session
  its replacement uses, a failed candidate only drops its hold, and the
  session closes exactly once at final shutdown.
- **Changed.** The candidate opens its own session while the running
  application keeps signing. On success the replaced application closes its
  session when it stops; on failure the candidate closes its new session and
  the running one is untouched. Two sessions overlap until the replaced
  application stops. If both use the same client key (only the bunker URI,
  relays or timeout changed), a bunker that keeps one session per client may
  refuse one of them during that window. With a new client key, the bunker
  decides which client may sign (Signet: the client last
  `writer-acquire`d). A candidate whose bunker does not answer still costs up
  to `nostr.signer.timeout` before it is rejected.

## Binary NIP-44 capability

NIP-46 params are JSON strings, so arbitrary bytes (Concord CORD-06 rekey
blobs, for example) cannot ride `Encrypt`. Callers that need bytes
type-assert `servicesigner.BinaryCipher`:

```go
EncryptBytes(ctx, plaintext []byte, recipient nostr.PubKey) (string, error)
DecryptBytes(ctx, ciphertext string, sender nostr.PubKey) ([]byte, error)
```

`local` implements it natively. Over NIP-46 it uses the `nip44_*_b64` pair, a
documented non-standard extension some bunkers serve; only the plaintext side
is base64 and the ciphertext is an ordinary NIP-44 v2 payload. If the bunker
answers that the method is unknown or unsupported, the call returns an error
wrapping `errors.ErrUnsupported`. A refusal (for example, a client that is not
the assigned writer) is a different error.

## Tests

Unit tests in `internal/servicesigner` drive the NIP-46 keyer through a fake
RPC, including adversarial `sign_event` results. The live proof against a real
bunker (Signet) is `scripts/signet_live_interop.py`; see
[`docs/runbooks/signet-interop.md`](../runbooks/signet-interop.md).
