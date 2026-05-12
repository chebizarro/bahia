# Bahia Planning Docs Index

## Reading guide

### Freshness labels
- **current** — strongly aligned with current code and tests
- **mixed** — partly current, but incomplete or drifting
- **stale** — materially behind current implementation
- **historical** — background only; not reliable as current contract

### Confidence labels
- **high** — code and tests strongly support this assessment
- **medium** — partial support; some uncertainty remains
- **low** — aspirational or contradicted by implementation

## Index

| Path | Apparent purpose | Freshness / staleness indicators | Features mentioned | Conflicts with code or other docs | Confidence |
|---|---|---|---|---|---|
| `README.md` | Product positioning and quick start | Core deployment story is still right; omits sidecar-first bootstrap, encrypted browser flows, MCP, and `Nostr discovery events (kind 31974 + NIP-51 kind 30002)` importance | builds, artifacts, deploys, drift, rollback, Soul Factory, OCI, Hive-CI | conflicts with newer control-plane framing in `docs/control-planes.md` and observed web bootstrap | medium |
| `web/README.md` | Web dashboard overview | materially stale on auth and transport details | dashboard, services, environments, workers, policies, events | says JWT/localStorage auth is current and mentions future NIP-07/NIP-46, while current code is signer-first NIP-07/NIP-46 and direct NIP-98-compatible | low |
| `docs/control-planes.md` | Primary narrative spec for control-plane topology and transport semantics | matches router, Nostr discovery bootstrap, sidecar-first topology, encrypted request/result contract, signer-first operator flows, and 311xx deprecation | public request/result/read-model kinds, encrypted flows, adoption, direct runtime, LLM, MCP, sidecar | strongest narrative spec; only partial gap is that newer web helper kinds go beyond the summarized tables | high |
| `docs/api.md` | REST API reference | covers major CRUD surfaces but omits key mounted routes | services, environments, builds, artifacts, deployments, rollback, adoption, direct runtime, OCI | omits `Nostr discovery events (kind 31974 + NIP-51 kind 30002)`, `/mcp`, orgs, workers, payments, notifications, signatures, SBOM, and some LLM/read-only routes present in `internal/api/router/router.go` | medium |
| `docs/event-spec.md` | Nostr event taxonomy and payload guide | strong for audit/LLM/Hive-CI; incomplete for encrypted and adoption/direct-runtime families | 31000-series audit, 31964/31965 projections, 5100/5101/5102 Loom, 5401/5402 Hive-CI, LLM request/result/status | does not describe encrypted `5980/7980` and is weaker than `docs/control-planes.md` for operator families | medium |
| `docs/nostr-commands.md` | Canonical public Nostr command contract | current for service + LLM 596x/696x/796x and 311xx deprecation | service requests, LLM requests, read models, result/status correlation | omits adoption `5978/5979/6978/7978/7979`, direct-runtime `6963`, and encrypted `5980/7980` that are documented elsewhere and implemented | medium |
| `docs/deployment.md` | Runtime/deployment configuration and rollout guidance | useful runtime model; examples appear broader than root example config and current web scope | runtime endpoints, endpoint refs, compose/podman, OCI, Hive-CI, monitoring, operator rollout pointers | config examples and rollout expectations do not line up cleanly with root `config.yaml`; adoption is documented operationally but not surfaced in web routes | medium |
| `docs/architecture.md` | High-level system architecture narrative | REST/service-layer framing lags implementation | API server, service layer, workflow coordinator, OCI registry, Hive-CI bridge, reconciler | omits relay sidecar, encrypted browser flows, signer-first writes, adoption/direct runtime, and LLM control-plane reality | low |
| `docs/protocol-compatibility.md` | Cross-protocol compatibility/status matrix | contains both current and obsolete material | Loom, NIP-98, NIP-05, NIP-46, Blossom, Cashu, OCI, Hive-CI, 311xx bridge | internally conflicts on NIP-46; lists 31100-31105 as implemented while newer docs deprecate them; Soul lifecycle status conflicts with `docs/soul-factory.md` | low |
| `docs/adoption-production-rollout.md` | Operational runbook for signer-first adoption/import and direct runtime rollout | aligns with operator-action code and router feature gating | operator pubkeys, endpoint refs, status/result following, fallback rules, rollout sequence | operational runbook rather than full end-user product spec; no conflicting code found in sampled areas | high |
| `docs/adoption-signer-first-operator-checklist.md` | Operator checklist for signer-first rollout | likely current and recent by naming/date context; should be used as operational aid | signer-first adoption, operator actions | not a behavioral spec; subordinate to implementation and rollout doc | medium |
| `docs/adoption-live-network-operator-checklist.md` | Live-network rollout checklist | recent operational checklist | live operator verification | not a product behavior contract | medium |
| `docs/adoption-live-network-verification.md` | Verification notes for adoption rollout | recent operational evidence | live-network verification | evidence doc, not normative spec | medium |
| `docs/relay-sidecar.md` | Relay sidecar topology and policy description | consistent with sidecar-first architecture and Nostr discovery relay discovery | public browser relays, sidecar/backend URLs, mirrored kinds, policy | narrow scope only; complements `docs/control-planes.md` | high |
| `docs/soul-factory.md` | Soul Factory narrative, event model, and operational story | substantial and partly validated, but some lifecycle claims exceed currently verified evidence | templates, souls, provisioning, signing, lifecycle actions | conflicts with `docs/protocol-compatibility.md` which still lists Soul Lifecycle as an incomplete known gap | medium |
| `docs/WEB_APP_PRODUCTION_PLAN.md` | Historical production readiness / roadmap for the web app | clearly historical despite “production-ready” header; many claims no longer fit current app shape | CRUD, auth, relay management, notifications UI, Soul Factory, dashboard, tests | claims missing notifications UI and old REST/client gaps that no longer match `web/src/routes` and `web/tests`; useful only as history | low |
| `docs/web-testing.md` | Web testing strategy and conventions | generally current for tools and commands | Vitest, Playwright, mocking patterns, coverage goals | still documents EventSource/SSE patterns while current shared state is relay-read-model-first; should not be treated as product spec | medium |
| `docs/web-api-client.md` | Web API client guide | partially current but subordinate to actual client/store behavior | client methods, Bahia envelope, errors | REST-centric; current product also relies on relay bootstrap and encrypted Nostr flows not captured here | medium |
| `docs/web-components.md` | Component library guidance | likely current for shared UI parts | components, accessibility, props | not a product behavior contract | medium |
| `docs/web-app-setup.md` | Local setup guide for web app | likely current for development workflow | setup, auth, troubleshooting | not a normative product behavior doc | medium |
| `docs/investigations/pstf-discovery-2026-05-03.md` | Discovery investigation synthesizing repo state for PSTF | recent, grounded in code, and still aligned with current sampled implementation | product purpose, flows, doc ranking, spec gaps, slices | investigation artifact, not normative spec; should be cross-checked when deeper code changes land | high |
| `docs/investigations/signer-first-auth-audit-2026-05-03.md` | Focused investigation into signer-first auth | recent by date/path | auth migration / signer-first status | investigative evidence, not contract | medium |
| `docs/investigations/payments-orgs-private-transport-2026-05-03.md` | Focused investigation into encrypted/private transport | recent by date/path | payments, orgs, private transport | investigation artifact | medium |
| `docs/investigations/swarmstr-tool-provisioning-2026-05-02.md` | Focused investigation into tool provisioning | recent by date/path | tool provisioning | investigation artifact | medium |
| `docs/investigations/signer-first-operator-rehearsal-2026-05-04/README.md` | Rehearsal evidence for signer-first operator flows | freshest operational evidence in repo sample | scan/import/restart/stop/deploy rehearsal | evidence doc, not full spec | high |
| `docs/archive/REVIEW-AND-ROADMAP-2024.md` | Archived roadmap / historical context | archived and date-stamped old state | 2024 roadmap themes | do not use as current behavior source | low |
| `pstf/README.md` | PSTF artifact workflow instructions | current and procedural | artifact sequence, feature folder convention | none observed | high |
| `pstf/product_map.md` | Current product map artifact | current after this discovery pass | product purpose, actors, features, flows, slices | derived artifact; authority comes from cited code/docs | high |
| `pstf/spec_gap_report.md` | Current spec gap artifact | current after this discovery pass | missing spec, conflicts, untested behavior, risky behavior | derived artifact; authority comes from cited code/docs | high |

