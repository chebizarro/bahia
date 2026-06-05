# Central Documentation Interface: Plan

## Goal
Make Bahia's existing user-guide documentation centrally managed, browsable from the web UI, and contextually available throughout the interface without duplicating documentation sources or weakening the existing MCP docs access path.

## Background
- User-facing documentation already lives under `docs/user-guide/`, with top-level guides and feature docs under `docs/user-guide/features/*.md`. The manual index groups getting started, core concepts, feature guides, and integration/reference docs in `docs/user-guide/index.md:27-61`, and explicitly says the same docs are intended for human users via web and AI agents via MCP in `docs/user-guide/index.md:96-105`.
- MCP is the current central serving/indexing path for docs. `internal/mcp/docs_resources.go:11-13` defines `DocsBasePath = "docs/user-guide"`; `internal/mcp/docs_resources.go:15-64` walks Markdown files and exposes them as `bahia://docs/<name>` resources with `text/markdown` MIME type; `internal/mcp/docs_resources.go:69-99` resolves topic names for direct docs, feature shorthands, and hyphen-to-path fallback reads.
- Docs discovery can diverge today: dynamic MCP resources scan actual Markdown files, `DocsResourceCatalog()` is a hardcoded topic list in `internal/mcp/docs_resources.go:216-241`, the MCP tool description hardcodes available topics in `internal/mcp/docs_resources.go:188-214`, and the human index is maintained separately in `docs/user-guide/index.md:39-56`. One observed drift: `docs/user-guide/features/fleet-health.md` appears in the user-guide index at `docs/user-guide/index.md:51-53` but is absent from `DocsResourceCatalog()` and the `bahia_docs_read` description list.
- The web UI has a global shell where documentation can be surfaced consistently. `web/src/routes/+layout.svelte:1-13` imports the global nav, auth guard, toast container, and `AssistantChat`; `web/src/routes/+layout.svelte:31-34` derives `assistantRouteContext` from `page.url.pathname` and params; `web/src/routes/+layout.svelte:68-77` wraps all pages in the global shell and mounts `<AssistantChat routeContext={assistantRouteContext} />`.
- Navigation is centrally modeled. `web/src/lib/components/nav-model.js:1-43` defines grouped feature links, `web/src/lib/components/nav-model.js:45` flattens them, `web/src/lib/components/nav-model.js:52-75` implements active link and current-location mapping. `web/src/lib/components/Nav.svelte:127-169` renders these sections, so docs affordances can be tied to existing route metadata rather than per-page ad hoc links.
- Assistant prompts already carry contextual hooks. `web/src/lib/stores/assistant.svelte.js:460-470` publishes `route_context` and `selected_refs`; `web/src/lib/stores/assistant.svelte.js:477-488` sends the payload through the existing encrypted ContextVM `assistant/prompt` operation. `selected_refs` is present as a seam, but the current inspected UI path does not populate it from documentation selections.
- `AssistantContextBuilder` already uses injected registry surfaces plus `defaultAssistantContextMaxChars = 24000` and a configurable `MaxChars` budget in `internal/service/assistant_context_builder.go:12-78`; selected refs are listed and resolved in `internal/service/assistant_context_builder.go:100-146`. Docs refs should extend that pattern, not introduce a parallel assistant context path.
- The Go API layer already has a read route group. `internal/api/router/router.go:78-111` builds a chi router with global middleware and separate read/write rate limiters; `internal/api/router/router.go:197-306` mounts authenticated `/api/v1` read routes with the read limiter. This is the planned seam for browser docs catalog/read endpoints.
- Feature pages use local page headers but no centralized page-header/docs component. Examples include Services `web/src/routes/services/+page.svelte:249-251`, Deployments `web/src/routes/deployments/+page.svelte:302-304`, Environments `web/src/routes/environments/+page.svelte:150-152`, LLM `web/src/routes/llm/+page.svelte:227-231`, Souls `web/src/routes/souls/+page.svelte:55-59`, and Workers `web/src/routes/workers/+page.svelte:473-475`.
- Prior art exists for docs and contextual assistant work. Commit `dd5567ad426a454a08d129fa415dce78f3e67829` added comprehensive user-guide docs plus MCP access (`internal/mcp/docs_resources.go`, `internal/mcp/docs_resources_test.go`, `docs/user-guide/index.md`, `docs/user-guide/mcp-tools.md`). `docs/plans/llm-enabled-ux-foundation-2026-05-16.md:153-170` lists assistant frontend/backend pieces, and `docs/plans/llm-enabled-ux-foundation-2026-05-16.md:396-427` covers assistant context builder, web UI, and verification work. `docs/designs/floating-chat-bubble.md:9-58` captures the floating assistant UX direction that now anchors contextual help.
- No SvelteKit docs route, static docs site generator, Markdown renderer, generated docs search index, or web UI import of `docs/user-guide/*.md` was found in the inspected seams. The current central serving path is runtime MCP-backed filesystem reads plus direct Markdown links from repository docs such as `README.md:188-203`.
- External package check supports `marked` plus `isomorphic-dompurify` as the minimal runtime Markdown rendering stack for trusted local Markdown in SvelteKit; `marked` documents that it does not sanitize HTML, so any implementation must sanitize before Svelte `{@html}` rendering. The package choice is a recommendation, not a protocol-level constraint: an equivalent parser/sanitizer is acceptable if it satisfies the tests.

