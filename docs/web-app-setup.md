# Bahia Web App Setup

The web app (`web/`) is a static SvelteKit 5 application. It has no backend
of its own: it connects to Nostr relays, hydrates an IndexedDB event store
from REQ subscriptions, signs intents with the operator's signer and
publishes them to the relays. The only HTTP it uses is the daemon's Blossom
blob proxy and the two operator maintenance routes listed in the
[HTTP reference](api.md). Design: [web store-first](architecture/web-store-first.md).

## Prerequisites

- Node.js `^22.22.2 || ^24.15.0 || >=26.0.0` and pnpm 10 (`web/package.json`
  `engines`; the lockfile is authoritative).
- A reachable relay sidecar (`bahia-relay`) and a running daemon publishing
  to it.
- A signer: a NIP-07 extension (nos2x, Alby, Nostore) or a NIP-46 bunker. For
  encrypted flows the signer must expose NIP-44 (`window.nostr.nip44.*` or
  the NIP-46 provider's `nip44.encrypt/decrypt`); without it the app shows
  the exact blocker instead of falling back to plaintext.

## Running

```bash
cd web
pnpm install --frozen-lockfile
PUBLIC_BAHIA_BOOTSTRAP_RELAYS=ws://localhost:3334/relay \
PUBLIC_BAHIA_SERVICE_PUBKEYS=<service-pubkey-hex> \
pnpm dev                      # http://localhost:5173
```

`vite.config.js` proxies `/api` to `http://localhost:8080` for the few HTTP
routes. The relay URL is dialed directly by the browser.

```bash
pnpm build                    # static output in web/build/
pnpm preview                  # http://localhost:4173
```

The container image (`web/Dockerfile`) serves `build/` with nginx, which
also proxies `/relay` to the sidecar (`web/nginx.conf`).

## Runtime bootstrap seed

The image contains no relay URLs or trust roots. At container start
`docker-entrypoint.d/40-bahia-bootstrap-env.sh` validates the runtime
environment and writes `/bahia-bootstrap.js`
(`window.__BAHIA_BOOTSTRAP__ = {schema:"bahia.bootstrap.v1", relay_urls,
service_pubkeys, widget_pubkeys}`), served with `no-store` and loaded before
the app. Startup fails if a value is missing or malformed, so a bad restart
never leaves a partial seed. Rotating a trust root is a container restart,
not a rebuild.

| Variable | Purpose |
|---|---|
| `PUBLIC_BAHIA_BOOTSTRAP_RELAYS` | Comma-separated `ws://`/`wss://` relay URLs the app dials first (required at runtime) |
| `PUBLIC_BAHIA_SERVICE_PUBKEYS` | Comma-separated 64-hex service pubkeys whose discovery, state and published documentation the app trusts (required) |
| `PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS` | Optional comma-separated 64-hex pubkeys allowed to publish ops widgets (kind `30318`); unset denies all widgets |
| `PUBLIC_BAHIA_WEB_BASE_VERSION`, `PUBLIC_BAHIA_GIT_COMMIT`, `PUBLIC_BAHIA_WEB_VERSION` | Build-time version metadata shown under Settings → Build information |

`docker-compose.yml` forwards the three runtime variables from the shell
environment; the edge deploy workflow (`.github/workflows/deploy-edge.yml`)
injects the same entries into the host Compose file. With `pnpm dev` or
`pnpm preview` and no injected seed, the same variables are read at build
time as a development fallback.

## How the app reads and writes

1. **Discovery.** From the seed relays the app REQs `11316` and the `30002`
   relay sets authored by a trusted service pubkey, learns the browser and
   ContextVM relays, feature flags and advertised capabilities
   (`$lib/stores/discovery.svelte.js`).
2. **Store-first hydration.** Every collection renders from the IndexedDB
   event store (`$lib/nostr/store.js`) immediately, then subscribes with
   `authors` + `#t` filters through the welshman-based pool
   (`$lib/nostr/pool-welshman.js`). Stored events arrive until `EOSE`; the
   subscription stays open; a `CLOSED` or dropped connection is reissued
   with capped jittered backoff from the last-seen cursor. Large families use
   paged backfill (`$lib/nostr/store-first-backfill.js`).
3. **Protected topics.** Public families hydrate before sign-in. After the
   operator signs in the pool answers the sidecar's NIP-42 challenge with
   the session signer (`$lib/nostr/relay-auth-signer.js`); a pubkey the
   sidecar does not admit sees `restricted:` in the connection status and
   only the public models.
4. **Writes.** Forms build the full desired state, mint a UUIDv7 id
   (`$lib/entity-id.js`), sign a `30900` intent (`$lib/nostr/intent-client.svelte.js`),
   gift-wrap it for sensitive domains (`$lib/nostr/intent-giftwrap.js`),
   publish it through the browser outbox (`$lib/nostr/outbox.js`) and wait
   for the requester-scoped `30315` status. Pending intents are shown in
   `PendingDomainIntents` until the status or the canonical record arrives.
5. **Confidential records.** Org and fleet content keys are trial-decrypted
   from `org-key-envelope` records with the signer's NIP-44; encrypted
   families (`$lib/nostr/confidential.js`) decrypt locally.
6. **Interactive RPC.** Secret reveal and assistant turns use ContextVM
   `25910` inside gift wraps (`$lib/nostr/encrypted-controlplane*.js`).

The connection indicator (`ConnectionStatus`) shows relay count, last event,
last `EOSE`, errors, auth state and a manual retry.

## Authentication

The app is signer-first. Sign-in establishes a signer session (NIP-07 or
NIP-46; `$lib/stores/auth.svelte.js`); the pubkey is the identity used for
intents, relay AUTH and the NIP-98 header on the few HTTP routes
(`signHttpRequest`). Organization roles come from the decrypted `org-member`
records (`$lib/stores/auth-roles.svelte.js`). There is no token exchange and
nothing is stored beyond the signer session.

## Settings

- **Relays**: the operator relay policy (`relay-settings:operator` record)
  and the per-session relay list.
- **Versions**: the daemon's signed `observed_deployments` projection — each
  row is an environment-service state joined with its runtime observation
  (names, target, observed version or digest, host, health, drift, time).
- **Build information**: the web artifact version and the backend's
  packaged artifact catalog; not evidence of what is deployed.

## Troubleshooting

| Symptom | Check |
|---|---|
| Blank app, console `bahia-web bootstrap env missing` | Set `PUBLIC_BAHIA_BOOTSTRAP_RELAYS` and `PUBLIC_BAHIA_SERVICE_PUBKEYS` on the container |
| "No trusted discovery" | The seed pubkeys do not match `nostr.private_key` of the daemon, or the daemon has no `nostr.browser_relays` so it never published `11316`/`30002` |
| Connection status `auth-required` / `restricted` | Sign in; if still restricted, the pubkey must be admitted by the sidecar (org member, fleet operator, or `read_auth_allowed_pubkeys`) |
| Intent stays pending | Watch the sidecar `OK` in the outbox panel; a `rejected` status carries the reason; the signer must be a member of the `org` the intent names |
| Encrypted action blocked | The signer lacks NIP-44; switch to a signer that exposes it |
| Stale data after a daemon restart | The store renders what it has; the subscription resumes from its cursor once relays reconnect — use the connection indicator's retry |
| `pnpm dev` fails on Node 20 | Upgrade Node (jsdom 30 and isomorphic-dompurify 4 need 22+) |

## Browser support

Current Chrome, Firefox, Safari and Edge. IndexedDB and WebSocket are
required; NIP-07 depends on the extension's platform support (iOS Safari
through Nostore).
