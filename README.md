# Bahia

![bahia logo](docs/assets/logo.png)

## What is Bahia?

**Bahia tracks your builds, deploys your containers, and tells you when something goes wrong.**

At its core, Bahia is a **deployment and runtime control plane**. It:
- Tracks which builds and artifacts exist
- Knows which versions should be running in which environments
- Executes deployments and rollbacks
- Observes runtime state and detects drift
- Records operational truth to Nostr
- Optionally provisions and manages Soul Factory agents

## Current product shape

Bahia's **purpose** is still deployment/runtime control, but the **current interaction model** is Nostr-native:

- **Nostr-native** — Nostr is a primary control-plane transport, not just an audit log
- **Sidecar-first** — the relay sidecar is the main realtime/public event boundary for browser and backend control-plane traffic
- **Signer-first** — operators and web users sign actions with Nostr identities (for example NIP-07 or NIP-46)
- **Relay-backed read models** — the web app bootstraps shared state from relay subscriptions and replaceable events
- **Encrypted sensitive flows** — notifications, payments history, orgs, secrets, run logs, and similar sensitive operations use encrypted Nostr request/result events
- **Narrowed HTTP compatibility surfaces** — REST and MCP still exist, but they are no longer the whole product story

If you are integrating with Bahia, start with:
- [`docs/user-guide/index.md`](docs/user-guide/index.md)
- [`docs/control-planes.md`](docs/control-planes.md)
- [`docs/relay-sidecar.md`](docs/relay-sidecar.md)
- [`docs/nostr-commands.md`](docs/nostr-commands.md)
- [`docs/event-spec.md`](docs/event-spec.md)

---

## How It Works

```text
You push code → CI builds it → Bahia registers build/artifact state →
You request a deployment → Bahia coordinates execution →
Bahia observes runtime state → Bahia publishes status, results, and read models to Nostr
```

**The main pieces:**
- **Hive-CI / external CI** — Produces workflow/build events and artifacts
- **Bahia** — Maintains deployment/runtime state and coordinates actions
- **OCI registry / Harbor-compatible image sources** — Stores container images
- **Loom workers / direct runtime targets** — Execute deployments or host workloads
- **PostgreSQL** — Stores canonical persisted state
- **Nostr relays / relay sidecar** — Carry public control-plane events, status/results, and read models
- **Encrypted request relays** — Carry sensitive encrypted Nostr request/result traffic where configured

## Architecture at a glance

```text
┌───────────────┐      ┌──────────────────────┐
│ Browser / CLI │─────▶│ ContextVM discovery (11316-11320) + NIP-51 30002 │
│ / MCP client  │      │   capability bootstrap│
└──────┬────────┘      └──────────┬───────────┘
       │                           │
       │ signed requests           │ relay + feature discovery
       ▼                           ▼
┌───────────────────────────────────────────────┐
│         Nostr control plane / sidecar         │
│  public requests • status/results • read models│
└──────────┬───────────────────────┬────────────┘
           │                       │
           ▼                       ▼
   ┌───────────────┐       ┌──────────────────┐
   │    Bahia      │       │ encrypted relays │
   │ reactor/router│       │ sensitive flows  │
   └──────┬────────┘       └──────────────────┘
          │
          ├──────────────▶ PostgreSQL
          ├──────────────▶ OCI / Blossom
          ├──────────────▶ Loom / runtime targets
          └──────────────▶ audit + projections back to relays
```

For a fuller architectural description, see [`docs/architecture.md`](docs/architecture.md).

## Current Status

- ✅ Service, environment, build, and artifact registration
- ✅ Deployment intents, approvals, execution, and rollback workflows
- ✅ Runtime observation and drift detection (Docker, Podman, Compose, Kubernetes)
- ✅ Nostr-native control plane with canonical request/status/result/read-model kinds
- ✅ Sidecar-first relay discovery via ContextVM discovery (`11316`-`11320`) plus NIP-51 relay sets (`30002`)
- ✅ Durable relay history with bounded replay/retention and a retrying outbound Nostr publish outbox
- ✅ Signer-first browser auth, NIP-46 CLI operator signing, and direct NIP-98 HTTP compatibility auth
- ✅ Encrypted Nostr request/result flows for sensitive domains
- ✅ PostgreSQL persistence
- ✅ Native MCP transport at `/mcp` and `/api/v1/mcp`
- ✅ Web UI for services, deployments, notifications, ML, workers, fleet health, and more
- ✅ Feature-gated web surfaces for LLM routes and Souls when those domains are enabled
- ✅ OCI Registry Server backed by PostgreSQL + Blossom
- ✅ Hive-CI Bridge for auto-ingesting workflow events into build/artifact state
- ✅ Soul Factory agent provisioning when enabled (feature-gated and disabled by default)

