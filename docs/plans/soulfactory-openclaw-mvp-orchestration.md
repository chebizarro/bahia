# SoulFactory OpenClaw MVP Orchestration Plan

## Framing
Soul Factory remains Nostr-first. The absence of dedicated REST provisioning/lifecycle routes is intentional and must not be treated as a bug. The MVP target is: a signed Soul draft/provisioning request is processed by a live Bahia SoulFactory reactor, invokes OpenClaw through Nostr runtime events, publishes canonical SoulFactory status/result/read-model events, and avoids fake deployment artifacts.

## Work items

### [ ] Item 1 — Beads/PSTF skeleton + config/startup/app reactor wiring
**Goal:** Establish tracked work and wire SoulFactory into live Bahia startup behind explicit config.
**Done when:**
- `bd prime` has been run; relevant Beads issue(s) are created/claimed or existing ones are claimed.
- `pstf/features/SOULFACTORY_OPENCLAW_MVP/` exists with feature spec, acceptance criteria, test matrix, and initial verification notes.
- Additive `soul_factory` config exists and is disabled by default.
- When enabled with valid config, app construction wires a SoulFactory reactor/provisioner/OpenClaw runtime adapter; disabled config starts no reactor.
- Deterministic tests cover enabled/disabled and invalid config behavior.
**Key files/modules:** `internal/config/config.go`, `internal/app/app.go`, new `internal/app/soulfactory.go`, `internal/soulfactory/*`, `pstf/features/SOULFACTORY_OPENCLAW_MVP/*`.
**Dependencies:** none.

### [x] Item 2 — Reactor publication hardening + runtime/Bahia projection correctness
**Goal:** Make provisioning publication and Bahia projection production-safe: relay OK visibility, terminal ordering, runtime result propagation, no synthetic deployable artifacts.
**Done when:**
- `6950`, error `7950`, success `7950`, and `31951` are published to the normalized primary/additional relay set where appropriate and publish errors are not silently ignored.
- OpenClaw runtime result is returned/applied before final `31951` and terminal `7950`.
- Bahia service projection remains safe, and build/artifact/deployment intents are created only from real runtime artifact metadata including digest and explicit config opt-in.
- Deterministic tests cover relay targets, runtime success/failure ordering, and no fake artifact creation.
**Key files/modules:** `internal/soulfactory/reactor.go`, `internal/soulfactory/provisioner_full.go`, `internal/soulfactory/bahia_integration.go`, tests under `internal/soulfactory`.
**Dependencies:** Item 1 preferred, but reactor/Bahia pieces can proceed with care.

### [x] Item 3 — Workspace/Nostr client/web/docs alignment
**Goal:** Remove placeholder/hardcoded production-path SoulFactory config in touched areas and keep UX/client/docs aligned with Nostr-first MVP.
**Done when:**
- Workspace OpenClaw config generation uses config-supplied relays/controllers/model/secret references; invalid pubkeys fail instead of panic; no hardcoded production relay/controller placeholders remain in touched path.
- `internal/soulfactory/nostr_client.go` publishes provisioning requests with tags/content aligned with browser/event codec (`draft`, `draft-event`, `spec-hash`, runtime tags, schema/method fields).
- Web Soul creation remains Nostr-first and, if changed, only improves capability/progress clarity without adding REST provisioning calls.
- Docs explain the Nostr event flow and that REST lifecycle/provisioning routes are non-goals.
- Tests cover workspace config and client publish shape; relevant web unit tests are added/updated if web behavior changes.
**Key files/modules:** `internal/soulfactory/workspace.go`, `internal/soulfactory/nostr_client.go`, `web/src/routes/souls/new/+page.svelte`, `web/src/lib/stores/souls.svelte.js`, `docs/openclaw-soulfactory-sidecar.md`, `docs/soulfactory-runtime-control.md`, `docs/user-guide/features/souls.md`, `docs/user-guide/nostr-integration.md`.
**Dependencies:** Item 1 config shape may affect workspace.

### [ ] Item 4 — Verification, Beads closeout, commit/push
**Goal:** Verify the vertical slice evidence, update PSTF/Beads, and leave repo pushed/handoff-ready.
**Done when:**
- Focused tests have been run and exact commands/results are recorded in PSTF verification.
- Remaining real work is captured in Beads, not TODOs/comments/markdown task lists.
- No REST provisioning/lifecycle route was added; no fake/stub/hardcoded production-path behavior remains in touched scope.
- Changes are committed and pushed; `git status` shows branch up to date with origin.
**Key files/modules:** PSTF verification report, Beads, git state.
**Dependencies:** Items 1–3.

## Non-goals
- Do not add REST provisioning or lifecycle routes as the primary SoulFactory path.
- Do not introduce fake `agents/<id>:latest` or pseudo-digest deployable artifacts.
- Do not infer completion from timeouts, EOSE, CLOSED, or missing events.
- Do not route OpenClaw MVP through fake Bahia direct_runtime artifacts.

## Coordination notes
- Agents must read AGENTS.md/project guidance themselves.
- Keep changes minimal and vertical-slice oriented.
- If “max” requires a specific Bahia environment rather than OpenClaw runtime-running state, record as PSTF HITL/Beads follow-up instead of inventing semantics.
