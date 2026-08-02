# Documentation Review & Update Plan — 2026-08-01

Tracking bead: **bahia-vit1z** (in progress). Related open bead: **bahia-qsfnh** (document continuity/events/environment-states).

Goal: verify README.md and the entire `docs/` tree against the current codebase, correct residual drift, and document features landed since the 2026-07-17 broad doc refresh (`3b9cb42f`).

**Ground rules for all sub-agents:**
- Verify claims against source code (cmd/, internal/, web/, pkg/) before writing. Never document a command, flag, tool, route, or event kind without confirming it exists.
- Historical/point-in-time folders are READ-ONLY records — do not rewrite: `docs/plans/`, `docs/reviews/`, `docs/investigations/`, `docs/analysis/`, `docs/archive/`, `docs/designs/`.
- Do NOT run `git commit` or `git push` — the orchestrator commits after verification.
- Mark your item's checkboxes here when done, and note anything deferred at the bottom.

## Research findings (from git + beads archaeology, 2026-08-01)

### Reference points
- README + user-guide broad refresh: `3b9cb42f` (2026-07-17). Later: NIP-46 docs in control-planes (`0c60c75c`, 07-26), relay retention (`245b38dd`), replay cap fix (`2861cb71`), SoulFactory NIP-29 groups (`bd31a9c7`), LiteLLM UI (`fad64dca`), token-file auth (`70365792`).
- Drift review `docs/reviews/user-guide-drift-2026-07-17.md` was mostly remediated by bead `bahia-5n5gp` (closed 07-18), but residual drift remains (listed below).
- Nested `bahia/bahia/docs/` dirs are empty untracked residue (no git history) — safe to delete.

### Residual known drift (unfixed from 07-17 review)
- `user-guide/features/artifacts.md`: nonexistent `bahia artifacts …` and `bahia builds register` commands.
- `user-guide/features/notifications.md` + `troubleshooting.md`: nonexistent `bahia notifications …` commands.
- `user-guide/features/souls.md`: shows `soul_factory_regenerate` invocation while stating those tools don't exist.
- `workers.md` / `ml-models.md`: stale worker capability kind `31989`; reconcile with Loom kind `10100`.
- `/continuity`, `/events`, `/environment-states` web routes undocumented (bead `bahia-qsfnh`).
- README `LICENSE` link broken (no LICENSE file at repo root); README doesn't link `docs/user-guide/index.md`.

