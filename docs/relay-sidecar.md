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

- `public_url` / `browser_relays` are exposed through ContextVM discovery (`11316`-`11320`) and NIP-51 relay sets (`30002`) to the frontend.
- `backend_url` is used by Bahia itself for publish/subscribe in sidecar-first mode. In Docker Compose this should point at `ws://relay:3334/relay` so backend and browser both target the explicit relay mount.
- When sidecar mode is enabled, Bahia's own ContextVM transport and canonical observable projectors use the sidecar URL instead of connecting directly to `nostr.relays`. This keeps ContextVM `25910`, CEP-4/NIP-59 wrappers (`1059`/`21059`), canonical observables (`30900`, `4903`, `30315`, `11316`-`11320`, `30002`, `30078`), and relevant interop traffic sidecar-first.
- Interop subscribers use `nostr.relays` unless `mirror_external=true`; with mirroring enabled, Bahia uses the sidecar as the public upstream boundary and does not also connect directly to mirrored upstream URLs. Private and Loom relays stay direct and separate.

## Local topology

`docker-compose.yml` starts:

- `relay`: the Khatru sidecar (`cmd/relay`) on `:3334` (serves both `/` and `/relay` for backward compatibility)
- `bahia`: backend publishing/subscribing to `nostr.sidecar.backend_url`
- `web`: nginx proxy exposing `/relay` to the browser

Browser flow:

1. Bootstrap from ContextVM discovery (`11316`-`11320`) and NIP-51 relay sets (`30002`).
2. Read the browser relay set and service pubkey.
3. Connect to `/relay` WebSocket.
4. Subscribe to scoped ContextVM responses/gift-wraps and canonical observables (`30900`, `4903`, `30315`, `11316`-`11320`, `30002`, `30078`) and wait for EOSE.
5. Keep live subscriptions open.

## Policy

The sidecar validates event IDs, signatures, and timestamp bounds before persistence.

- Direct ContextVM `25910` events must be signed by either the Bahia service pubkey or an authorized operator pubkey.
- ContextVM gift-wrap events (`1059` and `21059`) use random outer wrapper pubkeys, so the sidecar authorizes them by recipient `p` tag. The recipient must be the Bahia service pubkey or an authorized operator pubkey.
- ContextVM subscriptions must be scoped. Reads for `25910`, `1059`, and `21059` are allowed only when the filter scopes `authors` or `#p` to the Bahia service pubkey or authorized operator pubkeys.
- Bahia-authored canonical observable events require Bahia's service pubkey. That includes `30900`, `4903`, `30315`, `11316`-`11320`, `30002`, `30078`, and Bahia readiness/identity/checkpoint kinds.
- Legacy Bahia request/status/result/read-model/encrypted kinds (`5961`-`6006`, `6961`-`6997`, `7961`-`7997`, `31961`-`32003`, `31000`-`31024`, `5980`, `7980`) are migration-only and are not accepted as production sidecar contracts.
- `10100`, `30100`, `5101`, `5102`, `5401`, and `5402` interop events accept any valid signature; Bahia services still decide whether to act.

## Upstream mirroring guardrail

`mirror_external` is optional. Enable it only when the sidecar deployment is configured as the mirror boundary for `nostr.relays`. When true, Bahia routes public interop/audit subscriptions through the sidecar instead of also connecting directly to those upstream URLs; when false, Bahia keeps direct upstream subscriptions for non-control-plane interop. Bahia-owned control-plane publication remains sidecar-only in both modes.
