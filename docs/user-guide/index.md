# Bahia User Guide

Bahia is a Nostr-native deployment and runtime control plane. It tracks governed builds and artifacts, coordinates desired state, observes runtimes, detects drift, and publishes signed operational truth.

## Quick start

```bash
docker compose up --build
curl http://localhost:8080/health
open http://localhost:3000
```

The web deployment requires `PUBLIC_BAHIA_BOOTSTRAP_RELAYS` and `PUBLIC_BAHIA_SERVICE_PUBKEYS` so the browser can discover relays and trust the Bahia signer. See [Getting Started](getting-started.md) for configuration and a first deployment.

## Online documentation

The same catalog is available to people and agents:

- **Web:** `/docs` lists topics; `/docs/<topic>` reads one topic.
- **Contextual help:** product routes with a mapped guide add a visible documentation reference.
- **MCP:** `bahia_docs_list` and `bahia_docs_read`, plus `bahia://docs/<topic>` resources.

`internal/docs` publishes `docs/user-guide/**/*.md` to the relays as kind `30023` with `t=bahia-docs`. Only links to another file in this tree resolve as internal online documentation.

## Start here

- [Getting Started](getting-started.md) — install, configure, and deploy
- [Core Concepts](core-concepts.md) — entities, lifecycle, events, and authorization
- [CLI Reference](cli-reference.md) — terminal commands and flags
- [MCP Tools](mcp-tools.md) — agent tools, arguments, authorization, and completion
- [Nostr Integration](nostr-integration.md) — events, relays, encryption, outbox, and HTTP
- [Troubleshooting](troubleshooting.md) — readiness, relays, signers, and operations

## Feature guides

| Guide | Product surface |
|---|---|
| [Organizations](features/organizations.md) | Tenancy, roles, OCK encryption, and membership |
| [Services](features/services.md) | Deployable applications, secrets, runtime actions, and routes |
| [Adoption](features/adoption.md) | Scan and import running containers |
| [Builds](features/builds.md) | Governed Hive-CI build requests and results |
| [Artifacts](features/artifacts.md) | Images, provenance, SBOMs, signatures, and scans |
| [Environments](features/environments.md) | Deployment units, strategy, reconciliation, and protection |
| [Deployments](features/deployments.md) | Preview, approval, execution, logs, rollback, and observation |
| [Environment States](features/environment-states.md) | Desired-versus-observed state and drift |
| [Instance Health](features/instance-health.md) | Runtime supervision, recovery history, and maintenance |
| [Workers](features/workers.md) | Execution capability, scheduling, lifecycle, and cleanup |
| [Fleet Health](features/fleet-health.md) | Pressure, cleanup, and fleet operational state |
| [Route Canaries](features/route-canaries.md) | End-to-end route verification and outage state |
| [DNS](features/dns.md) | Zones, endpoints, projection, overrides, drift, and mesh |
| [Packages](features/packages.md) | Repositories, upload, promotion, yank, and drift |
| [Tool Provisioning](features/tool-provisioning.md) | Signed approval boundary and suspended execution |
| [Policies](features/policies.md) | Deployment evidence and enforcement |
| [Config Fabric](features/config-fabric.md) | Desired/effective service configuration and rollback |
| [Security](features/security.md) | SBOM vulnerability scanning and policy evidence |
| [Notifications](features/notifications.md) | Channels, encrypted configuration, and delivery log |
| [Backup](features/backup.md) | Backup, verification, restore, and retention |
| [Continuity](features/continuity.md) | Placement, readiness, and failover simulation |
| [Payments](features/payments.md) | Pricing, estimates, run cost, and payment history |
| [ML Models](features/ml-models.md) | Model registry, recipes, provenance, and endpoints |
| [LLM Routes](features/llm-routes.md) | Model-serving routes, releases, deployment, and rollback |
| [Virtual Machines](features/virtual-machines.md) | Persistent VMs and execution planes |
| [Souls](features/souls.md) | Soul Factory provisioning and lifecycle |
| [Operator Assistant](features/operator-assistant.md) | In-product planning, approval, and MCP execution |
| [Ops Widgets](features/ops-widgets.md) | Trusted live operations widgets |
| [Events](features/events.md) | Live inspection of verified control-plane events |
| [Settings](features/settings.md) | Signer, profile, relays, discovery, and fleet configuration |

## Operator guides

- [Managed DNS and HTTPS Routes](guides/managed-dns-and-https-routes.md) — deploy a service, project DNS, and attach a managed hostname

## Core flow

```text
operator signs intent ──▶ relay ──▶ daemon validates and applies
                                      │
                                      ├─▶ 30315 intent status
                                      ├─▶ 30900 canonical state
                                      └─▶ 4903 audit facts

runtime or worker ──▶ authenticated observation ──▶ desired/observed state and drift
```

Relay `OK` means publication, not completion. Follow intent status and canonical outcome records.

## Data model at a glance

| Term | Meaning |
|---|---|
| Service | An organization-owned deployable application |
| Environment | A deployment target with one or more units |
| Build | A governed CI execution |
| Artifact | An immutable image and provenance record |
| Deployment intent | Reviewed desired artifact and target |
| Deployment run | Concrete execution of an approved intent |
| Service state | Desired and observed runtime state |
| Drift | A mismatch or insufficient observation |
| Canonical record | Service-signed addressable state |
| Intent | Operator-signed desired change |

The daemon can run without PostgreSQL. When configured, PostgreSQL is a derived index for specific HTTP and operational features; relays and the daemon's local event store remain the state path.
