# Critique: LLM-Enabled UX Foundation Plan

> **Date**: 2026-05-16 · **Reviewed**: `docs/plans/llm-enabled-ux-foundation-2026-05-16.md`

---

## 1. Top 3 Under-Specified Seams

### 1a. Plan output JSON schema — nowhere defined

The entire planning → approval → execution pipeline depends on a plan structure that is never specified. "JSON-schema-constrained output" is mentioned (`LLM Provider Integration`) and the UI promises "ordered steps with tool/args preview" and a "risk level" — but there is no schema for what a plan *is*. What fields does a step have? How are tool arguments represented? Where does "risk level" come from — LLM-generated or derived?

**Impact**: Milestones 4, 5, and 6 all depend on this shape. It should be part of Milestone 1 (Protocol Foundation), not discovered during implementation.

### 1b. Per-session lock — mechanism unspecified

"Orchestrator acquires per-session lock" appears in both the planning flow (line ~120) and the approval/execution flow (line ~131) with no further detail. In-memory mutex? What happens when two `38420` prompts arrive for the same session concurrently? What about the recovery runner — does it also acquire this lock? Since there are no DB tables and no distributed coordination, the lock semantics constrain everything about concurrent session behavior.

### 1c. Context Builder resource resolution

The Operational Context Builder says `explicit IDs/coords from UI → exact name match → ambiguity returns needs_clarification`, and that's the entire resolution spec. But:
- How does the builder extract entity references from a natural-language prompt?
- What registries/stores does it query (services? workers? LLM routes? all three?)?
- "Route context hints from UI" (~200 tokens) — what is the schema of these hints?

The token budgets are premature without knowing the target model's context window or how resources are actually selected. These numbers will change on contact with reality.

---

## 2. Contradictions & Missing Dependencies

**Circular bootstrap**: The default LLM provider is "Bahia-managed LLM route endpoint (if configured)", but deploying an LLM route is one of the assistant's own tools (`5973`). If no route exists yet, the assistant cannot plan. The chat client must support a direct external API key as first-class config, not a fallback — otherwise Milestone 4 is blocked by a pre-existing deployment.

**Authorization gap on signing fallback**: The security model adds the assistant soul pubkey to the authorized-pubkey source. But the acknowledged fallback path is *service-signed* commands with an `["agent", agent_id]` tag. In that case the author is the service key (already authorized), so the soul pubkey addition is irrelevant. The plan doesn't specify how the control-plane distinguishes "service acting as itself" from "service acting on behalf of agent" — or whether it should.

**Misplaced work item**: Item 25 (add kind constants to `publisher.go`) is a backend constant addition filed under Milestone 6 (Web UI). It's a protocol artifact and belongs in Milestone 1.

---

## 3. Over-Planning — Cut or Simplify

**Session recovery (Milestone 5, item 18)**: The plan's own rollout phases list recovery as Phase 4. It's complex (relay backfill queries, re-observation, never-re-emit invariant) and Phase 1 can ship without it — `blocked` state already handles the user-visible case. Defer to Phase 1.5 or Phase 2.

**Session read model `31990`**: The plan commits to a replaceable Nostr event as canonical session state *and* requires the frontend to reconstruct from the event timeline (acceptance criterion 6). These are redundant. In Phase 1, the append-only timeline (`38422`/`38423`) is sufficient; the frontend already projects state from it. Drop `31990` or mark it as an optimization to add after the core loop works. This also simplifies the recovery runner (one fewer event kind to reconcile).

**7 milestones is too many for Phase 1**: Milestones 1–3 (Protocol, Tool Contract, Identity/Auth) are all small prerequisites. Merge them into a single "Foundation" milestone. This reduces coordination overhead and avoids the illusion that three sequential PRs are needed before real work starts.

---

## 4. Questions That Would Change Implementation Order

1. **Does Signet bunker sign arbitrary event kinds today?** If no, the soul-signing path is fiction for Phase 1, the fallback *is* the path, and Milestone 3 collapses into "register a soul identity for display purposes only." Start Milestone 4 sooner.

2. **Is there a deployed LLM route the assistant can target right now?** If no, the chat client must ship with direct-API-key support first. This makes the LLM adapter (item 11) a prerequisite for *testing* Milestone 4, not just a dependency — move it earlier or parallelize.

3. **Can multiple operators interact with the same assistant session?** The read model tags `["p", operator, "", "operator"]` (singular). If multi-operator is out of scope, say so explicitly — it simplifies subscription filters and approval authorization. If in scope, the auth model for cross-operator approval is missing.

4. **What happens to a session stuck in `executing` forever?** The plan says no synthetic timeouts, and `blocked` only triggers on relay CLOSED. A downstream tool that silently never responds leaves the session permanently executing with no recovery path (the recovery runner only runs on restart). Is this acceptable for Phase 1, or does it need an operator-initiated cancel?

---

*End of critique. No scope expansion proposed — only deletions, clarifications, and questions.*
