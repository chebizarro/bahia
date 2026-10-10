# Live NIP-46 service signer interop proof (Signet as the bunker)

**A passing disposable run is not activation authorization.**

This is an opt-in, **live NIP-46** test of Bahia's service signer
(`servicesigner.Open` with `nostr.signer.method=nip46`: `get_public_key`,
`sign_event`, `nip44_encrypt`, `nip44_decrypt`, `nip44_encrypt_b64`,
`nip44_decrypt_b64`), event-signed SBOM attestations, and the read-only
wrapped-key assistant startup path, against a real `signetd`. Signet is only
the bunker under test; Bahia uses nothing Signet-specific. The run does not
deploy a daemon or remove a service key. Its only target is a disposable,
synthetic identity on a private loopback relay. Bahia owns the runner; Signet
is an input.

## Run it

Prerequisites: `nak` (with `nak serve`), Go, CMake, git, and Signet's build
dependencies. Configure a CMake build directory from a **clean** Signet
(nostrc) checkout at the commit you reviewed, for example:

```bash
cmake -S /path/to/nostrc -B /path/to/nostrc/_build -DBUILD_APPS=OFF \
  -DWITH_NOSTRDB=OFF -DLIBNOSTR_WITH_NOSTRDB=OFF -DSIGNET_ENABLE_PASSKEYS=OFF
```

Then, from a **clean, committed** Bahia checkout:

```bash
python3 scripts/signet_live_interop.py \
  --signet-repo /path/to/nostrc \
  --build-dir /path/to/nostrc/_build \
  --signet-commit <reviewed-full-40-character-nostrc-SHA>
```

The runner refuses a Signet checkout that is dirty or not at
`--signet-commit`, a build directory not configured from that checkout, and a
dirty Bahia checkout, so a pass identifies both exact revisions. It rebuilds
`signetd` and `signetctl` before running and prints one `PASS:` line naming
the Bahia and Signet commits. Runner guard tests (no daemon):
`python3 test/scripts/test_signet_live_interop.py`.

## What it does

1. Starts `nak serve` bound to `127.0.0.1` on a free port and one `signetd`
   with a fresh SQLCipher store, a random DB key, and synthetic bunker and
   provisioner keys, all in a mode-`0700` temporary directory that is
   removed on exit. The identity policy allows only `connect`,
   `get_public_key`, `sign_event`, `nip44_encrypt`, `nip44_decrypt`,
   `nip44_encrypt_b64` and `nip44_decrypt_b64`.
2. Adopts a synthetic service key through the provisioner-authenticated
   `signetctl adopt-existing` path (secret on stdin) and checks the pairing
   URI pins the bunker pubkey and only the loopback relay.
3. `signetctl writer-acquire <agent> <first-client-pubkey>` (no TTL), then
   runs `TestLiveNIP46AssignedWriterSigns`: Bahia opens its service signer
   with the first client key and signs.
4. `signetctl reissue-connect --out` mints a second one-time connect secret
   (written `0600`), and `writer-acquire` reassigns the writer to a second,
   single-use client key. Runs `TestLiveNIP46ServiceSigner`: opening with a
   wrong `nostr.public_key` fails; the writer's signer reports the pinned
   service pubkey, signs an event, round-trips text and binary NIP-44 in both
   directions (each result cross-checked with a local NIP-44
   implementation), and signs and verifies an SBOM attestation event. The
   displaced first client still opens (the identity is public) but
   `sign_event` and every NIP-44 method are refused with a decrypted NIP-46
   remote error, never reported as unsupported, and the request event is
   unchanged; the writer still signs afterwards, and a signer whose context
   ended refuses locally.
5. Compiles the app test binary and runs
   `TestLiveAssistantWrappedStartupHistoricalReads` through the NIP-46 service
   signer, with the synthetic service secret supplied **only on stdin** (only
   to create the legacy v1 key and the manifest). It proves historical
   transcript and checkpoint reads through both the shared signer and the
   startup path, and no raw-key fallback or new writes.

A test counts only if `go test -json` (or `go tool test2json` for the app
binary) shows exactly one Run and one Pass for that named test; an exit-zero
no-test, skip or repeated run is a failure. No secret, pairing URI or raw test
output is printed; on failure only the failing test line is reported.

## Fixture format

The runner writes a private mode-`0600` fixture and passes its path in
`BAHIA_SIGNET_INTEROP_CONFIG`. The tagged tests **fail** without it; they never
silently skip. To run a test by hand against a disposable daemon you
provisioned yourself, use the same shape in a non-repository file:

```json
{
  "disposable": true,
  "signet_commit": "<full-40-character-nostrc-SHA>",
  "expected_bunker_pubkey": "<Signet NIP-46 bunker pubkey hex>",
  "expected_service_pubkey": "<distinct adopted service pubkey hex>",
  "writer_bunker_uri": "bunker://<bunker-pubkey>?relay=ws%3A%2F%2F127.0.0.1%3A<port>&secret=<one-time-secret>",
  "writer_secret_key_hex": "<client key currently assigned by writer-acquire>",
  "displaced_bunker_uri": "<first pairing URI; only for the signer test>",
  "displaced_writer_secret_key_hex": "<previously assigned writer key; only for the signer test>"
}
```

```bash
BAHIA_SIGNET_INTEROP_CONFIG=/path/outside/repo/fixture.json \
  go test -tags signetinterop ./internal/servicesigner \
  -run '^TestLiveNIP46ServiceSigner$' -count=1 -v
```

Each bunker URI must pin `expected_bunker_pubkey` and have exactly one `ws://`
or `wss://` relay at a loopback IP literal with an explicit port. The writer
keys must differ from the service key and from each other (writer keys are
single-use). Local URI validation errors never echo the URI or secret.

## Limits

This proof does not certify the daemon binary beyond the pinned source
commit, or prove Signet restart or revocation behavior. A passing run is not
authorization to remove a production service key. Configuring a remote
signer is described in [`service-signer.md`](service-signer.md); the wrapped
assistant mode remains read-only.
