# LLM-Enabled UX Foundation (Phase 1) — Plan

> **Status**: Ready for implementation
> **Date**: 2026-05-16
> **Scope**: Conversational AI agent inside Bahia — event-native operational interface, MCP tool execution, plan-and-approve workflow, persistent sidebar UX

---

## Goal

Implement a conversational AI agent as a SoulFactory-provisioned Nostr participant that lets operators issue infrastructure commands in natural language. The agent plans with an LLM, executes only after explicit approval, operates exclusively through event-native MCP tool calls, and surfaces all actions as observable Nostr events in a persistent sidebar panel.

This is a targeted extension of Bahia's existing Nostr-native control-plane patterns — not a platform refactor.

## Background

### Existing Infrastructure to Reuse

**Async operational path** — The assistant's execution model already exists in Bahia:
1. UI publishes Nostr request → `publishRequest()` in `web/src/lib/nostr/controlplane-requests.js:75-99`
2. Backend reactor validates/authorizes → `internal/controlplane/reactor.go:448-542`
3. Coordinators execute long-running work → `internal/service/llm_provisioning_coordinator.go`, `ml_inference_provisioning_coordinator.go`
4. UI projects read models from correlated events → `web/src/lib/stores/controlplane.svelte.js:615-626`

**MCP tool catalog** — Broad existing coverage (`internal/mcp/server.go:1675-1909`). Newer ML/LLM/package tools already use async Nostr event publishing with correlation receipts (`internal/mcp/ml_tools.go:77-121`).

**SoulFactory provisioning** — Full lifecycle for agent identities: Signet keypair → profile → memory → runtime binding → Bahia service registration (`internal/soulfactory/provisioner_full.go:86-421`).

**Event correlation** — Established patterns: `["e", requestID, "", "reply"]` tags, resource-scoped tags, `d` tags for addressable commands (`internal/controlplane/reactor.go:1881-2085`).

**Approval gating** — Deployment and LLM deployment tools already have approve/reject workflows (`internal/mcp/server.go:2351-2389`).

### Key Decisions

1. **Agent identity**: SoulFactory-provisioned soul with full Nostr identity (Signet-managed keypair)
2. **Execution pattern**: Event-native only — all operations publish Nostr commands and observe results via events
3. **UI placement**: Persistent sidebar panel accessible alongside all existing views
4. **LLM provider**: Configurable OpenAI-compatible endpoint with direct API key as first-class path (not fallback); optionally route through Bahia-managed LLM route
5. **No streaming in Phase 1**: Discrete status/result events only (avoids parallel transport/state model)
6. **No DB tables in Phase 1**: Session state is Nostr-event-backed (replaceable read models + append-only timeline)
7. **Single-operator sessions**: Each session belongs to one operator pubkey; multi-operator approval is out of scope
8. **Signing fallback is primary path for Phase 1**: Service-signed downstream commands with `["agent", agent_id]` tag until Signet arbitrary-kind signing is validated

---

## Approach

### Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│  Web Sidebar (Svelte 5)                                         │
│  ┌──────────┐  ┌─────────────┐  ┌──────────────────────────┐   │
│  │ Composer  │  │ Plan Review │  │ Transcript/Timeline      │   │
│  └────┬─────┘  └──────┬──────┘  └──────────────────────────┘   │
│       │ publish 38420  │ publish 38421                           │
├───────┼────────────────┼────────────────────────────────────────┤
│  Nostr Relay Layer                                              │
├───────┼────────────────┼────────────────────────────────────────┤
│  Backend                                                        │
│  ┌────▼────────────────▼──────────────────────────────────────┐ │
│  │ Control-Plane Reactor (assistant_handlers.go)              │ │
│  └────┬───────────────────────────────────────────────────────┘ │
│       │                                                         │
│  ┌────▼──────────────────────────────────────────────────────┐  │
│  │ Assistant Orchestrator                                     │  │
│  │  ┌─────────────┐ ┌──────────────┐ ┌───────────────────┐  │  │
│  │  │ LLM Planner │ │Context Builder│ │ Tool Executor     │  │  │
│  │  └─────────────┘ └──────────────┘ └────────┬──────────┘  │  │
│  └─────────────────────────────────────────────┼─────────────┘  │
│       │ publish 31990/38422/38423               │                │
│       │                            publish 5961/5973/38391 etc   │
│  ┌────▼─────────────────────────────────────────▼────────────┐  │
│  │ Existing Control-Plane (deploy/LLM/ML coordinators)       │  │
│  └───────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

