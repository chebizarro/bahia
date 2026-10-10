# Configuring Bahia's service signer

Bahia signs and NIP-44-encrypts as its **service identity**. Where that key
lives is selected by `nostr.signer.method`; the rest of Bahia sees a standard
signer. The contract is in
[`docs/architecture/service-signer.md`](../architecture/service-signer.md).

Every method is pinned to the existing identity: startup fails if the signer
reports a pubkey other than `nostr.public_key`. Never change
`nostr.public_key` to make a signer "fit" — move the existing key into the
signer instead.

## Settings

| Key | Env | Meaning |
|---|---|---|
| `nostr.public_key` | `BAHIA_NOSTR_PUBLIC_KEY` | Service pubkey, 64 hex. Required for `nip46`/`nip55l`; optional for `local` (must match the key if set). |
| `nostr.signer.method` | `BAHIA_NOSTR_SIGNER_METHOD` | `local`, `nip46` or `nip55l`. Empty = `local` if `nostr.private_key` is set. |
| `nostr.signer.bunker_uri` | `BAHIA_NOSTR_SIGNER_BUNKER_URI` | `bunker://…` URI or NIP-05 name (`nip46`). Secret. |
| `nostr.signer.client_secret_key` | `BAHIA_NOSTR_SIGNER_CLIENT_SECRET_KEY` | Bahia's dedicated NIP-46 client key, 64 hex (`nip46`). Secret. |
| `nostr.signer.client_secret_key_file` | `BAHIA_NOSTR_SIGNER_CLIENT_SECRET_KEY_FILE` | Absolute path to a file holding that key instead (≤ 4 KiB). |
| `nostr.signer.nip55l.bus_address` | `BAHIA_NOSTR_SIGNER_NIP55L_BUS_ADDRESS` | D-Bus address; empty = session bus (`nip55l`). |
| `nostr.signer.nip55l.app_id` | `BAHIA_NOSTR_SIGNER_NIP55L_APP_ID` | Application id shown to the signer's approval policy; default `bahia`. |
| `nostr.signer.timeout` | `BAHIA_NOSTR_SIGNER_TIMEOUT` | Bounds connecting and each signer request; default `30s`. |

Startup rejects: an unknown method; a remote method together with
`nostr.private_key` (mixed custody); a remote method without
`nostr.public_key`; `nip46` without a bunker URI or without exactly one of the
client key settings; settings that belong to a different method; a NIP-46
client key equal to the service key. Secrets are redacted from logs and
config renderings.

`assistant.wrapped_keys.signet_bunker_uri`, `owner_client_secret_key` and
`connect_timeout` have been removed; startup fails if they are still set.
Wrapped assistant keys use the service signer. With a remote method the
assistant requires `assistant.wrapped_keys.mode=wrapped_read_only`, because
`legacy_v1` derives transcript keys from the raw service key.

## Local key

Today's behaviour; no change is needed:

```yaml
nostr:
  private_key: <64-hex service secret key>   # or BAHIA_NOSTR_PRIVATE_KEY
```

## Any NIP-46 bunker

```yaml
nostr:
  public_key: <64-hex existing service pubkey>
  signer:
    method: nip46
    bunker_uri: bunker://<bunker-pubkey>?relay=wss%3A%2F%2Frelay.example&secret=<connect-secret>
    client_secret_key_file: /run/secrets/bahia-nip46-client
```

1. Import the **existing** service key into the bunker so it reports the same
   pubkey. Do not generate a new identity.
2. Generate a dedicated client key for this Bahia deployment
   (`nak key generate`) and store it as a mounted secret. It must not be the
   service key and should not be shared with any other client.
3. Pair: put the bunker's `bunker://` URI (with its connect secret, if any) in
   `nostr.signer.bunker_uri`. If the bunker asks for out-of-band approval,
   Bahia logs the authorization URL.
4. Configure the bunker to let only that client key use the service identity.
   How is bunker-specific.
5. Remove `nostr.private_key` from the deployment and start Bahia. Startup
   fails closed on a pubkey mismatch, an unreachable bunker or a refused
   `get_public_key`.

Features that need binary NIP-44 (`nip44_encrypt_b64`/`nip44_decrypt_b64`)
fail with an "unsupported" error on bunkers that do not implement that
extension; everything else uses standard NIP-46 only.

## NIP-55L (local D-Bus signer)

```yaml
nostr:
  public_key: <64-hex existing service pubkey>
  signer:
    method: nip55l
    nip55l:
      bus_address: ""        # session bus
      app_id: bahia
```

The signer on the bus must hold the existing service key and allow `app_id`.

## Example: using Signet as the bunker

This is one example of a NIP-46 bunker that fences a service identity to a
single writer. Bahia needs nothing Signet-specific; the steps below are
Signet operator commands (run with a provisioner identity) at the Signet
release you deploy.

1. **Adopt the existing key** so the pubkey is unchanged (secret on stdin,
   never in argv):

   ```bash
   signetctl -c signet.conf adopt-existing bahia-service \
     --sec - --expected-pubkey <existing-service-pubkey> < service.nsec.hex
   ```

   The reply includes `pubkey` (must equal `nostr.public_key`) and a
   `bunker_uri` with a one-time connect secret.
2. **Assign Bahia's dedicated client key as the writer.** Writer keys are
   single-use: a key that was ever a writer can never be reassigned, so a new
   deployment (or a rotation) always uses a freshly generated client key.

   ```bash
   signetctl -c signet.conf writer-acquire bahia-service <bahia-client-pubkey>
   ```

   From then on Signet accepts `sign_event` and NIP-44 for this identity only
   from that client; every other client, including a former writer, is
   refused.
3. If the original connect secret was used, mint another with
   `signetctl -c signet.conf reissue-connect bahia-service --out <0600-file>`
   and put it in the URI's `secret` parameter.
4. Allow the methods Bahia uses in the identity's policy: `connect`,
   `get_public_key`, `sign_event`, `nip44_encrypt`, `nip44_decrypt`, and
   (for binary NIP-44) `nip44_encrypt_b64`, `nip44_decrypt_b64`.
5. Configure Bahia as in [Any NIP-46 bunker](#any-nip-46-bunker) with the
   writer's client key and the Signet `bunker_uri`.

To move the writer to a new Bahia deployment, generate a new client key and
`writer-acquire` it; the old deployment's requests are then refused.
`scripts/signet_live_interop.py` exercises exactly this flow against a
disposable `signetd`; see [`signet-interop.md`](signet-interop.md).
