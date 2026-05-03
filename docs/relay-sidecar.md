# Bahia Relay Sidecar

Bahia uses a Khatru-based Nostr relay sidecar as the supported browser-facing and server-facing control-plane topology. The sidecar stores rebuildable relay state; Bahia's database remains the source of truth and the projector republishes read-model snapshots when needed.

## Configuration

```yaml
nostr:
  relays:
    - "wss://upstream.example"     # public interop relays; mirror source when mirror_external=true
  browser_relays:
    - "ws://localhost:3000/relay"  # browser-safe discovery
  sidecar:
    enabled: true
    listen_addr: "0.0.0.0:3334"
    public_url: "ws://localhost:3000/relay"
    backend_url: "ws://relay:3334"
    data_dir: "./data/relay-sidecar"
    mirror_external: false
    event_retention: 168h
    request_retention: 24h
    auth_private_key: ""
    max_query_limit: 500
```

- `public_url` / `browser_relays` are exposed by `/api/v1/system/info` to the frontend.
- `backend_url` is used by Bahia itself for publish/subscribe in sidecar-first mode. In Docker Compose this should point at `ws://relay:3334/relay` so backend and browser both target the explicit relay mount.
- When sidecar mode is enabled, Bahia's own control-plane reactor/projector use the sidecar URL instead of connecting directly to `nostr.relays`. This keeps canonical 696x/796x/3196x/read-model publication sidecar-only.
- Interop subscribers use `nostr.relays` unless `mirror_external=true`; with mirroring enabled, Bahia uses the sidecar as the public upstream boundary and does not also connect directly to mirrored upstream URLs. Private and Loom relays stay direct and separate.

## Local topology

`docker-compose.yml` starts:

- `relay`: the Khatru sidecar (`cmd/relay`) on `:3334` (serves both `/` and `/relay` for backward compatibility)
- `bahia`: backend publishing/subscribing to `nostr.sidecar.backend_url`
- `web`: nginx proxy exposing `/relay` to the browser

Browser flow:

1. Fetch `/api/v1/system/info`
2. Read `nostr.browser_relays`
3. Connect to `/relay` WebSocket
4. Query 31961/31962/31963 + activity/status kinds and wait for EOSE
5. Keep live subscriptions open

## Policy

The sidecar validates event IDs, signatures, and timestamp bounds before persistence.

- `5961`-`5968` requests require `nostr.authorized_pubkeys`.
- `696x`, `796x`, `31961`-`31963`, and `310xx` Bahia projections require Bahia's service pubkey.
- `10100`, `30100`, `5101`, `5102`, `5401`, and `5402` interop events accept any valid signature; Bahia services still decide whether to act.

## Upstream mirroring guardrail

`mirror_external` is optional. Enable it only when the sidecar deployment is configured as the mirror boundary for `nostr.relays`. When true, Bahia routes public interop/audit subscriptions through the sidecar instead of also connecting directly to those upstream URLs; when false, Bahia keeps direct upstream subscriptions for non-control-plane interop. Bahia-owned control-plane publication remains sidecar-only in both modes.