### Nostr Event Protocol

New dedicated namespace (does not overload SoulFactory lifecycle or existing control-plane commands):

| Kind | Name | Author | Semantics |
|------|------|--------|-----------|
| `31990` | Session read model | Service pubkey | Replaceable (`d=<session_id>`), canonical session state |
| `38420` | Prompt request | Operator browser key | Addressable (`d=assistant-turn:<session_id>:<turn_id>`) |
| `38421` | Plan approval | Operator browser key | Addressable (`d=assistant-approval:<session_id>:<plan_hash>`) |
| `38422` | Status | Service pubkey | Append-only, `e` reply to request |
| `38423` | Result | Service pubkey | Append-only, `e` reply to request, terminal |

**Correlation**: All assistant events carry `["session", <session_id>]`. Status/result use `["e", <request_event_id>, "", "reply"]`. Approval carries `["plan-hash", <hash>]` and `["decision", "approve|reject"]`.

**Session read model** (`31990`) carries: state, operator pubkey, assistant identity, last plan hash, pending steps, transcript summary. Tags: `["p", operator, "", "operator"]`, `["agent", agent_id]`, `["status", state]`.

### Session State Machine

```
idle
  → planning          (prompt received)
    → awaiting_approval  (plan generated, requires approval)
    → idle               (needs_clarification or read-only answer)
  → awaiting_approval
    → executing        (plan approved)
    → idle             (plan rejected)
  → executing
    → completed        (all steps terminal-success)
    → blocked          (relay closed before terminal result)
    → failed           (downstream terminal failure)
```

### Per-Session Concurrency

In-memory `sync.Mutex` keyed by `session_id`. Single-instance only in Phase 1 (no distributed lock). Concurrent prompts for the same session: second prompt blocks until first completes or the lock holder releases. Recovery runner also acquires the session lock before re-observing.

### Planning Flow

1. Operator publishes `38420` prompt request
2. Reactor validates auth, dispatches to orchestrator (async goroutine)
3. Orchestrator acquires per-session mutex
4. Context builder assembles bounded operational context
5. LLM planner called with JSON-schema-constrained output (schema defined below)
6. Plan validated against assistant-safe tool catalog (tool names exist, args match schema, no secrets)
7. `plan_hash = sha256(canonical_json(plan + session_id))`
8. Publish `38423 status=planned` with plan, update `31990 state=awaiting_approval`

### Approval/Execution Flow

1. Operator publishes `38421` with `plan_hash` and `decision`
2. Reactor validates, orchestrator acquires session lock
3. Verify `plan_hash` matches latest — reject stale approvals
4. On approve: execute steps sequentially
   - Derive idempotency key: `assistant:<session_id>:<plan_hash>:<step_id>`
   - Execute through assistant-safe tool catalog → get `AsyncToolReceipt`
   - Observe downstream status/result events (no synthetic timeout)
   - On terminal success → advance to next step
   - On relay-close before terminal → session `blocked`
   - On downstream failure → session `failed`
5. After all steps complete: publish `38423 status=completed`

### Duplicate/Replay Safety

- Same `d` tag prompt: no replan, republish existing result
- Duplicate approval for submitted plan: don't republish downstream commands
- Out-of-order status after terminal: ignore
- Recovery runner on restart: re-observe, never re-emit commands

---

## New Subsystems

### Backend (Go)