### Post-2026-07-17 changes needing doc coverage (commit refs)
- **Server/API**: native encrypted service deployments + runtime lifecycle (`bb27be2d`); adoption/import hardening — deployment-unit binding, explicit legacy takeover, signed-import org propagation (`06bdd13d`, `202f5afc`, `d7b25b49`); secret-backed LLM route headers (`e69ce18b`), gateway token-file loading (`70365792`), event-driven provisioning intake (`acbe975a`); Loom deployment recovery + persisted state (`bb679f2e`); payment-history ContextVM handler (`5feb254d`); stale deployment-run health events (`4198bb06`).
- **CLI**: NIP-46 remote operator signing incl. relay pool (`0c60c75c`); signed imports carry org identity (`d7b25b49`).
- **Web UI**: LiteLLM model config UI + secret-backed header controls (`fad64dca`, `e69ce18b`); durable Nostr subscription recovery + health introspection (`75d27210`, `2f567a1c`); live branch/docs subscriptions, EOSE ordering (`733219ec`, `ea296836`); encrypted-result ordering (`888cbcda`); serialized collection-cache snapshots (`6c925828`); souls UI reconciles provisioned drafts (`75b349a2`).
- **Nostr/relay**: Cascadia canonical kinds (`b82a034f`); relay history persistence, admission restrictions removed (`1bd0625f`, `dfac85a1`); durable publish outbox (`0bc8081f`); replay scoping/caps (`ed1fb7c8`, `252cd759`, `b52b297c`, `2861cb71`); retention enforcement (`245b38dd`); transport health metrics (`00846411`); ContextVM relay-policy/tier-one routing, isolated response publishing, gift-wrap reply correlation (`d08e66db`, `36125957`, `3da7e9d2`, `006dd185`); audit projections relay-public (`0b92f48d`).
- **SoulFactory**: canonical ContextVM mutation handlers + provisioning projections (`3cebf393`, `12adca15`); deterministic provisioning recovery + restart reconciliation (`38d33c60`, `f6ea191b`, `f857e0d1`); OpenClaw runtime updates (`618ab42e`); NIP-29 group assignment (`bd31a9c7`); NIP-42 auth retry (`1c6fea32`, `d71b647d`); signet transport canonical events/NIP-17/service identity (`303d9d31`, `70b44890`, `c4b8388e`); bunker secrets removed from public artifacts (`fc08ad65`).
- **Deployment/ops**: fleet-hygiene policy/publisher/reconciler/metrics (`6e4daeb3`); fleet-health gauges + WS6 alert contract/Grafana (`1d37dbcd`, `8bd430f1`, `8353df47`, `2f113953`); OSV cache cleanup runner (`b3924276`); tier-three relay recovery + sidecar in topology (`d77a87f6`, `06c33202`); Loom job profile parameters (`c9de938e`).
- **Behavior changes from closed beads**: real traffic shifting / verified rollback / rollout health failure semantics (`bahia-cg3vb`, `bahia-s4y11`, `bahia-3elai`); tenant-scoped notification channels/logs + honest delivery failures (`bahia-xldxv`, `bahia-7lzih`); stricter NIP-07/NIP-46/backend auth + route gating (`bahia-nh89d`, `bahia-pnme5`, `bahia-kfqp1`, `bahia-6o1tj`); per-tool MCP authorization (`bahia-2r1z5`); Cashu ops fail as unsupported (`bahia-940z7`); rollout/DNS/backend readiness fail closed (`bahia-cgnkb`, `bahia-5gjs1`, `bahia-8r9hf`, `bahia-f8e27`); relay retention/observability/outbox (`bahia-ahdfj`, `bahia-fukea`, `bahia-w4dq6`); persistent docs/branch subscriptions + auth-bootstrap exemption (`bahia-t7rur`).

## Work items

### Item 1 — User guide (`docs/user-guide/**`)
- [x] Fix residual drift: artifacts.md, notifications.md, troubleshooting.md fake CLI commands; souls.md `soul_factory_regenerate`; workers.md/ml-models.md kind `31989` vs Loom `10100` (verify actual kinds in internal/kinds).
- [x] Update feature pages for behavior changes: deployments (traffic shifting, verified rollback, health failure semantics), notifications (tenant channels/logs, honest failures), security/auth (NIP-07/NIP-46 stricter auth, route gating), payments (Cashu fail-unsupported, payment history), llm-routes (secret-backed headers, LiteLLM UI, token-file auth), fleet-health (hygiene policy, gauges), souls (NIP-29 groups, provisioning recovery).
- [x] mcp-tools.md: per-tool MCP authorization (`bahia-2r1z5`); verify tool list against internal/mcp.
- [x] cli-reference.md: NIP-46 remote signing flags, signed-import org identity; verify against cmd/cli.
- [x] nostr-integration.md: canonical kinds, durable outbox, retention, replay caps — verify against internal/kinds + relay code.
- [x] NEW pages for `/continuity`, `/events`, `/environment-states` routes + index them (closes bead `bahia-qsfnh`).
- [x] index.md + getting-started/core-concepts consistency pass.

### Item 2 — README + core top-level docs
- [x] README.md: fix LICENSE link (link removal or flag missing file — do not invent a license), link `docs/user-guide/index.md`, verify feature list/quick-start commands against cmd/cli.
- [x] architecture.md, control-planes.md, api.md, event-spec.md, protocol-compatibility.md, nostr-commands.md, nostr-event-implementation-guide.md: verify against canonical kinds migration, ContextVM routing, durable outbox/replay/retention, NIP-46 signing, audit projections.
- [x] deployment.md, adoption-*.md, push-to-deploy-and-hiveci-runbook.md, relay-sidecar.md, operator-assistant-protocol.md: adoption/import hardening, encrypted service deployments, runtime lifecycle, fail-closed readiness semantics.
- [x] Delete empty untracked `bahia/bahia/docs` residue dirs.

