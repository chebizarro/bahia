# Bahia Web Dashboard

A SvelteKit-based web dashboard for the Bahia Deployment Registry.

## Features

- **Dashboard**: Overview of services, environments, drift status
- **Services**: List and detail views with builds, artifacts, secrets
- **Environments**: List all deployment environments
- **Workers**: View available Loom workers and pricing
- **Policies**: Manage deployment policies
- **Events**: Real-time event stream via SSE

## Development

```bash
cd web
npm install
npm run dev
```

The dev server proxies `/api` requests to `http://localhost:8080`.

## Svelte 5 Rune Architecture

The dashboard runs with global Svelte 5 rune mode enabled. Components and routes use `$state`, `$derived`, `$effect`, callback props such as `onClose`/`onConfirm`, and DOM event props such as `onclick`.

Shared UI state lives in rune-backed `.svelte.js` modules under `src/lib/stores/`; existing `.js` store entrypoints remain as stable re-export facades for imports.

## Production Build

```bash
npm run build
```

Output is in `web/build/` - static files that can be served by the Go server or any static host.

## Integration with Go Server

The built dashboard can be embedded in the Go server:

```go
//go:embed web/build/*
var dashboardFS embed.FS

// Serve at /dashboard
r.Handle("/dashboard/*", http.StripPrefix("/dashboard", 
    http.FileServer(http.FS(dashboardFS))))
```

## Authentication

Currently uses JWT tokens stored in localStorage. Future versions will support:
- NIP-07 (browser extension signing)
- NIP-46 (Nostr Connect remote signing)

## API Endpoints Used

- `GET /api/v1/services` - List services
- `GET /api/v1/environments` - List environments
- `GET /api/v1/state` - List deployment states
- `GET /api/v1/workers` - List Loom workers
- `GET /api/v1/policies` - List deployment policies
- `GET /api/v1/events/stream` - SSE event stream
- `GET /api/v1/services/{id}/environments/{envId}/logs?follow=true` - Live log stream