| File | Kind | Responsibility |
|------|------|---------------|
| `internal/domain/assistant.go` | Domain types | Kind constants, session/plan/step types, state enums, hash helpers |
| `internal/controlplane/assistant_handlers.go` | Request handlers | Parse/validate `38420`/`38421`, publish `31990`/`38422`/`38423`, bridge to orchestrator |
| `internal/service/assistant_orchestrator.go` | Coordinator | Per-session planning, approval/execution, state transitions, no-timeout observation |
| `internal/service/assistant_context_builder.go` | Helper | Bounded operational context from registries, workers, activity, transcript summary |
| `internal/service/assistant_session_recovery.go` | Background runner | *(Phase 1.5)* Startup re-observation of pending steps via relay backfill |
| `internal/soulfactory/operator_assistant_bootstrap.go` | Bootstrap | Ensure managed assistant soul exists, expose identity/signer to orchestrator |
| `internal/adapters/llm/chat_client.go` | Adapter | OpenAI-compatible chat completions for planning turns |
| `internal/mcp/agent_async_tools.go` | Tool catalog | Restricted assistant-safe action set + normalized `AsyncToolReceipt` contract |

### Frontend (Svelte)

| File | Responsibility |
|------|---------------|
| `web/src/lib/stores/assistant.svelte.js` | Session bootstrap, live subscriptions, transcript state, request publishing |
| `web/src/lib/components/assistant/AssistantSidebar.svelte` | Persistent sidebar shell, session list, composer |
| `web/src/lib/components/assistant/AssistantPlanApproval.svelte` | Plan review, step list, approve/reject actions |
| `web/src/lib/components/assistant/AssistantTurn.svelte` | Transcript entry rendering, downstream status badges |

### Modified Existing Files

| File | Change |
|------|--------|
| `internal/controlplane/reactor.go` | Register `38420`/`38421`, delegate to assistant handlers |
| `internal/adapters/nostr/subscriber.go` | Add assistant kinds to `DefaultInboundKinds` |
| `internal/adapters/nostr/publisher.go` | Add assistant kind constants |
| `internal/mcp/ml_tools.go` | Normalize async receipt to include `status_kinds`, `result_kinds`, `resource_tags` |
| `internal/mcp/server.go` | Wire assistant-safe catalog, expose internal dispatch path |
| `internal/app/app.go` | Wire bootstrap, orchestrator, recovery, LLM client, feature flag |
| `web/src/lib/nostr/client.js` | Assistant kind constants and parsers |
| `web/src/routes/+layout.svelte` | Mount persistent sidebar, pass route context |

---

## Assistant-Safe Tool Catalog

Phase 1 exposes **only** event-native tools backed by existing Nostr command/result flows:

| Action | Downstream Kind | Status/Result Kinds |
|--------|----------------|-------------------|
| Service deploy | `5961` | `6961` / `7961` |
| Service rollback | `5962` | `6961` / `7961` |
| LLM deploy | `5973` | `6973` / `7973` |
| LLM approval | `5974` | `7973` |
| LLM rollback | `5975` | `7973` |
| ML deploy | `38391` | `38396` |
| ML approval | `38392` | `38397` |
| ML rollback | `38393` | `38398` |

**Excluded** (D1-disabled or sync-only): ML recipe run (`38390`), ML model import (`38394`), all sync mutation tools.

### Normalized Receipt Contract

```go
type AsyncToolReceipt struct {
    ToolName        string
    RequestEventID  string
    RequestKind     int
    StatusKinds     []int
    ResultKinds     []int
    ReadModelKinds  []int
    DTag            string
    ResourceTags    map[string]string
    IdempotencyKey  string
    PublishedRelays []string
}
```

### Plan JSON Schema

The plan is the central data structure — defined in `internal/domain/assistant.go` and used by the LLM, backend, and frontend:

