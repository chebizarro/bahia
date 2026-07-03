# Bahia Assistant — Agentic Upgrade Plan

> **Status**: Draft
> **Date**: 2026-07-02
> **Scope**: Evolve the in-app Bahia Assistant from a single-shot JSON planner into a provider-agnostic, multi-step agentic loop with persistent memory, subagents, skills, hooks, a tiered permission model, and an upgraded MCP surface (broader tools, external MCP clients, agent-discoverable schemas).

---

## Goal

Turn the Bahia Assistant into a genuinely agentic operator: it should hold real conversation memory across turns, reason in a multi-step tool-use loop (call a tool → read the result → decide the next step), delegate sub-tasks to focused subagents, and be extensible through skills/commands and hooks — while preserving Bahia's Nostr-native, auditable, approval-aware execution model. The [claude-code](../../claude-code) harness conventions are the design template; the Bahia MCP is upgraded in step to feed the loop.

This is an evolution of the [LLM-Enabled UX Foundation](./llm-enabled-ux-foundation-2026-05-16.md) (Phase 1), not a rewrite.

## Background

### Current state (what Phase 1 built)

- **Transport is Nostr/ContextVM, not REST.** Frontend publishes encrypted `assistant/prompt` / `assistant/approval` operations (`web/src/lib/stores/assistant.svelte.js:590-632`); backend handles them via `internal/controlplane/assistant_handlers.go:18-42` → `AssistantOrchestrator.HandlePromptRequest`.
- **Single-shot JSON planning.** The LLM returns `steps[].tool_name/tool_args` through `response_format` (`internal/adapters/llm/chat_client.go:337-371`); steps are validated against a `bahia_assistant_*` allowlist (`internal/service/assistant_orchestrator.go:778-799`) and executed only after approval (`:369-414`).
- **No real memory in the prompt.** The LLM sees only `operator_prompt`, route context, selected refs, and a truncated append-only `TranscriptSummary` — not full turn history (`assistant_orchestrator.go:206-212, 965-968`; `internal/domain/assistant.go:62-65`).
- **State is Nostr-event-backed.** `AssistantSession` is serialized into kind `30900` events; no dedicated DB tables exist (`internal/domain/assistant.go:9-19, 48-67`; `assistant_orchestrator.go:801-822`). Status/results stream back as kind `30315` events.
- **LLM adapter is OpenAI-compatible chat-completions only.** Concrete `ChatClient` (`chat_client.go:48-75`); request body has no `tools`/`tool_choice` (`:261-275`); streaming carries text deltas only (`:188-258`). No SDK dependency; a hand-rolled Anthropic HTTP call exists in `internal/adapters/llm/generator.go`.
- **MCP is a large in-process Go registry.** `Tool{Name, Description, InputSchema}` (`internal/mcp/server.go:212-228`); `bahia_assistant_*` tools are **async** — they publish command intents and return correlation receipts, not terminal results (`internal/mcp/agent_async_tools.go:13-131`; `internal/domain/assistant.go:113-126`). Read tools (`bahia_list_*`) resolve synchronously via `Server.CallTool`. One narrow external MCP **client** already exists: `internal/adapters/agentmemory/client.go`.

### claude-code templates to borrow (conventions, not code)

- **Subagents** = Markdown + YAML frontmatter (`name`, `description`, `model`, `tools`); `description` doubles as the invocation trigger (`claude-code/plugins/plugin-dev/skills/agent-development/SKILL.md:53-147`).
- **Skills** = `SKILL.md` directory packages with `name`/`description` frontmatter + progressive-disclosure body (`.../skill-development/SKILL.md:25-44`).
- **Commands** = Markdown prompt files with optional `allowed-tools`/`model`/`argument-hint` frontmatter (`.../command-development/references/frontmatter-reference.md:1-112`).
- **Hooks** = JSON event handlers: `PreToolUse`, `PostToolUse`, `Stop`, `SubagentStop`, `UserPromptSubmit`, `SessionStart/End`, etc. (`.../hook-development/SKILL.md:120-272`).
- **Permissions** = JSON policy with `ask`/`deny`/`allow` rules and sandbox controls (`claude-code/examples/settings/settings-strict.json`).

### Decisions locked with the user

1. **Adopt the full capability set** — agentic loop, persistent memory, subagents, skills/commands, hooks, richer permissions.
2. **Execution model: autonomous-with-audit, approval configurable.** Not hard-gated on every mutation; approval becomes a tier/policy, not a mandatory step.
3. **Provider-agnostic model layer** — one loop abstraction over both OpenAI-compatible native tool-calling and Anthropic tool-use.
4. **MCP upgrade is three-pronged** — broaden `bahia_assistant_*`, add a client to consume external MCP servers, and restructure tool registration/schemas for clean agentic discovery.