## Approach
Implement a targeted central docs pipeline rather than a broad documentation-site rewrite:

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

The source of truth remains `docs/user-guide/`. A new reusable `internal/docs` package should own catalog scanning, topic normalization, safe reads, metadata extraction, relative-link rewriting support, and deterministic ordering. MCP becomes a consumer of that catalog instead of maintaining a separate static topic list, and the browser gets a read-only API backed by the same service.

Use the existing Go API router for docs endpoints, not a separate SvelteKit filesystem/bundling path. This keeps MCP, assistant context, and web UI on the same runtime docs service, removes catalog drift once, and fits the existing `/api/v1` read-route pattern in `internal/api/router/router.go:197-306`. The endpoints should be classified in PSTF as read-only docs/query surfaces, not signer-first control-plane mutations.

For the web interface, add real SvelteKit routes: `/docs` for grouped topic discovery and `/docs/[topic]` for document reading. The reader must rewrite internal Markdown links into docs topics. The rule should be centralized with the catalog/reader: resolve relative links from the source document path, convert target Markdown paths under `docs/user-guide/` to `/docs/<topic>`, preserve external links unchanged, and reject or visibly mark links that point outside the docs root.

For contextual help, preserve the existing assistant transport. Assistant prompts continue through encrypted ContextVM `assistant/prompt`; contextual documentation is attached as selected references, not sent through a new polling loop or synthetic request/response abstraction. The frontend should derive the current route's docs topic and pass visible, dismissible docs references to the composer. The backend context builder should resolve `docs:<topic>` and `bahia://docs/<topic>` refs into a dedicated `Documentation References` section using the same `MaxChars` budgeting discipline as the existing builder.

The first implementation should expose every `docs/user-guide` Markdown topic through the catalog and `/docs`, while route-aware contextual help should start with nav-mapped product routes only. That avoids hiding existing docs, but keeps contextual suggestions precise and testable. No work item may claim a docs surface exists until it is backed by the central catalog, tests, and PSTF evidence; no fake docs catalog, placeholder web route, or synthetic Nostr/ContextVM behavior should be accepted.

## Work Items

### Item 1 — Central docs catalog service
**Goal:** Create one runtime catalog and safe reader for the `docs/user-guide/` corpus.

**Done when:**
- All Markdown files under `docs/user-guide/` are listed deterministically, including nested feature docs such as `features-fleet-health`.
- Topic normalization preserves the existing MCP convention: `index`, `getting-started`, `features-services`, `features-fleet-health`, and equivalent nested names.
- Reads reject path traversal and return explicit not-found errors for missing topics.
- Metadata includes topic, title from first `#`, category from path (`feature`, `guide`, `reference`), source path, and web href `/docs/<topic>`. Descriptions or frontmatter are deferred unless required by the UI implementation.
- Link-resolution helpers map relative Markdown links under `docs/user-guide/` to docs topics and reject outside-root links.
- Unit tests cover drift-sensitive files, safe reads, deterministic ordering, and link rewriting.

**Key files:** `internal/docs/*`, `docs/user-guide/index.md`, `docs/user-guide/features/*.md`.

**Dependencies:** None.

**Size:** M.

### Item 2 — Remove MCP docs catalog drift
**Goal:** Make MCP docs resources, `bahia_docs_read`, and `bahia_docs_list` use the central docs catalog.

