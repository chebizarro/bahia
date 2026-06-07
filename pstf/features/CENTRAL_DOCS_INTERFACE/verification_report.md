# CENTRAL_DOCS_INTERFACE Verification Report

Updated: 2026-06-07
Bead: `bahia-521z`
Plan: `docs/plans/central-docs-interface-2026-06-05.md` Item 8

## Scope verified

Item 8 verifies the convergence of Items 1-7 without reopening implementation scope unless a blocker is found. The reviewed scope is the central docs path:

```text
docs/user-guide/*.md
  -> internal/docs catalog + reader
  -> MCP docs resources/tools
  -> Go read-only docs API under /api/v1
  -> /docs web UI
  -> route doc metadata in nav-model.js
  -> assistant selected_refs: ["docs:<topic>"]
  -> AssistantContextBuilder Documentation References excerpts
```

## Repository evidence reviewed

- `internal/docs/catalog.go` and `internal/docs/catalog_test.go` implement and test deterministic scanning, nested topic normalization, metadata extraction, safe reads, link resolution, and rejected unsafe links.
- `internal/mcp/docs_resources.go` and `internal/mcp/docs_resources_test.go` show MCP list/read/resources are catalog-backed and include `features-fleet-health` without static drift.
- `internal/api/handlers/docs.go`, `internal/api/router/router.go`, and `internal/api/router/docs_routes_test.go` show `/api/v1/docs` and `/api/v1/docs/{topic}` are GET-only read routes with explicit errors and no Nostr command metadata.
- `web/src/routes/docs/+page.svelte`, `web/src/routes/docs/[topic]/+page.svelte`, `web/src/lib/components/docs/*`, and `web/tests/unit/docs-ui.test.js` show the UI loads API catalog/docs, renders sanitized Markdown, rewrites central internal links, preserves external links, and handles failures explicitly.
- `web/src/lib/components/nav-model.js`, `web/src/lib/components/Nav.svelte`, `internal/docs/nav_model_topics_test.go`, and `web/tests/unit/nav.test.js` show global Docs navigation and route docTopic mappings backed by the central catalog.
- `web/src/lib/components/assistant/*`, `web/src/lib/stores/assistant.svelte.js`, and `web/tests/unit/assistant/assistant-components.test.js` show route docs refs are visible, dismissible, and sent through `selected_refs` without replacing operational refs.
- `internal/service/assistant_context_builder.go` and `internal/service/assistant_context_builder_docs_test.go` show docs refs resolve into bounded `Documentation References` context and unresolved refs are non-fatal.
- `pstf/features/BAHIA_NOSTR_AUDIT_PARITY/route_transport_matrix.json` and `web/tests/unit/route-transport-matrix.test.js` classify docs routes as `http_native` read-only docs queries and keep REST imports allowlisted only for non-signer-first surfaces.

## Acceptance criteria mapping

See `acceptance_criteria.json` and `test_matrix.json` for the durable criteria-to-test mapping. Summary:

- CDI-AC-01: central catalog and safe reader.
- CDI-AC-02: MCP drift removal.
- CDI-AC-03: read-only docs API and route transport evidence.
- CDI-AC-04: browsable `/docs` UI.
- CDI-AC-05: route-aware docs metadata/global affordances.
- CDI-AC-06: frontend assistant docs refs via existing selected_refs seam.
- CDI-AC-07: backend assistant docs context resolution.
- CDI-AC-08: cross-track PSTF/docs/gates/Beads closeout.

## Quality gates

Final command evidence from Item 8 closeout:

- Passed: `go test ./internal/docs ./internal/mcp ./internal/api/router ./internal/service`
- Passed: `go test ./...`
- Passed: `cd web && npm test -- --run tests/unit/docs-ui.test.js tests/unit/nav.test.js tests/unit/assistant/assistant-components.test.js tests/unit/assistant/assistant-store.test.js tests/unit/route-transport-matrix.test.js` — 5 files / 27 tests passed; emitted existing Svelte compiler advisories in `AssistantPlanApproval.svelte`.
- Passed: `cd web && npm run lint` — deterministic SvelteKit `svelte-check --tsconfig ./tsconfig.json` gate completed with 0 errors and 0 warnings.
- Passed: `cd web && npm run build` — production build completed; emitted existing Svelte/SvelteKit advisories for `web/src/routes/policies/+page.svelte`, `AssistantPlanApproval.svelte`, the SvelteKit tsconfig extension, and an unused `qrcode` default import warning.

## Route transport disposition

The central docs browser routes are HTTP-native read-only query surfaces. They are intentionally not signer-first Nostr mutation routes because they read local documentation through the authenticated `/api/v1` read-route layer. The assistant route does not introduce a new docs transport: docs references are metadata in `selected_refs` on the existing encrypted ContextVM `assistant/prompt` request, and durable operational truth remains on canonical observable streams when the assistant performs or follows mutations.

## Production-readiness closeout

Reviewed scope has no fake docs catalog, placeholder UI route, ignored API failure, hardcoded topic drift, or synthetic Nostr/ContextVM completion behavior:

- Catalog data is scanned from `docs/user-guide`, not hardcoded.
- MCP and web surfaces consume the same catalog service.
- API/UI error paths are explicit and tested.
- Internal Markdown links are resolved centrally and unsafe links are visibly rejected.
- Route docs references are catalog-backed and test-guarded.
- Assistant docs context uses selected references and the existing encrypted assistant transport; it does not poll relays or invent request/response semantics for docs.

## Remaining work

No CENTRAL_DOCS_INTERFACE implementation work remains from evidence review. Tracked follow-ups:

- None.

Resolved follow-ups:

- `bahia-oikr`: existing unrelated full navigation E2E harness console errors; resolved by tightening navigation E2E runtime error detection and verifying focused docs navigation plus full navigation spec on 2026-06-07.
- `bahia-4ez0`: project-level missing `web` lint script discovered during closeout; resolved by adding and verifying the deterministic SvelteKit lint/check gate.