## Approach

**A targeted refactor of the assistant subsystem, not a platform rewrite.** Keep everything that gives Bahia its value — Nostr/ContextVM transport, encrypted control-plane ops, kind `30900` session read-models, kind `30315` status/audit events, and event-native MCP mutation receipts. Replace only the brain: swap the single-shot `AssistantPlan` planner+executor for a resumable, provider-agnostic **agent loop**. The legacy plan/approve path stays behind a config flag so rollout is reversible (no DB migrations required).

### 1. The agent loop and the async-tool bridge (the core problem)

A claude-code-style loop is synchronous (call tool → see result → continue), but Bahia's mutation tools are **async receipts**: they publish a command intent and the terminal result arrives later as a correlated Nostr event. The resolution is a new `AssistantAgentLoop` (`internal/service/assistant_agent_loop.go`) whose tool runtime (`internal/service/assistant_tool_runtime.go`) always returns one normalized `AssistantToolObservation`, hiding the sync/async split from the model:

- **Read-only sync tools** (`bahia_assistant_dns_list_*`, future `*_list`/`*_get`): run inline via `mcp.Server.CallTool`, observation fed straight back to the model.
- **Async mutation tools** (allowed by policy): invoke existing `InvokeAssistantAsyncTool`, get the `AsyncToolReceipt`, then **suspend** — persist loop state as `waiting_async` and release the goroutine rather than block for a minutes-long mutation. The same recovery path (`observeDownstreamResult` backfill-then-subscribe on `receipt.ResultKinds` + `e=RequestEventID`, extended in `assistant_session_recovery.go`) resumes the loop whether the terminal event arrives live, on backfill, or after a process restart — so crash-recovery is intrinsic to the loop, not a bolted-on later phase. The model perceives a single tool result that simply took a while.
- **Approval-required actions** (policy returns `ask`): the runtime does **not** execute; it creates an `AssistantDeferredAction`, moves the session to `awaiting_approval`, and emits an `approval_required` status. The extended `assistant/approval` op (now keyed by `action_id`, not just `plan_hash`) resumes the loop on approve, or feeds a denial observation back to the model on reject.
- **Fail-closed semantics preserved:** relay close / `max_wait` timeout / crash → session `blocked` (not lost); `assistant_session_recovery.go` is extended to resume loops in `waiting_async` state from receipt metadata.

Loop state (`RunID`, `Iteration`, `State`, `PendingActionID`, failure counters) lives in `AssistantSession.Metadata["agent_loop"]`; guards for `max_iterations` and `max_consecutive_tool_failures` bound runaway loops. Every material transition publishes a kind `30315` status with a `phase` field (`tool_call_requested`, `tool_submitted`, `tool_observed`, `approval_required`, `subagent_started`, `loop_completed`, …) — old clients still render the legacy `message`.

### 2. Provider-agnostic model layer

Replace the `PlanFromPrompt`/`response_format` planner with a new `AgentModelClient` interface (`internal/adapters/llm/agent_client.go`): a `Next(ctx, req, onEvent)` call taking messages + tool schemas + `tool_choice` and returning content blocks, `tool_calls`, and a `stop_reason`. Two implementations behind it — `openai_agent_client.go` (native `tools`/`tool_calls` on `/v1/chat/completions`, reusing the existing `ContextTooLargeError` handling) and `anthropic_agent_client.go` (adapting the HTTP pattern already in `generator.go` for `/v1/messages` `tool_use`/`tool_result` blocks). Provider is selected via new `assistant.agentic` config; `chat_client.go` stays as the legacy adapter during rollout. **Increment one ships the interface, its canonical cross-provider message/observation datamodel (how tool results serialize, multi-call-per-turn), and the OpenAI adapter only**; `anthropic_agent_client.go` sits behind the same seam, deferred until the loop is proven.

### 3. Persistent memory (three layers)

- **Durable transcript** — a new service-authored append-only event kind `30316` (`bahia.assistant-transcript.v1`) tagged by session/turn/role/seq, carrying full user/assistant/tool messages. Content is **encrypted with a service-held symmetric key** so any operator session — including a new device — can retrieve the key and replay its own history (a distinct key-custody model from the per-requester `EncryptedRequestTransport`). A new `assistant_transcript_store.go` publishes and replays these into real multi-turn model history — replacing `TranscriptSummary` as the sole memory source.
- **Session snapshot** — kind `30900` stays a compact read-model (state, participants, loop metadata, pending receipts/action, transcript cursor). Full messages never bloat it.
- **Long-term semantic memory** — the existing `internal/adapters/agentmemory/client.go` becomes optional post-turn semantic memory (facts/preferences with provenance, never secrets), fetched at turn start via the generic MCP client.

