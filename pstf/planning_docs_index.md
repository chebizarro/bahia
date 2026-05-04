# Bahia Planning Docs Index

## Reading guide

### Freshness
- **current**: aligns well with selected code
- **mixed**: partly current, partly outdated or incomplete
- **stale**: materially behind current implementation
- **historical**: useful background, not reliable as a current contract

### Confidence
- **high**: selected code strongly supports the document’s role and currentness
- **medium**: some alignment, but important omissions or drift
- **low**: aspirational, inconsistent, or contradicted

## Index

| Path | Apparent purpose | Freshness / staleness indicators | Features mentioned | Conflicts with code or other docs | Confidence |
|---|---|---|---|---|---|
| `docs/investigations/pstf-discovery-2026-05-03.md` | Investigation synthesis for PSTF mapping | Current; grounded in code/doc/test references | product purpose, flows, doc authority, gaps, slices | synthesis document, not normative spec | high |
| `docs/control-planes.md` | Primary narrative for control-plane topology and transport semantics | Current; matches sidecar-first, signer-first, encrypted request/result, and 311xx deprecation seen in code | public request/result/read-model families, encrypted flows, adoption/direct-runtime, LLM kinds, relay topology | does not fully enumerate newer public write kinds used in current web helpers | high |
| `docs/adoption-production-rollout.md` | Operational runbook for signer-first adoption/import and direct-runtime rollout | Current; aligns with router/handler/operator-action behavior | operator pubkeys, endpoint refs, fallback rules, evidence capture, staged rollout | operational runbook, not full product spec | high |
| `docs/relay-sidecar.md` | Sidecar topology and policy description | Current; consistent with sidecar-first architecture and recent rehearsal fixes | public browser relays, sidecar/backend URLs, event policy, mirrored kinds | narrow scope; not a complete product behavior doc | high |
| `docs/nostr-commands.md` | Public command/status/result/read-model reference | Mixed; consistent on canonical `596x` and 311xx deprecation, but incomplete versus current families | public request kinds, status/result kinds, read models, 311xx deprecation | omits adoption `5978/5979`, direct-runtime `6963`, encrypted `5980/7980`, and newer write kinds used by web | medium |
| `docs/api.md` | REST API reference | Mixed; describes major REST surfaces, but misses current discovery and some mounted domains | services, environments, builds, artifacts, deployments, rollback, adoption, direct-runtime, OCI | omits `/api/v1/system/info`, `/mcp`, several mounted domains (notifications, secrets, orgs, some payments/LLM routes) | medium |
| `docs/deployment.md` | Runtime/deployment configuration and rollout guide | Mixed; useful runtime model, but examples look broader than the selected root config | runtime endpoints, compose, OCI, Hive-CI, adoption/deploy guidance | settings discussed here do not line up cleanly with selected `config.yaml` example | medium |
| `docs/event-spec.md` | Nostr event taxonomy and data-shape guide | Mixed; good for audit/LLM/Hive-CI, incomplete for newer encrypted/operator families | audit events, Loom, LLM, Hive-CI | does not document encrypted `5980/7980` or adoption/direct-runtime families from current control-plane docs | medium |
| `docs/protocol-compatibility.md` | Cross-protocol compatibility/status matrix | Mixed to stale; contains current and historical material together | Loom, NIP-98, NIP-46, Cashu, Blossom, Hive-CI, command families | internally inconsistent on NIP-46; still presents `31100-31105` as implemented while newer docs deprecate them; soul lifecycle status also conflicts | low |
| `README.md` | Product positioning and quick-start overview | Mixed to stale; still accurate on original deployment-control-plane core but behind current control-plane shape | deploy registry, Loom, drift, Soul Factory, OCI registry, Hive-CI | omits sidecar-first topology, encrypted browser flows, MCP prominence, adoption/direct-runtime, `/system/info` importance | medium |
| `docs/architecture.md` | High-level system architecture explanation | Stale; REST/service-layer framing lags the current relay/control-plane architecture | REST API, service layer, OCI registry, Hive-CI bridge, reconciler | omits sidecar-first relay model, encrypted routes, signer-first browser flows, adoption/direct-runtime, newer control-plane framing | low |
| `docs/soul-factory.md` | Soul Factory narrative and CLI/MCP story | Mixed / aspirational; detailed, but selected proof does not confirm every lifecycle claim | event kinds, CLI flows, provisioning steps, tiers, gallery UI, lifecycle actions | conflicts with `docs/protocol-compatibility.md`, which still lists Soul Lifecycle as an incomplete known gap | medium |
| `docs/web-testing.md` | Web testing strategy and conventions | Mixed; useful process guidance, but broader coverage claims exceed selected proof | Vitest, Playwright, patterns, coverage goals | selected tests only prove a subset of the stated breadth | medium |
| `config.yaml` | Example local/runtime config surface | Mixed; reflects part of the current contract but not full feature surface | server/db, harbor, loom, relays, encrypted-request relays, auth, adoption, direct-runtime | narrower than deployment/control-plane docs; should be treated as example config, not full contract | medium |

## Highest-authority sources for PSTF onboarding

1. `internal/domain/models.go`
2. `internal/domain/llm.go`
3. `internal/domain/soul.go`
4. `internal/api/handlers/system.go`
5. `internal/controlplane/encrypted_transport.go`
6. `internal/controlplane/operator_actions.go`
7. `internal/api/router/router.go`
8. `web/src/lib/stores/controlplane.svelte.js`
9. `docs/control-planes.md`
10. `docs/adoption-production-rollout.md`

## Key conflicts to keep visible

| Conflict | Sources |
|---|---|
| NIP-46 is called both “stubbed” and effectively implemented | `docs/protocol-compatibility.md` |
| `311xx` command bridge is listed as implemented in one doc but deprecated/quarantined in newer docs | `docs/protocol-compatibility.md`, `docs/control-planes.md`, `docs/nostr-commands.md` |
| Product is framed as REST/deployment-registry-first in older docs, while current behavior is relay-read-model-first and signer-first | `README.md`, `docs/architecture.md`, `docs/control-planes.md`, `web/src/lib/stores/controlplane.svelte.js` |
| Soul lifecycle reads as complete in Soul Factory docs but incomplete in compatibility notes | `docs/soul-factory.md`, `docs/protocol-compatibility.md` |
| Deployment/config docs discuss settings not reflected in the selected example config | `docs/deployment.md`, `config.yaml` |

## Conservative usage guidance

- Use `docs/control-planes.md` as the primary narrative spec.
- Use `internal/api/handlers/system.go`, router wiring, and domain models as the implementation contract.
- Use `README.md`, `docs/architecture.md`, and `docs/protocol-compatibility.md` only with code cross-checks.
- Treat Soul Factory and parts of the CI/OCI story as partially current until deeper implementation review is complete.
