# Bahia Relay Sidecar

Bahia now includes additive scaffolding for a Khatru-based local Nostr relay sidecar. This is phase-1 infrastructure only: it does not cut the frontend over from SSE, does not remove JWT exchange, and does not publish read-model projections yet.

## Configuration

The sidecar is configured under `nostr.sidecar`:

```yaml
nostr:
  browser_relays:
    - "ws://localhost:3000/relay"
  sidecar:
    enabled: true
    listen_addr: "0.0.0.0:3334"
    public_url: "ws://localhost:3000/relay"
    data_dir: "./data/relay-sidecar"
    mirror_external: false
    event_retention: 168h
    request_retention: 24h
    auth_private_key: ""
    max_query_limit: 500
```

`browser_relays` is browser-safe relay discovery. `/api/v1/system/info` advertises those relays only when `nostr.sidecar.enabled=true`. `relay_read_models` intentionally remains `false` until the projector/read-model bead lands.

## Local topology

`docker-compose.yml` starts a separate `relay` service from `cmd/relay` and exposes it in two ways:

- direct local testing: `ws://localhost:3334`
- browser/web topology: `ws://localhost:3000/relay` through `web/nginx.conf`

The sidecar is isolated from Bahia's database. Current storage is in-memory and rebuildable; `data_dir` is reserved for the later durable eventstore implementation.

## Current policy

The phase-1 sidecar validates event IDs, Schnorr signatures, and timestamp bounds before accepting events. Write policy is scoped by kind family:

- `5961`-`5968` requests require `nostr.authorized_pubkeys`.
- `696x`, `796x`, `31961`-`31963`, and `310xx` Bahia projections require Bahia's service pubkey.
- `10100`, `30100`, `5101`, `5102`, `5401`, and `5402` interop events accept any valid signature; Bahia services still decide whether to act.

No upstream mirroring is enabled in this scaffold. Do not enable mirroring while Bahia also connects directly to the same upstream relays.