### 4. Subagents, skills, commands, hooks (claude-code conventions, server-side)

All four are loaded from configured directories as markdown + YAML frontmatter, matching the harness templates:

- **Subagents** (`assistant_subagents.go`): `name`/`description`/`model`/`tools` frontmatter → child agent loops invoked via an internal `bahia_assistant_delegate_subagent` tool; child tool set intersected with global policy; results returned as a sync observation; `SubagentStop` hooks gate acceptance.
- **Skills** (`assistant_skills.go`): `SKILL.md` packages with progressive disclosure — only names/descriptions injected at turn start; body loaded on demand via a read-only `bahia_assistant_skill_load` tool, paths confined to skill roots.
- **Commands** (`assistant_commands.go`): markdown prompt templates (`description`/`allowed-tools`/`model`/`argument-hint`) expanded into the initial user message before `UserPromptSubmit` hooks — not tools.
- **Hooks** (`assistant_hooks.go`): `UserPromptSubmit`/`SessionStart`/`SessionEnd`/`PreToolUse`/`PostToolUse`/`Stop`/`SubagentStop`. Handler types are `prompt` and `mcp-tool` (read-only) first; **no arbitrary shell** until a sandbox runner exists. Security order: hard-deny rules → hooks → re-evaluate permissions on any hook-modified input → execute. Hooks can never upgrade a deny to allow.

### 5. Tiered permission model (implements "autonomous-with-audit")

A policy engine (`assistant_permissions.go` + domain types) evaluates `allow`/`ask`/`deny` from config rules against tool descriptors carrying `effect` (read/mutation), `execution_mode`, `resource_types`, and `default_risk`; risk is upgraded from args (production/rollback/delete → high). Modes: `review` (legacy: ask all mutations) and **`audited`** (allow reads + low/medium-risk scoped mutations, ask high-risk/destructive, deny forbidden — the autonomous-with-audit default). Approval stops being mandatory per mutation; every action still emits audit events. **Increment one ships these two modes only**; `readonly`/`emergency` and arg-based risk-upgrade are deferred until a caller needs them.

### 6. MCP upgrade (three prongs)

- **Restructure for discovery** — wrap existing tools in a `ToolDescriptor` registry (`internal/mcp/registry.go`) carrying execution-mode/effect/risk/resource metadata; `GetTools()`/`CallTool()` keep their public shape but the loop discovers tools through an `AssistantToolRegistry` that merges Bahia MCP descriptors + internal tools + external MCP + permission metadata.
- **Broaden the `bahia_assistant_*` surface** — add read tools first (`service_list/get`, `llm_route_list`, `ml_endpoint_list`, `worker_list`, `docs_read`, `package_list`, `backup_status`), then async mutation wrappers only where backed by observable command publishers (packages, policy, backup, worker, artifact, deployment approval). No direct registry mutation tools for the agent.
- **Consume external MCP servers** — a new generic `internal/adapters/mcpclient` (JSON-RPC `initialize`/`tools/list`/`tools/call`, auth, timeouts, tool-name prefixing). External MCP is **disabled by default**; each server needs explicit permission rules. `agentmemory` migrates onto this client while keeping its typed helpers.

## Work Items

Two increments. **Increment 1 proves the thesis** — a multi-step loop over async receipts, with real memory and a permission gate — and is the entire critical path. **Increment 2** adds the extensibility surface once the core is trusted. All backend lands behind `assistant.agentic.enabled=false`; enablement ships atomically with the minimal frontend in item 9.

### Increment 1 — the agentic core

