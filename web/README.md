# Bahia Web Dashboard

A SvelteKit-based web dashboard for Bahia's **Nostr-native deployment and runtime control plane**.

The web app is no longer just a thin REST dashboard. Its current behavior is:
- bootstrap capabilities from the ContextVM server announcement (`11316`) and relay topology from NIP-51 relay sets (`30002`)
- load shared state from relay-backed read models
- publish signed public Nostr requests for many control-plane writes
- use encrypted Nostr request/result flows for sensitive browser operations
- use narrowed REST endpoints only where the product still exposes HTTP compatibility/query surfaces

## Main capabilities

Each item maps to a route directory under `src/routes/`.

- **Dashboard** (`/`): services, environments, drift, recent activity, cost summary
- **Services / Deployments / Environments** (`/services`, `/deployments`, `/deployments/pending`, `/deployments/runs`, `/environments`, `/environment-states`): registry views, deployment flows, pending approvals, run details
- **Artifacts / Builds / Packages / Security** (`/artifacts`, `/builds`, `/packages`, `/security`): artifact, SBOM, package, and scan views
- **Health and operations** (`/instance-health`, `/fleet-health`, `/workers`, `/continuity`, `/backup`, `/config-fabric`, `/dns`, `/events`): managed-instance health and maintenance, worker fleet, continuity, backups, config fabric, DNS
- **Notifications** (`/notifications`): encrypted notification channel management and logs
- **LLM / ML** (`/llm`, `/ml`): route, release, deployment, and state views
- **Souls** (`/souls`, `/souls/new`, `/souls/[id]`, `/souls/[id]/edit`): Soul Factory gallery, provisioning, editing, and live status
- **Policies / Payments / Orgs / Settings** (`/policies`, `/payments`, `/orgs`, `/settings/{profile,relays,fleet}`): supporting operational views and flows
- **Docs / Widgets** (`/docs`, `/widgets`): in-app docs and Wheelhouse ops widgets
- **Operator assistant**: the `AssistantChat` panel mounted in the root layout on every route

## How the browser talks to Bahia

### 1. Capability bootstrap
The browser first loads the ContextVM server announcement (`11316`, `d=bahia-system-v1`) plus NIP-51 relay sets (`30002`) to discover:
- browser relay URLs
- service pubkey
- core control-plane kind mappings
- feature flags such as `relay_sidecar`, `relay_read_models`, `direct_nostr_http_auth`, `encrypted_nostr_requests`, `llm_control_plane`, and `mcp_transport`

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
- Nostr relay read models discovered from ContextVM `11316` and NIP-51 `30002` events
- Public request/status/result events on the relay sidecar / browser relays
- Encrypted request/result relays for sensitive operations

### Secondary / compatibility
- ContextVM discovery events (`11316`-`11320`) and NIP-51 relay sets (`30002`)
- selected REST CRUD/query endpoints
- live log streaming endpoints where applicable

## Development

```bash
cd web
pnpm install
pnpm dev
```

The dev server proxies `/api` requests to `http://localhost:8080`. Requires Node `^20.19.0` or `>=22.12.0`. Discovery bootstrap is configured with `PUBLIC_BAHIA_BOOTSTRAP_RELAYS` and `PUBLIC_BAHIA_SERVICE_PUBKEYS`; see [web-app-setup](../docs/web-app-setup.md) for the full variable list.

`pnpm-lock.yaml` is the CI lockfile. `web/Dockerfile` still installs with `npm` from `package-lock.json`, so keep both lockfiles in sync when you change dependencies.

## Quality Gates

```bash
pnpm lint          # svelte-kit sync + svelte-check --tsconfig ./tsconfig.json
pnpm test:unit     # Vitest (jsdom), tests/unit/**
pnpm test:e2e      # Playwright against pnpm dev on 127.0.0.1:4173
```

See [web-testing](../docs/web-testing.md) for coverage targets and harnesses.

## Svelte 5 Rune Architecture

The dashboard runs on Svelte 5. `svelte.config.js` does not force rune mode globally, so each component's mode is inferred from its code. Rune components use `$state`, `$derived`, `$effect`, `$props()`, callback props such as `onClose`/`onConfirm`, and DOM event props such as `onclick`. Many legacy components still opt out with `<svelte:options runes={false} />`; follow each component's actual contract (see [web-components](../docs/web-components.md)).

Shared UI state lives in rune-backed `.svelte.js` modules under `src/lib/stores/`; existing `.js` store entrypoints remain as stable re-export facades for imports.

## Production Build

```bash
pnpm build
```

Output is in `web/build/` (static adapter with an `index.html` SPA fallback) and can be served by any static host.

## Production serving

The checked-in `web/Dockerfile` builds the static SvelteKit output and copies `web/build/` into an nginx runtime image. `web/nginx.conf` serves the SPA and proxies `/api/` and `/relay` to their backend services; the Go server does not embed the dashboard.

## Related docs

- `../docs/control-planes.md`
- `../docs/relay-sidecar.md`
- `../docs/web-app-setup.md`
- `../docs/web-testing.md`
- `../docs/web-api-client.md`