**Done when:**
- `DocsResourceCatalog()` no longer hardcodes a list that can diverge from filesystem docs.
- `bahia_docs_list` returns the scanned, sorted catalog and includes `features-fleet-health` without special casing.
- `docsToolDefinitions()` points agents to `bahia_docs_list` instead of embedding a static full topic list.
- Existing MCP resource URIs and tool names remain compatible.
- MCP tests prove list/read/resource behavior against the central catalog and preserve missing-topic error behavior.

**Key files:** `internal/mcp/docs_resources.go`, `internal/mcp/docs_resources_test.go`, `internal/mcp/server.go:1976-1986`.

**Dependencies:** Item 1.

**Size:** S.

### Item 3 — Read-only web docs API
**Goal:** Expose catalog and document reads to the browser through the existing Go API read-route layer.

**Done when:**
- `/api/v1/docs` returns the central catalog grouped or groupable by category.
- `/api/v1/docs/{topic}` returns document Markdown plus resolved metadata and link-rewrite data needed by the web reader.
- Missing topics return explicit not-found responses; server errors are visible and testable, not silent fallbacks.
- The routes use the existing read limiter/auth/tier conventions in `internal/api/router/router.go:197-306` and are documented in PSTF route transport evidence as read-only docs/query surfaces.
- Tests prove the endpoints do not publish Nostr events, claim relay completion, or behave like control-plane mutations.

**Key files:** `internal/api/handlers/*`, `internal/api/router/router.go`, `internal/api/router/*_test.go`, route transport/PSTF artifacts.

**Dependencies:** Item 1.

**Size:** S.

### Item 4 — Browsable `/docs` web interface
**Goal:** Add a real web documentation surface that reads from the central docs API.

**Done when:**
- `/docs` displays grouped topics for Getting Started/Guides, Feature Guides, and Integration/Reference.
- `/docs/[topic]` renders sanitized Markdown, uses the centralized internal-link mapping, preserves external links, and handles missing topics explicitly.
- The renderer uses a maintained Markdown parser plus sanitizer before Svelte `{@html}` rendering. `marked` plus `isomorphic-dompurify` is the recommended minimal package choice; an equivalent stack is acceptable if it passes sanitization and link tests.
- The page supports deep links, accessible navigation back to the catalog, and no placeholder empty-state claiming docs are loaded when the API failed.
- Web tests cover catalog loading, document rendering, sanitized HTML, internal link rewriting, not-found handling, and failed API states.

**Key files:** `web/src/routes/docs/+page.svelte`, `web/src/routes/docs/[topic]/+page.svelte`, new docs UI components under `web/src/lib/components/docs/*`, web package manifest.

**Dependencies:** Item 3.

**Size:** M.

### Item 5 — Route-aware docs metadata and global affordances
**Goal:** Make documentation contextual in the shell from the same navigation model that drives routes and breadcrumbs.

**Done when:**
- `nav-model.js` includes optional `docTopic` metadata for routes with corresponding docs, including `features-services`, `features-deployments`, `features-fleet-health`, `features-llm-routes`, and `features-ml-models` where route labels differ from topic names.
- Helper functions derive current route docs links without duplicating route logic in page components.
- `Nav.svelte` exposes a global Docs entry and a contextual docs action for the current route when a mapped topic exists.
- Tests assert that every referenced `docTopic` exists in the central catalog.

**Key files:** `web/src/lib/components/nav-model.js`, `web/src/lib/components/Nav.svelte`, `web/src/routes/+layout.svelte`.

**Dependencies:** Item 1.

**Size:** S.

### Item 6 — Assistant documentation references in the frontend
**Goal:** Attach relevant route documentation to assistant prompts through the existing `selected_refs` seam.

**Done when:**
- `+layout.svelte` or the assistant shell derives the current docs topic from the route model.
- `AssistantChat -> AssistantPanel -> AssistantComposer` can receive default docs refs without breaking existing prompt/session behavior.
- Docs refs are visible and dismissible in the composer before send.
- Prompt submission sends `selectedRefs: ["docs:<topic>"]` through the existing `publishAssistantPrompt` payload.
- Web tests prove route-derived docs refs are included by default, can be dismissed, and do not remove user-selected operational refs.

**Key files:** `web/src/routes/+layout.svelte`, `web/src/lib/components/assistant/AssistantChat.svelte`, `web/src/lib/components/assistant/AssistantPanel.svelte`, `web/src/lib/components/assistant/AssistantComposer.svelte`, `web/src/lib/stores/assistant.svelte.js`.

**Dependencies:** Item 5.

**Size:** M.

