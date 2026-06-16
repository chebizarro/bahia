# Bahia User Guide

Welcome to Bahia — a deployment and runtime control plane that tracks your builds, deploys your containers, and tells you when something goes wrong.

## What is Bahia?

Bahia is a **Nostr-native** deployment platform that:

- **Tracks builds and artifacts** — knows which versions exist and where they came from
- **Manages deployments** — coordinates what should run in which environment
- **Observes runtime state** — detects drift between desired and actual state
- **Records operational truth** — publishes events to Nostr for auditability
- **Provisions AI agents** — manages Soul Factory agent lifecycles (optional)

## Quick Start

```bash
# Start with Docker Compose
docker compose up --build

# Check health
curl http://localhost:8080/health

# Open the web UI
open http://localhost:3000
```

## Documentation Overview

The same documentation corpus is available in three places:

- **Web UI**: Open `/docs` for the browsable catalog, then `/docs/<topic>` for a specific guide such as `/docs/features-services`.
- **Contextual help**: Product routes with matching guides expose a route-specific docs action, and the assistant composer shows a dismissible documentation reference such as `docs:features-services` before you send a prompt.
- **MCP**: AI agents can discover docs with `bahia_docs_list`, read topics with `bahia_docs_read`, or read `bahia://docs/<topic>` resources.

All three paths read from `docs/user-guide/**/*.md`; do not duplicate user-facing docs in route code or assistant prompts.

### Getting Started
- [Getting Started](getting-started.md) — Installation, first deployment, initial setup

### Core Concepts
- [Core Concepts](core-concepts.md) — Services, environments, artifacts, and the Nostr model

### Feature Guides

| Feature | Description |
|---------|-------------|
| [Services](features/services.md) | Create and manage deployable applications |
| [Environments](features/environments.md) | Configure deployment targets (staging, production) |
| [Deployments](features/deployments.md) | Deploy artifacts with intents, approvals, and runs |
| [Artifacts](features/artifacts.md) | Container images and build outputs |
| [Notifications](features/notifications.md) | Alert channels (webhook, email, Slack, Nostr) |
| [Organizations](features/organizations.md) | Team management and access control |
| [LLM Routes](features/llm-routes.md) | Manage and deploy LLM inference endpoints |
| [ML Models](features/ml-models.md) | AI/ML model registry, recipes, and inference |
| [Souls](features/souls.md) | AI agent provisioning with Soul Factory |
| [Workers](features/workers.md) | Loom workers for deployment execution |
| [Fleet Health](features/fleet-health.md) | Resource pressure map and cleanup orchestration status |
| [Backup](features/backup.md) | Backup definitions, policies, and recovery |
| [DNS](features/dns.md) | DNS zone and endpoint management |
| [Packages](features/packages.md) | Package repository management |
| [Policies](features/policies.md) | Deployment approval and SBOM policies |
| [Security](features/security.md) | OSV vulnerability scanning dashboard |
| [Payments](features/payments.md) | Cost estimation and payment history |

### Integration & Reference
- [Nostr Integration](nostr-integration.md) — How Nostr powers the control plane
- [MCP Tools Reference](mcp-tools.md) — Tools available for AI agents
- [CLI Reference](cli-reference.md) — Command-line interface guide
- [Troubleshooting](troubleshooting.md) — Common issues and solutions

## Key Concepts at a Glance

| Term | What it means |
|------|---------------|
| **Service** | An application you deploy |
| **Environment** | A deployment target (staging, production, etc.) |
| **Build** | A CI run that produced deployable output |
| **Artifact** | An immutable container image plus metadata |
| **Deployment Intent** | A request to deploy an artifact |
| **Deployment Run** | A concrete execution of a deployment |
| **Drift** | A mismatch between desired and observed state |
| **Read Model** | Nostr event reflecting current shared state |

## Architecture Overview

```
┌─────────────────┐     ┌──────────────────────────────┐
│ Browser / CLI   │────▶│ ContextVM discovery + NIP-51 │
│ / MCP Agent     │     │ 11316-11320 + relay sets     │
└────────┬────────┘     └──────────────┬───────────────┘
         │                              │
         │ signed requests              │ relay discovery
         ▼                              ▼
┌────────────────────────────────────────────────────────┐
│            Nostr Control Plane / Sidecar               │
│   public requests • status/results • read models       │
└────────────────────────────────────────────────────────┘
                          │
         ┌────────────────┼────────────────┐
         ▼                ▼                ▼
    PostgreSQL      OCI / Blossom    Loom Workers
```

## Getting Help

- **Web UI**: Access the dashboard at `http://localhost:3000`
- **Docs UI**: Browse documentation at `http://localhost:3000/docs`; internal documentation links stay inside `/docs/<topic>`.
- **Assistant**: Open the floating assistant on a mapped product route to include a visible, dismissible route docs reference in `selected_refs`.
- **MCP**: Connect to `/mcp` or `/api/v1/mcp` for AI agent tooling, including `bahia_docs_list` and `bahia_docs_read`.
- **Nostr**: Subscribe to read models and status events
- **API Docs**: See [api.md](../api.md) for HTTP reference

---

*This documentation is designed for both human users (via web) and AI agents (via MCP).*
