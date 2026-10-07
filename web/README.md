# Bahia Web

The SvelteKit 5 web app for Bahia. It is a static bundle that talks to Nostr
relays: it reads canonical state through REQ subscriptions into an IndexedDB
event store, renders from that store, and writes by signing `30900` intents
with the operator's NIP-07 or NIP-46 signer. It has no API client; the only
HTTP it uses is the daemon's Blossom blob proxy and the maintenance-window
route.

## Pages

Dashboard (drift, cost, activity), services, environments and environment
states, deployments, builds, artifacts, policies, workers, DNS, LLM routes,
ML models, packages, backups, security, route canaries, fleet health and
instance health, continuity, notifications, payments, organizations,
secrets, config fabric, souls (Soul Factory), widgets, events, settings
(relays, versions, profile, fleet) and the published docs at `/docs`.
Feature pages render when the daemon advertises the feature in discovery.

## Development

Node `^22.22.2 || ^24.15.0 || >=26.0.0`, pnpm 10. Commit `pnpm-lock.yaml`
with dependency changes.

```bash
cd web
pnpm install --frozen-lockfile
PUBLIC_BAHIA_BOOTSTRAP_RELAYS=ws://localhost:3334/relay \
PUBLIC_BAHIA_SERVICE_PUBKEYS=<service-pubkey-hex> \
pnpm run dev                    # http://localhost:5173, /api proxied to :8080
```

Quality gates:

```bash
pnpm run lint                   # svelte-kit sync + svelte-check
pnpm run test:unit
pnpm run build                  # static output in build/
```

`pnpm run test:e2e` runs the Playwright suites against in-process relay
harnesses; `pnpm run test:e2e:joined` runs the assistant suite against a real
daemon. See [web testing](../docs/web-testing.md).

## Production

`Dockerfile` builds the bundle and serves it with nginx (`nginx.conf`
proxies `/relay` to the sidecar). The image carries no trust roots: set
`PUBLIC_BAHIA_BOOTSTRAP_RELAYS` and `PUBLIC_BAHIA_SERVICE_PUBKEYS` (and
optionally `PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS`) on the container; the
entrypoint validates them and writes the bootstrap seed before nginx starts.
See [web app setup](../docs/web-app-setup.md).

## Code layout

- `src/lib/nostr/` — event store, relay pool, subscriptions, discovery,
  intent client, gift wrap, outbox, NIP-07/NIP-46 signers, relay auth,
  confidential-record decryption, generated kind constants (`kinds.gen.js`).
- `src/lib/stores/` — rune-backed `.svelte.js` stores (auth, discovery,
  control plane, collections, intents, orgs, secrets, notifications, …).
  `.js` entrypoints re-export them for stable imports.
- `src/lib/components/` — reusable components
  ([reference](../docs/web-components.md)).
- `src/routes/` — pages; `src/routes/docs` renders the published user guide.
- `tests/unit`, `tests/e2e`, `tests/fixtures` (Go-generated intent fixtures).

Svelte 5 rune mode is on globally: components use `$state`, `$derived`,
`$effect`, callback props and DOM event props. Stores must not poll
(`setInterval`) or call HTTP; `tests/unit/architecture-gates.test.js`
enforces this.

## Related docs

- [Control planes](../docs/control-planes.md)
- [Event specification](../docs/event-spec.md)
- [Relay sidecar](../docs/relay-sidecar.md)
- [Web store-first design](../docs/architecture/web-store-first.md)