1. **Domain + config foundation** — `internal/domain/assistant_agent.go` (loop state, `AssistantToolObservation`, `AssistantDeferredAction`); `assistant_permissions.go` **owns** the permission/effect/risk types (item 2 descriptors only carry values — one schema owner); transcript kind `30316` constants (service-held-symmetric-key envelope in content, key-reference/rotation metadata in tags — see Resolved Decisions); `action_id`/`cancel_scope` on `AssistantApprovalRequest`. Extend `AssistantConfig` (`internal/config/config.go`) with `agentic` (OpenAI-only first), `permissions`, and `mcp.async_observation` blocks + validation. *Tests: config validation, JSON back-compat.*
2. **Tool descriptor registry** — `internal/mcp/registry.go` descriptors (execution-mode/effect/risk/resource, referencing item 1's types) for existing assistant/DNS/service/LLM/ML tools; `AssistantToolRegistry` discovery. *Tests: mode/effect/risk correctness.*
3. **Model client — interface + OpenAI adapter** — `agent_client.go` interface **and its canonical cross-provider message/observation datamodel** (tool-result serialization, multi-call-per-turn); `openai_agent_client.go` native tool-calling. `anthropic_agent_client.go` stubbed behind the seam, deferred to increment 2. *Tests: mocked text / tool-call / malformed / context-too-large.*
4. **Permission engine — `review` gate** — `assistant_permissions.go` evaluating `review` (ask all mutations) as the enablement default; `audited` mode + arg-based risk-scoring are scaffolded behind the same interface but graduate in item 12. *Tests: read-allow, mutation-ask, deny.*
5. **Async tool-runtime bridge + suspend/resume** — `assistant_tool_runtime.go` normalizing sync results, async receipts, and deferred/denied calls into observations; the **suspend-to-`waiting_async` + recovery-resume** mechanism (reusing `observeDownstreamResult`, extending `assistant_session_recovery.go`) is built here — recovery is not a separate later phase. *Tests: sync, async success/failure, relay-closed→blocked, restart-resumes, duplicate receipt.*
6. **Transcript store + memory** — `assistant_transcript_store.go` publish/replay of kind `30316` (encrypted with the service-held symmetric key); wire replay into `assistant_context_builder.go`, replacing `TranscriptSummary` as the sole memory source. *End-to-end testable only with item 7; tested together.*
7. **Agent loop** — `assistant_agent_loop.go`: iteration loop, `max_iterations`/`max_consecutive_tool_failures` guards, deferred-approval suspend/resume, stop condition. Consumes items 3, 5, 6. *Tests: read→continue, async→suspend→resume→continue, ask→approve→execute, reject→denial-observation, memory replay across turns.*
8. **Orchestrator integration (flag-off)** — route agentic prompts through the loop in `assistant_orchestrator.go`; extend `HandleApprovalRequest`/`assistant_handlers.go` for `action_id`; keep the legacy path. Lands dark behind the flag.
9. **Minimal frontend + enablement** — `web/src/lib/nostr/assistant.js` parse `30316` + status `phase`/action fields; `assistant.svelte.js` action-level approval + `publishAssistantActionDecision`. This is the slice that makes item 8 exercisable, so **enablement is atomic here**. *E2E: (1) read-only DNS question, no approval; (2) low-risk mutation auto-runs in `audited` and awaits the Nostr result; (3) high-risk rollback asks; (4) relay close → blocked → recovery resumes.*

### Increment 2 — extensibility (after the core is proven)

10. **Subagents, skills, commands, hooks** — four markdown-frontmatter loaders + delegation runner (returns a sync observation via item 5) + hook runner (`prompt`/`mcp-tool` handlers only; no shell). Depends on items 4+5+7. *Tests: invalid frontmatter, tool-restriction intersection, PreToolUse ask/deny, Stop block; E2E subagent delegation.*
11. **Generic external MCP client** — `internal/adapters/mcpclient` (JSON-RPC `initialize`/`tools/list`/`tools/call`); merge external tools into the registry with prefixes (disabled by default, per-server permission rules); migrate `agentmemory`. *Tests: mocked server, timeout, name collision, permission denial.*
12. **Anthropic adapter + richer UX + graduation** — fill in `anthropic_agent_client.go`; richer transcript/subagent rendering; graduate default posture from `review` to `audited`; add `readonly`/`emergency` modes and arg-based risk-upgrade if needed.

## Resolved Decisions

- **Transcript encryption: service-held symmetric key.** Kind `30316` content is encrypted with a **service-held symmetric key** (not per-recipient sealing) so any operator session, including a new device, can retrieve the key and replay its own history. Item 1 freezes the `30316` schema around this: a symmetric-key AEAD envelope in `content`, key-reference/rotation metadata in tags. NIP-44 primitives (`internal/adapters/secrets/nip44.go`) can inform the AEAD choice, but the key source is service-held, not secrets-subsystem-derived.
- **First-rollout posture: `review`, graduating to `audited`.** Item 4 ships `review` (ask all mutations) as the enablement default; the `audited` autonomous-with-audit posture and its risk-scoring graduate in item 12 once the audit trail is trusted.

## References

- Prior art: [LLM-Enabled UX Foundation Phase 1](./llm-enabled-ux-foundation-2026-05-16.md)
- Harness template: `claude-code/plugins/plugin-dev/skills/` (agent/skill/command/hook development)
- Existing external MCP client: `internal/adapters/agentmemory/client.go`
