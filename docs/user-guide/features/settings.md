# Settings

**Settings** (`/settings`) combines browser-local preferences, signer configuration, trusted relay policy, fleet configuration, and read-only system discovery.

## Main settings page

The page provides:

- Nostr Connect setup by `nostrconnect://` URI or QR code;
- light/dark appearance;
- links to operational settings;
- read-only service identity, service relays, publish state, Blossom, OCI registry, runtime, feature, registry, and version information.

Observed deployment versions come from runtime observations. Build information describes packaged artifacts and does not prove that a version is running.

## Profile

**Profile** (`/settings/profile`) edits Nostr kind-0 metadata and publishes it with the active browser or remote signer. The page reports each relay's `OK` result. A successful profile publish does not change Bahia organization membership or roles.

## Relays

**Relays** (`/settings/relays`) manages persistent operator relay policy and reconnects the current browser session. The browser's trusted bootstrap seed is injected when the web container starts:

- `PUBLIC_BAHIA_BOOTSTRAP_RELAYS`
- `PUBLIC_BAHIA_SERVICE_PUBKEYS`

The container entrypoint writes these values into the runtime seed, so one image can be deployed with different trusted relays and service identities. Local session overrides do not change daemon relay policy.

The relay sidecar defaults `nostr.sidecar.read_auth_mode` to `enforce`. Unset or unknown values also normalize to `enforce`. Protected REQ/COUNT filters therefore require NIP-42 authentication from an admitted key. `warn` observes without enforcing; `off` disables the read-auth gate.

## OpenClaw Fleet

**OpenClaw Fleet** (`/settings/fleet`) edits and publishes the trusted kind-`31953` fleet template. The page supports structured sections or raw JSON, shows a diff, reports relay outcomes, and tracks rollout across current Souls.

The signed-in key is trusted for its own document. Other publishers must appear in the fleet-OCK-encrypted `operators:soul-factory` allowlist. An unreadable allowlist never widens trust.

## Security boundary

- Browser signer state and appearance are local to the browser session.
- Server configuration shown on the page is read-only.
- Fleet and relay policy changes are signed events.
- Credentials and private keys are not part of discovery output.
- A relay `OK` is publication evidence, not daemon application evidence.

## Related

- [Organizations](organizations.md)
- [Souls](souls.md)
- [Config Fabric](config-fabric.md)
- [Nostr Integration](../nostr-integration.md)
