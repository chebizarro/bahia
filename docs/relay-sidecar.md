# Bahia Relay Sidecar

Bahia uses a Khatru-based Nostr relay sidecar as the supported browser-facing and server-facing control-plane topology. The sidecar is a standalone HTTP/NIP-11/WebSocket Nostr relay server, not a REST CRUD API: `cmd/relay/main.go` loads config and starts `relaysidecar.New(...).Run(ctx)`, while `internal/relaysidecar/server.go` mounts the Khatru relay on `/` and on the path from `nostr.sidecar.public_url` such as `/relay`. The sidecar stores rebuildable relay state; Bahia's database remains the source of truth and the projector republishes read-model snapshots when needed.

## Configuration

```yaml
nostr:
  relays:
    - "wss://upstream.example"     # compatibility service/upstream interop source; never browser or ContextVM policy
  service_relays:
    - "wss://service-relay.example" # backend service publish/backfill relays
  browser_relays:
    - "ws://localhost:3000/relay"  # browser relay discovery
  contextvm_relays:
    - "ws://localhost:3000/relay"  # ContextVM request/reply relays; may intentionally reuse the sidecar URL
  relay_auth_unavailable: "exclude_and_fail"
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

- `public_url` / `browser_relays` are exposed through ContextVM discovery (`11316`-`11320`) and the `d=bahia-browser-v1` NIP-51 relay set (`30002`) to the frontend.
- `contextvm_relays` is the preferred ContextVM request/reply relay policy and is projected as `d=bahia-contextvm-v1`; deployments may intentionally reuse the sidecar URL, but the purpose remains distinct from browser bootstrap.
- `service_relays` is the backend service publish/backfill policy and is projected as `d=bahia-service-v1`; `nostr.relays` remains only a backward-compatible service alias.
- `relay_auth_unavailable=exclude_and_fail` means auth-required relays without usable credentials are excluded from the current operation and the operation fails if remaining relays cannot satisfy the success rule.
- `backend_url` is used by Bahia itself for publish/subscribe in sidecar-first mode. In Docker Compose this should point at `ws://relay:3334/relay` so backend and browser both target the explicit relay mount.
- When sidecar mode is enabled, Bahia's own ContextVM transport and canonical observable projectors use the sidecar URL instead of connecting directly to `nostr.relays`. This keeps ContextVM `25910`, CEP-4/NIP-59 wrappers (`1059`/`21059`), canonical observables (`30900`, `4903`, `30315`, `11316`-`11320`, `30002`, `30078`), and relevant interop traffic sidecar-first.
- During the compatibility window, non-browser interop subscribers may still use `nostr.relays` as the upstream interop source unless `mirror_external=true`; service publish/backfill should prefer `service_relays`, browser bootstrap uses `browser_relays`, and ContextVM request/reply uses `contextvm_relays`. With mirroring enabled, Bahia uses the sidecar as the public upstream boundary and does not also connect directly to mirrored upstream URLs. Private, Loom, repository/ngit, DM, and relay-administration relays stay direct and separate unless a deployment explicitly routes those purposes through the sidecar.

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

The sidecar accepts storage and subscriptions for every Nostr event kind. It does not maintain event-kind, author, recipient, or filter-scope allowlists.

Before persistence, it still enforces protocol validity: the event ID must match the serialized event, the Schnorr signature must verify, and `created_at` must fall within the configured operational timestamp bounds. Search remains disabled because the in-memory store does not implement NIP-50; ordinary NIP-01 filters, including filters without `kinds`, are accepted.

Authorization belongs to consumers. Bahia validates signatures, authors, encryption, tags, capabilities, and application semantics before acting on an event. Relay admission is not an authorization boundary.

## Upstream mirroring guardrail

`mirror_external` is optional. Enable it only when the sidecar deployment is configured as the mirror boundary for `nostr.relays`. When true, Bahia routes public interop/audit subscriptions through the sidecar instead of also connecting directly to those upstream URLs; when false, Bahia keeps direct upstream subscriptions for non-control-plane interop. Bahia-owned control-plane publication remains sidecar-only in both modes.