### Item 7 — Backend assistant docs context resolution
**Goal:** Resolve documentation refs into bounded assistant context without changing assistant transport semantics.

**Done when:**
- `AssistantContextBuilder` accepts a docs provider or central docs service dependency following the existing injected-registry pattern in `internal/service/assistant_context_builder.go:17-78`.
- Selected refs in `docs:<topic>` and `bahia://docs/<topic>` forms resolve to title/topic metadata plus bounded excerpts under `Documentation References`.
- The excerpt budget is deterministic: allocate a documented fraction of `MaxChars` to docs refs, split it across selected docs, preserve headings where possible, and always run final context through the existing truncation guard.
- Missing docs refs produce an explicit unresolved-ref note in assistant context and do not fail prompt processing.
- Existing operational selected-ref behavior remains covered by tests.

**Key files:** `internal/service/assistant_context_builder.go`, `internal/domain/assistant.go`, `internal/docs/*`, assistant context builder tests.

**Dependencies:** Item 1.

**Size:** M.

### Item 8 — Cross-track verification, documentation, and closeout
**Goal:** Prove the integrated docs experience is real, deterministic, and documented after the implementation tracks converge.

**Done when:**
- PSTF artifacts under `pstf/features/CENTRAL_DOCS_INTERFACE/` define acceptance criteria, test matrix, defects, and verification evidence that map to Items 1–7.
- User-facing docs explain web docs access and contextual assistant docs behavior; update `docs/user-guide/index.md`, `docs/user-guide/mcp-tools.md`, and `docs/user-guide/nostr-integration.md` if assistant context semantics are described there.
- Beads issues track any remaining product decisions or incomplete slices before closeout.
- Quality gates include relevant Go tests, web tests, linters/builds, and route transport evidence.
- Closeout confirms no fake docs catalog, placeholder UI route, ignored API failure, hardcoded topic drift, or synthetic Nostr/ContextVM behavior remains in the touched scope.

**Key files:** `pstf/features/CENTRAL_DOCS_INTERFACE/*`, `docs/user-guide/index.md`, `docs/user-guide/mcp-tools.md`, `docs/user-guide/nostr-integration.md`, relevant Go/web test files, Beads issues.

**Dependencies:** Items 1–7.

**Size:** S.

## Suggested Implementation Order
1. Land the central catalog/reader and tests (Item 1).
2. In parallel after Item 1: refactor MCP drift out (Item 2), add the Go read-only docs API (Item 3), add route metadata (Item 5), and start backend docs context resolution (Item 7).
3. Build `/docs` on the API (Item 4).
4. Thread route-derived docs refs through the assistant frontend (Item 6).
5. Converge with PSTF evidence, docs updates, quality gates, Beads closeout, commit, and push (Item 8).

## Open Questions
- Should documentation refs be attached by default for every mapped route, or should the assistant present the docs ref as visible-but-dismissable only when the user opens the assistant on that route? Either choice preserves transport semantics, but it affects how assertive the UI feels.
- Should descriptions/order eventually move to Markdown frontmatter? The first slice should not block on it; add frontmatter only if title/category grouping is inadequate after the real `/docs` UI exists.

## References
- `docs/user-guide/index.md`
- `internal/mcp/docs_resources.go`
- `internal/mcp/docs_resources_test.go`
- `internal/mcp/server.go:1976-1986`
- `internal/api/router/router.go:78-111`
- `internal/api/router/router.go:197-306`
- `web/src/routes/+layout.svelte`
- `web/src/lib/components/nav-model.js`
- `web/src/lib/components/Nav.svelte`
- `web/src/lib/components/assistant/*`
- `web/src/lib/stores/assistant.svelte.js`
- `internal/domain/assistant.go`
- `internal/service/assistant_context_builder.go`
- `docs/architecture.md`
- `docs/control-planes.md`
- `docs/plans/llm-enabled-ux-foundation-2026-05-16.md`
- `docs/designs/floating-chat-bubble.md`
- `docs/designs/consolidated-navigation.md`
- `docs/investigations/fake-nostr-routes-audit-2026-06-01.md`
- `docs/adoption-live-network-verification.md`
- Commit `dd5567ad426a454a08d129fa415dce78f3e67829` (`feat: Add comprehensive end-user documentation with MCP access`)
- Marked documentation: https://marked.js.org/
- isomorphic-dompurify package: https://www.npmjs.com/package/isomorphic-dompurify
- svelte-exmarkdown package: https://www.npmjs.com/package/svelte-exmarkdown
