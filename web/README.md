# Bahia Web Dashboard

A SvelteKit-based web dashboard for Bahia's **Nostr-native deployment and runtime control plane**.

The web app is no longer just a thin REST dashboard. Its current behavior is:
- bootstrap capabilities and relay topology from `Nostr discovery events (kind 31974 + NIP-51 kind 30002)`
- load shared state from relay-backed read models
- publish signed public Nostr requests for many control-plane writes
- use encrypted Nostr request/result flows for sensitive browser operations
- use narrowed REST endpoints only where the product still exposes HTTP compatibility/query surfaces

## Main capabilities

- **Dashboard** — services, environments, drift, recent activity, cost summary
- **Services / Deployments** — registry views, deployment flows, pending approvals, run details
- **Notifications** — encrypted notification channel management and logs
- **LLM** — route, release, deployment, and state views
- **Souls** — Soul Factory gallery, provisioning, and live status
- **Workers / Policies / Payments / Orgs** — supporting operational views and flows

## How the browser talks to Bahia

### 1. Capability bootstrap
The browser first loads `Nostr discovery events (kind 31974 + NIP-51 kind 30002)` to discover:
- browser relay URLs
- service pubkey
- core control-plane kind mappings
- feature flags such as `relay_read_models`, `direct_nostr_http_auth`, and `encrypted_nostr_requests`

The discovery payload is currently a core subset. Broader kind families are documented in `../docs/control-planes.md` and `../docs/nostr-commands.md`.

### 2. Shared state
Shared UI state is primarily loaded from **relay-backed read models**. The app waits for EOSE during bootstrap, then keeps subscriptions open for live updates.

### 3. Public control-plane writes
Many non-sensitive actions are published as **signed Nostr request events** and resolved by correlated result events, not by polling REST for completion.

### 4. Sensitive operations
Sensitive domains such as notifications, payments history, org/member operations, secrets, and similar flows use **encrypted Nostr request/result events** on separate encrypted-request relays when configured.

## Authentication

The web app is **signer-first**.

### Current auth model
- **NIP-07** browser signing is supported
- **NIP-46** (Nostr Connect / bunker) is supported in the browser session flow
- **Direct NIP-98 HTTP auth** is used for compatible REST/MCP requests when the backend advertises it
- The first-party app does **not** rely on JWT session exchange as its primary auth path
- `Authorization: Bearer ...` is a legacy/unsupported compatibility path for Bahia itself and should be rejected by protected Bahia Nostr event contracts when auth is enabled

For encrypted browser flows, signer support must also expose NIP-44 encrypt/decrypt capability.

## Transport surfaces used by the web app

### Primary
- Nostr relay read models discovered from `Nostr discovery events (kind 31974 + NIP-51 kind 30002)`
- Public request/status/result events on the relay sidecar / browser relays
- Encrypted request/result relays for sensitive operations

### Secondary / compatibility
- `Nostr discovery events (kind 31974 + NIP-51 kind 30002)`
- selected REST CRUD/query endpoints
- live log streaming endpoints where applicable

## Development

Use pnpm 10 and Node.js `^22.22.2 || ^24.15.0 || >=26.0.0`. The Node.js
minimum is required by jsdom 30 and isomorphic-dompurify 4; Node 20 is no
longer supported. Commit `pnpm-lock.yaml` with dependency changes.

```bash
cd web
pnpm install --frozen-lockfile
pnpm run dev
```

The dev server proxies `/api` requests to `http://localhost:8080`.

## Quality Gates

```bash
pnpm run lint
pnpm run test:unit
pnpm run build
```

The lint gate runs SvelteKit sync followed by `svelte-check --tsconfig ./tsconfig.json`.

### Joined assistant E2E (opt-in)

`tests/e2e/assistant-unified-execution.spec.js` drives the assistant panel
against a real backend. The normal `pnpm run test:e2e` skips it. To run it:

```bash
pnpm run test:e2e:joined            # extra args go to `playwright test`
```

This needs Go, Docker (for PostgreSQL) and a real `node_modules`, not a
symlinked one. The command builds `cmd/server`,
`cmd/bahia-assistant-e2e-provider` and the dashboard. It then starts
PostgreSQL, `bahia-test-relay`, the deterministic model provider, the backend
and the static dashboard on loopback, runs `playwright.joined.config.js`, and
tears everything down. Harness logs go to
`test-results/assistant-joined-harness/`. Set
`BAHIA_ASSISTANT_E2E_SKIP_DASHBOARD_BUILD=1` to reuse an existing `build/`.

## Svelte 5 Rune Architecture

The dashboard runs with global Svelte 5 rune mode enabled. Components and routes use `$state`, `$derived`, `$effect`, callback props such as `onClose`/`onConfirm`, and DOM event props such as `onclick`.

Shared UI state lives in rune-backed `.svelte.js` modules under `src/lib/stores/`; existing `.js` store entrypoints remain as stable re-export facades for imports.

## Production Build

```bash
npm run build
```

Output is in `web/build/` and can be served by the Go server or another static host.

## Integration with Go Server

The built dashboard can be embedded in the Go server:

```go
//go:embed web/build/*
var dashboardFS embed.FS

// Serve at /dashboard
r.Handle("/dashboard/*", http.StripPrefix("/dashboard",
    http.FileServer(http.FS(dashboardFS))))
```

## Related docs

- `../docs/control-planes.md`
- `../docs/relay-sidecar.md`
- `../docs/web-app-setup.md`
- `../docs/web-testing.md`
- `../docs/web-api-client.md`
