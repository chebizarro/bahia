# SoulFactory Nostr Agent Lifecycle — Plan Critique

> **Date**: 2026-05-14
> **Scope**: Critique of `docs/plans/soulfactory-nostr-agent-lifecycle-2026-05-14.md`

---

## 1. Top 3 Under-Specified Seams

**1a. Authorization model for `38384` control requests.**
The plan says OpenClaw "validates controller allowlists" (Work Item 8) and Metiq reuses its existing control bus, but never defines: Who signs `38384`? Is it Bahia's operator key, the soul's own key, or the SoulFactory service key? How does the runtime decide to trust it? The Swarmstr control bus (`swarmstr/internal/nostr/runtime/control_bus.go:134-767`) already has its own auth model — the plan doesn't say whether Bahia adopts it, wraps it, or introduces a third trust root. Without this, Work Items 7/8/9 will each invent their own answer.

**1b. `31952` ↔ `31951` reconciliation on conflict.**
The plan makes `31952` the "canonical editable spec" and `31951` the "authoritative latest read model" but never defines what happens when they diverge — e.g., an operator edits a draft while a provisioning run is in flight, or a runtime publishes state that contradicts the draft. There's no version/sequence tag, no optimistic-locking mechanism, and no stated winner. This will bite Work Items 5 and 6.

**1c. `soulfactory.*` method contract.**
Six RPC methods are named (`provision`, `update`, `suspend`, `resume`, `redeploy`, `revoke`) but their request/result payloads, error shapes, and idempotency keys are unspecified. Both the OpenClaw (TypeScript) and Metiq (Go) bridges must serialize and deserialize these identically. Without a shared schema (even a prose one), the bridges will drift on day one.

## 2. Contradictions & Missing Dependencies

- **Work Item 6 depends on Work Item 7, but the ordering implies otherwise.** Item 6 says provisioning step 8 includes "runtime binding," which requires a runtime adapter (Item 7). But Item 6 is sequenced before Item 7. Either Item 6's deploy step must be stubbed, or the items need reordering/splitting.
- **"No REST control APIs" vs. Bahia service/deploy fields.** The goal says "no REST control APIs," but the plan references Bahia registration/deployment intent creation (Work Item 6, step 8) without saying how deployment happens. If Bahia's deploy infra is REST-based today, this is a contradiction the plan ignores.
- **Missing dependency: relay bus (Item 3) is load-bearing for Items 5–9** but there's no acceptance criterion that validates the bus in isolation before dependent items begin. The vertical slice (Item 11) is too late — a broken bus blocks everything.

## 3. Over-Planning — Cut or Simplify

- **Voice/avatar/memory in `31952` (State Model section).** The plan punts voice schema to Open Questions but still bakes voice, avatar synthesis policy, and ContextVM/agent-memory options into the draft spec. These are pure UX sugar with no protocol dependency. **Cut them from the initial spec; add as additive tags later.** This simplifies Items 2, 6, and 10 significantly.
- **`6951` / `1951` formalization (Protocol Shape).** The plan introduces two new event kinds for lifecycle action progress/result. The existing `6950`/`7950` pattern already covers request→status→result. Unless lifecycle actions have semantically different progress shapes than provisioning, **reuse `6950`/`7950` with a discriminator tag** and avoid new kinds, new parsers, and new UX plumbing.
- **Work Item 10 (full agent studio UX) is premature.** It's the largest item and the hardest to verify. A minimal "provision from draft, show status" UX is sufficient for the vertical slice. **Defer the studio redesign until the protocol is proven end-to-end.**

## 4. Questions That Would Change Implementation Order

1. **Does OpenClaw already have, or plan to have, a Nostr transport?** If not, Work Item 8 (OpenClaw bridge) is the critical-path risk and should move before Items 3–6, not after. If it does, the plan's ordering is fine.
2. **Is the Bahia deploy step (registering services, creating deployments) currently REST-only?** If yes, the "no REST" constraint either needs an escape hatch for internal infra or a Nostr-native deploy protocol — neither of which is in scope. This answer determines whether Item 6/step 8 is feasible as written.
3. **Will both runtimes be developed by the same team on the same timeline?** If not, the "common method vocabulary" (Section: Runtime Integration) needs a versioned spec artifact, not just a list in a plan doc. This changes whether Item 7 is a thin adapter or a full protocol contract project.