## Highest-authority sources for PSTF onboarding

1. `internal/domain/models.go`
2. `internal/domain/llm.go`
3. `internal/domain/soul.go`
4. `internal/api/router/router.go`
5. `internal/adapters/nostr/projector.go`
6. `internal/controlplane/encrypted_transport.go`
7. `internal/controlplane/operator_actions.go`
8. `web/src/lib/stores/controlplane.svelte.js`
9. `web/src/lib/stores/auth.svelte.js`
10. `docs/control-planes.md`
11. `docs/adoption-production-rollout.md`
12. `docs/investigations/pstf-discovery-2026-05-03.md`

## Highest-risk documentation conflicts

| Conflict | Sources |
|---|---|
| Current auth model in web docs still says JWT/localStorage while implementation is signer-first NIP-07/NIP-46 + direct NIP-98 compatibility | `web/README.md`, `web/src/lib/stores/auth.svelte.js`, `docs/control-planes.md` |
| Product framed as REST/deployment-registry-first while current browser behavior is relay-read-model-first | `README.md`, `docs/architecture.md`, `docs/api.md`, `docs/control-planes.md`, `web/src/lib/stores/controlplane.svelte.js` |
| NIP-46 described as both stubbed and implemented | `docs/protocol-compatibility.md` |
| 311xx bridge listed as implemented in one doc and deprecated in current docs | `docs/protocol-compatibility.md`, `docs/control-planes.md`, `docs/nostr-commands.md` |
| Soul lifecycle reads as complete in one doc and incomplete in another | `docs/soul-factory.md`, `docs/protocol-compatibility.md` |
| Web production plan claims gaps that current routes/tests have already closed | `docs/WEB_APP_PRODUCTION_PLAN.md`, `web/src/routes/notifications/`, `web/tests/e2e/` |

## Conservative usage guidance

- Use code plus `docs/control-planes.md` as the main contract.
- Use `docs/adoption-production-rollout.md` for operator-flow expectations.
- Use investigation docs as evidence summaries, not primary specs.
- Treat `README.md`, `web/README.md`, `docs/architecture.md`, `docs/protocol-compatibility.md`, and `docs/WEB_APP_PRODUCTION_PLAN.md` as secondary and cross-check every claim.