```go
type AssistantPlan struct {
    Summary            string              `json:"summary"`
    NeedsClarification bool                `json:"needs_clarification"`
    ClarifyingQuestion string              `json:"clarifying_question,omitempty"`
    RiskLevel          string              `json:"risk_level"` // "low", "medium", "high" — LLM-assessed
    ContextRefs        []string            `json:"context_refs,omitempty"`
    Steps              []AssistantPlanStep `json:"steps"`
}

type AssistantPlanStep struct {
    StepID         string         `json:"step_id"`
    Title          string         `json:"title"`
    Description    string         `json:"description"`
    ToolName       string         `json:"tool_name"`  // must exist in agent_async_tools catalog
    ToolArgs       map[string]any `json:"tool_args"`  // validated against tool input schema
    ArgsPreview    map[string]any `json:"args_preview,omitempty"` // human-readable subset for UI
    IdempotencyKey string         `json:"idempotency_key,omitempty"` // derived at execution time
}
```

`risk_level` is LLM-assessed based on: mutation scope (single service vs environment-wide), data-loss potential, production vs staging context. The UI renders this as a visual indicator — it does not gate approval.

---

## Security Model

### Trust Boundaries

- **Operator browser key** authors prompt/approval requests (`38420`/`38421`)
- **Bahia service key** authors session/status/result events (`31990`/`38422`/`38423`) AND downstream control-plane commands in Phase 1
- **Assistant soul identity** (SoulFactory-provisioned) is used for attribution (`["agent", <agent_id>]` tag on downstream commands) and session read model references — not for event signing in Phase 1
- **Future**: When Signet bunker arbitrary-kind signing is validated, downstream commands can be signed by the assistant soul key directly

**Service-as-agent distinction**: The control-plane reactor does not need to distinguish "service acting as itself" from "service acting on behalf of agent" in Phase 1. The `["agent", agent_id]` tag on downstream commands provides an audit trail. The assistant orchestrator is the only code path that attaches this tag.

### Authorization

- Assistant prompt/approval requests validated server-side using existing `isAuthorized` gate
- Assistant soul pubkey added to control-plane authorized-pubkey source for its allowed downstream kinds
- Tool catalog is allowlist-only — LLM cannot select tools outside the catalog
- Plan validation rejects disabled/excluded tools before publishing to the user

### Approval Semantics

- Every side-effecting plan requires explicit operator approval
- Read-only explanatory turns complete without approval gate
- `plan_hash` in approval prevents stale/mismatched execution
- No tool-arg editing in Phase 1 (approve full plan or reject)

---

## Operational Context Builder

Assembles bounded prompt context for the LLM planner. Token budgets are initial targets — tune based on target model context window during implementation.

**Sources queried:**
- Service registry (`internal/service/registry.go`) — services, environments, deployment intents/runs
- LLM registry (`internal/service/llm_registry.go`) — routes, releases, route state
- ML registry (`internal/service/ml_registry.go`) — models, endpoints, inference state
- Worker catalog — capabilities, health, runtime targets
- Recent activity from Nostr event repository (if wired)
- Transcript from `31990` session read model

**Route context schema from UI:**
```json
{"route": "/deployments/abc123", "params": {"id": "abc123"}, "resource_type": "deployment", "resource_id": "abc123"}
```

**Resource resolution:**
1. Explicit IDs/coords from `selected_refs` in `38420` — direct registry lookup
2. Route context resource type/ID — direct registry lookup
3. Natural-language entity extraction by LLM (during planning, not context building) — context builder provides registry summaries, LLM resolves references in its plan output
4. Ambiguous references → LLM returns `needs_clarification`

**Rules:** Never expose secret values. Only IDs, names, states, and timestamps.

---

## LLM Provider Integration

### `internal/adapters/llm/chat_client.go`

- OpenAI-compatible chat completions endpoint
- **First-class config**: `base_url` + `model` + `api_key` (direct external API — not a fallback)
- **Optional**: route through Bahia-managed LLM route if one exists and is healthy
- JSON-schema-constrained output for plan responses
- No streaming in Phase 1
- Failure modes:
  - HTTP/LLM error → `38423 status=failed`, no side effects
  - Invalid JSON → `needs_clarification`, no retry
  - Context too large → summarize once, then fail with `context_too_large`

