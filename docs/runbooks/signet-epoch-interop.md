# Local Signet epoch cryptography interop proof

**A passing disposable run is not activation authorization.**

This is an opt-in, **live NIP-46** test of Bahia's dormant epoch NIP-44 and
SBOM DSSE adapters plus the read-only wrapped-key assistant startup path. It does not activate remote signing in Bahia, deploy a
daemon, or remove a service key. Its only target is a disposable, synthetic
Signet identity on a private local relay. Do not use a production service
identity or copy an existing service nsec into this fixture.

## Fixture prerequisites

1. Build and run one Signet daemon from commit
   `d5af2ef3d802f651ab87ac3cd27a9fb2583829c7` with a fresh, disposable
   encrypted store and a private loopback-IP Nostr relay. Record the build commit;
   the test config's `signet_commit` is an operator assertion, not binary
   attestation. See Signet's `signet/docs/WRITER_EPOCH_CUTOVER.md` for its
   provisioning and single-active-store rules.
2. Through Signet's authenticated, encrypted provisioner management path,
   adopt a newly generated disposable service key. Confirm the returned
   service pubkey equals the test's `expected_service_pubkey`. Separately pin
   the Signet NIP-46 bunker pubkey as `expected_bunker_pubkey`; it is **not**
   the adopted service pubkey. Never paste, log, or commit the service secret.
   Pair a **different**, dedicated NIP-46 owner
   client with that agent. The owner must not be a Signet provisioner.
3. Set the agent policy to permit the four `nip44_*` methods and explicitly
   list `sign_bahia_sbom_dsse` in `allow_methods`; `*` alone does not opt in
   to DSSE. Acquire a writer lease for the owner twice, so the current epoch
   is at least 2. Use a TTL long enough for a two-minute test and record the
   current epoch and expiration returned by the authenticated management
   response. No other writer should touch this disposable identity while the
   test runs.
4. Put the following JSON in a **non-repository** file with mode `0600`.
   The bunker URI host must match `expected_bunker_pubkey` and have exactly one
   `ws://` or `wss://` relay at a loopback IP literal,
   with an explicit port. DNS names, non-loopback addresses and additional
   relay endpoints are rejected before any connection. Its owner key and
   bunker URI are sensitive even though the service key is
   disposable. Do not publish the file or test logs containing its contents.

```json
{
  "disposable": true,
  "signet_commit": "d5af2ef3d802f651ab87ac3cd27a9fb2583829c7",
  "bunker_uri": "bunker://<disposable-Signet-bunker-pubkey>?relay=ws%3A%2F%2F127.0.0.1%3A<private-port>",
  "owner_secret_key_hex": "<dedicated-disposable-NIP46-owner-hex>",
  "expected_bunker_pubkey": "<disposable-Signet-bunker-pubkey-hex>",
  "expected_service_pubkey": "<distinct-adopted-service-pubkey-hex>",
  "epoch": 2,
  "expires_at": "<writer-lease-expiration-RFC3339Nano>"
}
```

Run from the Bahia repository:

```bash
BAHIA_SIGNET_INTEROP_CONFIG=/path/outside/repo/fixture.json \
  go test -tags signetinterop ./internal/adapters/signet \
  -run '^TestLiveSignetEpochNIP44AndSBOMDSSE$' -count=1 -v
```

The tagged test **fails** if the fixture is absent; it never silently skips.
It connects through Bahia's real NIP-46 client, checks the pinned service
pubkey, round-trips text and binary NIP-44, signs a valid Bahia-built SBOM
statement through `sign_bahia_sbom_dsse`, and verifies the existing DSSE
envelope and key ID. It then sends no-epoch and stale-epoch probes for all
four NIP-44 methods and DSSE over the same authenticated connection. Each
probe requires an empty result, a decrypted NIP-46 remote-error response
(not a transport/context error), and a still-live bunker ping. Local URI
validation errors are generic and never echo the bunker URI or pairing secret.

This proof does not itself provision Signet, certify the daemon binary,
exercise a second wrong-owner client, or prove restart/expiry/revocation
behavior. Those require separate Signet and cutover gates. A passing run is
not authorization to enable Bahia's remote signer or remove its raw key.
Bahia still requires `nostr.private_key` and the raw control-plane signer; the
wrapped assistant mode is read-only and does not remove those dependencies.

The reproducible disposable runner is
`nostrc/signet/tests/interop/run_live_epoch_bahia.py`. It generates a synthetic
service key internally, adopts that key through Signet via stdin, and passes it
only through stdin to the assistant startup test. It never writes the service
secret to source, an argument vector, or logs. From a clean, committed Nostrc
worktree configured with a dedicated CMake build directory, run:

```bash
python3 signet/tests/interop/run_live_epoch_bahia.py \
  --build-dir /path/to/nostrc-worktree/_build \
  --bahia /path/to/committed-bahia-worktree \
  --bahia-commit <full-reviewed-Bahia-SHA>
```

The runner checks both the epoch adapter interop test and
`TestLiveAssistantWrappedStartupHistoricalReads` on the same disposable loopback
Signet lease. The latter proves historical transcript and checkpoint reads,
no raw-key fallback or new writes, and bootstrap client closure. It does not
enable v2 writers or remove the service key from Bahia configuration.
