# Bahia Assistant Agentic Upgrade — Plan Critique

> **Reviews**: `docs/plans/bahia-assistant-agentic-upgrade-2026-07-02.md`
> **Date**: 2026-07-02 · **Scope**: implementability only (4 axes below). Not a rewrite.

The plan is coherent and well-grounded in the current code. It is also ~2× larger than a first executable increment needs. Below: the seams too vague to build, the ordering hazards, what to cut, and the questions that reorder the work.

## 1. Top 3 under-specified seams

1. **Transcript encryption / recipient model (Phase 4, Open Q #1) — the real fork.** Spot-checked the plan's central assumption. NIP-44 primitives *do* exist (`internal/adapters/secrets/nip44.go`: `Encryptor.Encrypt/Decrypt`), but they are bound to the **secrets** subsystem — keyed off a secrets private key, with worker re-encryption and AES fallback — not a reusable *service→operator* helper. The assistant's own kind 30315/30900 events are **not** encrypted (no `Encrypt` calls in `assistant_orchestrator.go`), so there is no existing pattern to copy for a service-authored, operator-readable replaceable event. The one encrypted path that exists (`EncryptedRequestTransport`) is request/response-scoped to a single requester, not a broadcast event a *later* session can replay. The unspecified question isn't "does crypto exist" — it's **who is the recipient key of a 30316 event, and how does a fresh (possibly different-device) session decrypt its own history?** Per-recipient sealing breaks multi-device replay; a service-held symmetric key is a different design. This decides the kind-30316 tag/content schema, so it must be answered before Phase 1 freezes those constants — not deferred to Phase 4.

2. **Async-tool bridge: block vs. suspend (Phase 6) — the "core problem" left mechanism-free.** The plan reuses `observeDownstreamResult` to "await the terminal event and resume the loop," but never says whether the loop goroutine **blocks synchronously** for the full `max_wait` (holding an in-memory loop across a minutes-long mutation) or **suspends to `waiting_async` and resumes via recovery**. These are materially different implementations with different failure and concurrency profiles, yet the plan treats them as one line.

3. **Canonical cross-provider message representation (Phase 3).** `AgentModelClient.Next` returns "content blocks, tool_calls, stop_reason," but OpenAI (role=`tool` keyed by id) and Anthropic (`tool_result` blocks inside a user turn) thread tool results differently, and differ on multi-call-per-turn and streamed partial tool-args. The interface is specified; the **shared history/observation datamodel both adapters must serialize to** — where the actual risk lives — is not.

## 2. Contradictions & missing dependencies (13 items)

- **8 ↔ 12 circular claim.** Phase 8 says "ship atomically with frontend action-approval (Phase 12)," but 12 is ordered *after* 8. Either 8 lands dark behind the flag and the "atomic" ship is really at 12, or 12 moves before 8. State the true coupling; as written it reads as a cycle.
- **Permission metadata defined twice.** Phase 1 declares `assistant_permissions.go` *types* and Phase 2 puts `effect/risk/resource` on `ToolDescriptor`; Phase 5 evaluates them. Two owners of the same schema → drift. Assign one.
- **Memory replay has no consumer until 7/8.** Phase 4 wires 30316 replay into `assistant_context_builder.go`, but only the loop (7) and orchestrator (8) consume multi-turn history. Phase 4's "replay into real model history" is untestable end-to-end until 7/8; its listed tests are really 7/8 tests.
- **Phase 10 hidden deps.** Subagent-as-sync-observation needs the runtime (6) and loop (7); hook re-evaluation needs Phase 5's flow. Make 10 explicitly depend on 5+6+7.

## 3. Over-planning — cut for the first increment

The first increment only needs to prove the thesis: *a multi-step loop over async receipts, with real memory and a permission gate.* That is Phases 1–9 + minimal frontend (12) + E2E 1–4. Cut or defer:

- **Phase 10 (subagents/skills/commands/hooks) and Phase 11 (external MCP client) — cut entirely** from increment one. Four loaders + delegation + hook runner + a JSON-RPC MCP client are the whole second capability half and prove nothing the core loop doesn't. E2E scenario #5 (subagent) goes with them.
- **Anthropic adapter (Phase 3) — defer.** The system is OpenAI-compatible today. Build the interface + OpenAI adapter; keep `anthropic_agent_client.go` behind the same seam until the loop is proven. Halves Phase 3 and simplifies Phase 8's provider-selection config.
- **Permission modes (Phase 5) — ship 2, not 4.** `review` + `audited` cover the rollout (Open Q #2 already leans `review`-first). Drop `readonly`/`emergency` and arg-based risk-upgrade until a caller needs them.

## 4. Questions that change implementation order

1. **Does the loop block or suspend on async tools?** If suspend-and-recover, Phase 9 (recovery) is a *hard dependency* of 6/7 and should merge into them; if block, 9 stays a later crash-recovery add-on. Reorders the loop's core unit.
2. **First-rollout posture: `review` or `audited`?** (Open Q #2) If `review`, Phase 5 ships as a thin "ask all mutations" gate and all risk-scoring/`audited` logic moves *after* Phase 13 — off the critical path.
3. **Transcript recipient/decrypt model?** (Seam 1) Determines the kind-30316 schema — blocks *Phase 1* domain constants, not Phase 4.
4. **Is provider-agnosticism required in increment one, or just the seam?** If OpenAI-only ships first, Phase 3 and Phase 8 config both shrink.