See [`docs/control-planes.md`](docs/control-planes.md) for the current product transport contract.

## Quick Start

```bash
# Start with Docker Compose (includes PostgreSQL, API server, and Web UI)
docker compose up --build

# API health
curl http://localhost:8080/health

# Browser UI
open http://localhost:3000
```

## Development

```bash
# Prerequisites: Go 1.24+, PostgreSQL 16+

# Install dependencies
make deps

# Run locally
make run-dev

# Run tests
make test

# Build binaries
make build
```

## Control planes

Bahia currently exposes three main control-plane surfaces:

1. **Nostr relay sidecar** — primary async/realtime plane for shared browser state, operator requests, status/results, and read models
2. **Native MCP** — JSON-RPC tools over HTTP at `/mcp` and `/api/v1/mcp`
3. **REST API** — narrowed CRUD/query/log/compatibility surface

Important: the web app's shared state is **not** primarily a REST polling client. It bootstraps from ContextVM discovery (`11316`-`11320`) plus NIP-51 relay sets (`30002`), connects to relays, waits for EOSE on read models, and then stays live on subscriptions.

Also note: ContextVM discovery (`11316`-`11320`) plus NIP-51 relay sets (`30002`) expose the core control-plane discovery map. Broader kind families are documented in `docs/control-planes.md` and `docs/nostr-commands.md`.

## Key Nostr event contracts

Full HTTP reference: [`docs/api.md`](docs/api.md)

| Endpoint | Description |
|----------|-------------|
| `ContextVM discovery (11316-11320) + NIP-51 relay sets (30002)` | Capability + relay/bootstrap discovery (core kind map; broader families documented separately) |
| `POST /mcp` | Native MCP JSON-RPC endpoint |
| `POST /api/v1/services` | Create a service (REST compatibility surface) |
| `POST /api/v1/deployments/intents` | Create deployment intent |
| `service/rollback` over ContextVM kind `25910` | Create signer-first rollback intent |
| `GET /api/v1/state/drifted` | List drifted service state |
| `GET /v2/` | OCI Distribution API |

## CLI

```bash
bahia services list
bahia environments list
bahia state list
bahia state drifted
bahia deployments deploy --service <id> --environment <id> --artifact <id>
bahia deployments rollback --service <id> --environment <id> --deployment-unit <unit-id> --target-artifact <previous-artifact-id> --supersedes-intent <current-intent-id>
```

Some operator flows are signer-first and relay-driven. See:
- [`docs/adoption-production-rollout.md`](docs/adoption-production-rollout.md)
- [`docs/nostr-commands.md`](docs/nostr-commands.md)

## Key Concepts

| Term | What it means |
|------|---------------|
| **Service** | An application you deploy |
| **Environment** | A target deployment context such as staging or production |
| **Build** | A CI run that produced deployable output |
| **Artifact** | An immutable container image plus metadata |
| **Deployment Intent** | A request to deploy an artifact |
| **Deployment Run** | A concrete execution attempt |
| **Runtime Observation** | A snapshot of what is actually running |
| **Drift** | A mismatch between desired and observed state |
| **Read Model** | Relay-published replaceable event that reflects current shared UI state |

## Documentation

### Start here
- [User Guide](docs/user-guide/index.md) — task-oriented product documentation
- [Control Planes](docs/control-planes.md) — current transport and control-plane contract
- [Relay Sidecar](docs/relay-sidecar.md) — sidecar topology and boundaries
- [Nostr Commands](docs/nostr-commands.md) — canonical Nostr request kinds
- [Event Specification](docs/event-spec.md) — event kinds and payloads

### Core product docs
- [Architecture](docs/architecture.md) — how Bahia is structured
- [API Reference](docs/api.md) — HTTP compatibility/query surface
- [Deployment Guide](docs/deployment.md) — how to run Bahia
- [Soul Factory](docs/soul-factory.md) — AI agent provisioning

### Operational docs
- [Adoption Production Rollout](docs/adoption-production-rollout.md) — signer-first adoption/import + direct-runtime rollout
- [Protocol Compatibility](docs/protocol-compatibility.md) — protocol support status and compatibility notes