### Item 3 — SoulFactory + web docs
- [x] soul-factory.md, soul-factory-sidecar-runbook.md, soulfactory-runtime-control.md, openclaw-soulfactory-control-wrapper.md, openclaw-soulfactory-sidecar.md: canonical mutation handlers/projections, provisioning recovery + restart reconciliation, NIP-29 groups, NIP-42 retry, signet transport changes, bunker-secret removal.
- [x] web-app-setup.md, web-components.md, web-api-client.md, web-testing.md, WEB_APP_PRODUCTION_PLAN.md: subscription recovery + health introspection, LiteLLM config UI, souls UI reconciliation, EOSE/encrypted-result ordering, collection-cache snapshots.

### Item 4 — Ops docs (`docs/runbooks/`, `docs/rollout/`, `docs/specs/`)
- [x] runbooks: grafana-ws6.md, ws6-alerts.md, ws6-rollout.md vs current alert contract/Grafana assets (`8353df47`, `2f113953`, deploy/observability/); routstr-gateway.md spot-check.
- [x] rollout/ + specs/ (rest-deprecation-plan.md, ai-ml-public-spec-candidate-notes.md): verify still-accurate status statements; annotate rather than rewrite if superseded.

## Progress / deferred notes
(append here)
- 2026-08-01 Item 2 complete: refreshed README and scoped top-level core/operations docs against current code, commits, and beads; removed the dead LICENSE link (no root LICENSE exists); deleted the verified-empty nested `bahia/` residue; focused package tests and `git diff --check` passed.
- 2026-08-01 Item 1 complete: refreshed `docs/user-guide/**` against current commits, beads, CLI, MCP, relay, and web-route source; removed residual fake commands and additional stale Backup/DNS/Package/Service/ML examples; added and indexed Continuity, Events, and Environment States guides. Verification passed: all 160 registered `bahia_*` MCP names match the reference, relative links/fences are valid, and `git diff --check -- docs/user-guide` is clean. Deferred implementation-only gaps are documented rather than hidden: the standard app leaves external MCP tools fail-closed, direct MCP notification handlers are not organization-qualified, SoulFactory startup backfill is one newest request/action globally, and web `nav-model.js` still lacks contextual `docTopic` mappings for Continuity/Events (Environment States is not in main nav). Thus bead `bahia-qsfnh`'s user-guide content/index scope is covered, but its route-to-doc-reference scope is not fully covered under this docs-only assignment; no beads were closed.
- 2026-08-01 Item 4 complete: verified WS6 alerts/Grafana docs against the checked-in rules, fixtures, dashboard, telemetry, commits, and `fp-obs*` beads; documented the NIP-98 scrape boundary and the superseded custom Routstr gateway path; added point-in-time status annotations to the btc-01 proof, REST deprecation, and AI/ML candidate docs. Focused telemetry/alerting tests and `git diff --check` passed; `promtool` was not installed, so its checked-in rule fixtures were source-reviewed but not executed. No beads were closed.
- 2026-08-01 Item 3 complete: refreshed all ten assigned SoulFactory/OpenClaw/web docs against the referenced commits, beads, CLI/sidecar flags, `internal/soulfactory`, Signet, and `web/src`; corrected stale key-source, runtime-method, HTTP API, component, and test claims, and documented canonical projections, restart reconciliation, NIP-29/NIP-42, secret-safe Signet handoff, subscription health/recovery, EOSE/encrypted ordering, IndexedDB snapshots, LiteLLM secret refs, and draft/soul reconciliation. Verification passed for scoped diff whitespace, Markdown fences/relative links, and the full web suite (76 files, 599 tests). The scoped Go suite passed the Signet, wrapper, and sidecar packages but `internal/soulfactory` has an existing stale expectation: `TestOpenClawCommandDriverDefaultsToWrapperSupportedMethods` expects three default methods while production now also includes `soulfactory.update`. Deferred implementation gaps documented rather than hidden: SoulFactory startup request/action backfill remains one newest request/action globally, and its package-local MCP tools are not wired into the standard external MCP registry. No beads were closed.