**Bootstrap note**: The assistant needs an LLM to plan. Since deploying an LLM route is one of its own tools, the direct API key path must be functional before any Bahia-managed route exists. This is why direct config is first-class.

---

## Recovery and Observability

### Session Recovery — Deferred to Phase 1.5

The recovery runner (`assistant_session_recovery.go`) is complex and not required for initial launch. In Phase 1, sessions stuck in `executing` after a restart will show as `blocked` in the UI. Operators can start a new session. Recovery will be added as an incremental follow-up.

### Operator-Initiated Cancel

Phase 1 includes a cancel path: operator publishes `38421` with `decision=cancel` for any session in `executing`/`blocked`. This moves the session to `failed` and stops observation. No downstream rollback is attempted — the operator handles that manually or starts a new session.

### Tracing and Debugging

- All assistant actions produce observable Nostr events (`38422`/`38423`)
- Downstream command correlation preserved via `["downstream-request", <event_id>]` tags on assistant status events
- Session timeline fully replayable from relay history
- Token/cost accounting: LLM client tracks per-turn token usage, surfaced in `38423` result metadata

---

## UI Implementation

### Sidebar Architecture

- Persistent right sidebar in `+layout.svelte`, collapsible
- Main content width adjusts (not overlay in desktop)
- Open/collapsed state persisted in localStorage
- Current route path/params passed as context hints to prompt requests

### Bootstrap Flow (`assistant.svelte.js`)

1. Wait for auth init → get operator pubkey
2. Read `controlplaneConnection.servicePubkey`
3. Query `31990` sessions (service-authored, `#p=operator_pubkey`)
4. Query recent `38420`/`38421` (operator-authored)
5. Query recent `38422`/`38423` (service-authored)
6. Join by session ID → start live subscription

### Key UX Rules

- Never mark turns failed on client-side timeout — show "still waiting"
- Surface downstream request IDs and actual control-plane statuses
- Plan approval shows: summary, risk level, ordered steps with tool/args preview
- After reload, session reconstructs entirely from Nostr events

---

## Incremental Rollout

### Feature Flag

Assistant enabled via config flag in `internal/app/app.go`. When disabled:
- Reactor ignores `38420`/`38421` events
- No soul bootstrap
- No sidebar mounted
- Zero runtime cost

### Rollout Phases

1. **Protocol-only** — Land event kinds, domain types, protocol doc (no behavior)
2. **Planning-only** — Prompts produce plans but execution is disabled
3. **Full approval/execution** — Side-effecting plans with approval gate
4. **Recovery** — Restart-safe pending step observation

### Backward Compatibility

- No existing event kinds modified
- No DB schema changes
- No existing UI routes affected
- Sidebar is additive to layout
- MCP tool receipt expansion is additive (new fields only)

---

## Work Items

### Milestone 1: Foundation (Protocol + Tools + Identity)
1. Add `docs/operator-assistant-protocol.md` — canonical protocol contract including plan JSON schema
2. Add `internal/domain/assistant.go` — kind constants, types, state enums, plan schema definition
3. Add `internal/adapters/nostr/publisher.go` kind constants for assistant events
4. Add `web/src/lib/nostr/client.js` — assistant kind constants and parsers
5. Add `pstf/features/LLM_ENABLED_UX_FOUNDATION/` — feature spec, acceptance criteria
6. Add `internal/mcp/agent_async_tools.go` — restricted catalog + `AsyncToolReceipt`
7. Modify `internal/mcp/ml_tools.go` — normalize receipt fields (additive)
8. Modify `internal/mcp/server.go` — wire catalog, internal dispatch path
9. Add `internal/soulfactory/operator_assistant_bootstrap.go` — managed assistant soul
10. Validate Signet arbitrary-kind signing capability; if unavailable, confirm service-signed fallback works

