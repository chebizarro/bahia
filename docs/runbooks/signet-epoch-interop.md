# Local Signet epoch cryptography interop proof

**NOT YET RUN OR PROVEN. A passing run is not activation authorization.**

This is an opt-in, **live NIP-46** test of Bahia's dormant epoch NIP-44 and
SBOM DSSE adapters. It does not activate remote signing in Bahia, deploy a
daemon, or remove a service key. Its only target is a disposable, synthetic
Signet identity on a private local relay. Do not use a production service
identity or copy an existing service nsec into this fixture.

## Fixture prerequisites

1. Build and run one Signet daemon from commit
   `d097cea2219a3784ccf011d1e9bf94f9fb7f8ce2` with a fresh, disposable
   encrypted store and a private loopback-IP Nostr relay. Record the build commit;
   the test config's `signet_commit` is an operator assertion, not binary
   attestation. See Signet's `signet/docs/WRITER_EPOCH_CUTOVER.md` for its
   provisioning and single-active-store rules.
2. Through Signet's authenticated, encrypted provisioner management path,
   adopt a newly generated disposable service key. Confirm the returned
   pubkey equals the test's `expected_service_pubkey`. Never paste, log, or
   commit the service secret. Pair a **different**, dedicated NIP-46 owner
   client with that agent. The owner must not be a Signet provisioner.
3. Set the agent policy to permit the four `nip44_*` methods and explicitly
   list `sign_bahia_sbom_dsse` in `allow_methods`; `*` alone does not opt in
   to DSSE. Acquire a writer lease for the owner twice, so the current epoch
   is at least 2. Use a TTL long enough for a two-minute test and record the
   current epoch and expiration returned by the authenticated management
   response. No other writer should touch this disposable identity while the
   test runs.
4. Put the following JSON in a **non-repository** file with mode `0600`.
   The bunker URI must pin the expected service pubkey and have exactly one
   `ws://` or `wss://` relay at a loopback IP literal (`127.0.0.1` or `::1`),
   with an explicit port. DNS names, non-loopback addresses and additional
   relay endpoints are rejected before any connection. Its owner key and
   bunker URI are sensitive even though the service key is
   disposable. Do not publish the file or test logs containing its contents.

```json
{
  "disposable": true,
  "signet_commit": "d097cea2219a3784ccf011d1e9bf94f9fb7f8ce2",
  "bunker_uri": "bunker://<disposable-service-pubkey>?relay=ws%3A%2F%2F127.0.0.1%3A<private-port>",
  "owner_secret_key_hex": "<dedicated-disposable-NIP46-owner-hex>",
  "expected_service_pubkey": "<same-disposable-service-pubkey-hex>",
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