### Milestone 2: Planning Backend
11. Add `internal/adapters/llm/chat_client.go` — planning LLM adapter (direct API key path)
12. Add `internal/service/assistant_context_builder.go` — bounded context assembly
13. Add `internal/service/assistant_orchestrator.go` — prompt → plan flow only
14. Add `internal/controlplane/assistant_handlers.go` — reactor integration
15. Modify `internal/controlplane/reactor.go` — register `38420`/`38421`
16. Modify `internal/adapters/nostr/subscriber.go` — add inbound kinds
17. Modify `internal/app/app.go` — wire bootstrap, orchestrator, feature flag

### Milestone 3: Execution Backend
18. Extend `assistant_orchestrator.go` — approval → execution → cancel flow
19. Modify `internal/app/app.go` — enable execution path, auth wiring

### Milestone 4: Web UI + Verification
20. Add `web/src/lib/stores/assistant.svelte.js` — session state store
21. Add `web/src/lib/components/assistant/AssistantSidebar.svelte`
22. Add `web/src/lib/components/assistant/AssistantPlanApproval.svelte`
23. Add `web/src/lib/components/assistant/AssistantTurn.svelte`
24. Modify `web/src/routes/+layout.svelte` — mount sidebar
25. Backend unit tests: planning, stale approval rejection, duplicate suppression, cancel
26. Web unit tests: session bootstrap, plan rendering, blocked state
27. E2E: prompt → plan → approve → control-plane command → result tracking
28. PSTF verification report

### Follow-up (Phase 1.5)
- Session recovery runner (`assistant_session_recovery.go`) for restart-safe pending step observation
- Token streaming via Nostr events for real-time plan generation UX
- Operator-editable plan steps before approval
- Multi-operator session sharing

---

## Risks

| Risk | Impact | Mitigation |
|------|--------|-----------|
| Signet bunker cannot sign arbitrary command events as assistant soul | Must use fallback signing from day 1 | Service-signed fallback is the **primary** Phase 1 path; soul identity is for display/attribution only |
| Async tool receipt normalization breaks existing callers | MCP regression | Additive fields only on existing receipt structs; no breaking signature changes |
| No LLM route exists to test against | Cannot test planning without external API | Direct API key config is first-class and tested first; Bahia-managed route is optional |
| Relay CLOSED/auth loss during execution | Stalled sessions | `blocked` state + operator-initiated cancel; recovery runner deferred to Phase 1.5 |
| LLM hallucinates invalid tool names/args | Bad plans reach user | Strict catalog validation before publishing plan; reject at planning time |
| Session stuck in `executing` forever (downstream never responds) | No automatic recovery | Operator cancel via `38421 decision=cancel`; automatic timeout deferred |

---

## Acceptance Criteria

1. No downstream control-plane command exists before explicit operator approval
2. Approved plans produce exactly one downstream Nostr command per step
3. Duplicate approval does not republish downstream commands
4. D1-disabled/excluded tools are rejected at planning time
5. Sidebar reload reconstructs session entirely from Nostr events
6. Relay CLOSED/auth interruption marks session `blocked`, not `failed`
7. Final assistant result accurately reflects downstream terminal result
8. All assistant actions visible as Nostr events — no hidden mutations
9. Operator can cancel a stuck session via `38421 decision=cancel`

---

## References

- `docs/plans/bahia-ai-ml-inference-fabric-2026-05-16.md` — Complementary ML event namespace (`38390-38399`, `31980-31989`)
- `docs/plans/soulfactory-nostr-agent-lifecycle-2026-05-14.md` — SoulFactory provisioning/lifecycle patterns
- `docs/plans/soulfactory-agent-customization-2026-05-15.md` — Agent customization (persona, voice, memory)
- `pstf/features/LLM_ROUTE_RELEASE_DEPLOYMENT/` — Verified LLM deployment slice
- `pstf/features/SOUL_FACTORY_RUNTIME_LIFECYCLE/` — Verified agent lifecycle slice
- `internal/mcp/ml_tools.go:77-121` — Event-native MCP receipt pattern to extend
- `internal/controlplane/reactor.go:294-542` — Reactor subscription/dispatch pattern to follow
- `internal/service/llm_provisioning_coordinator.go` — Long-running coordinator pattern to align with
