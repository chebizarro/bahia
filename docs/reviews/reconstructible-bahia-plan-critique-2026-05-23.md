# Plan Critique: Reconstructible Bahia

**Date:** 2026-05-23
**Scope:** `docs/plans/reconstructible-bahia-2026-05-23.md` vs. Oracle export `prompt-exports/oracle-plan-2026-05-23-140723-relay-canonical-plan-dffe.md`

---

## 1. Top 3 Under-Specified Seams

### 1a. `DecodedProjectionEvent` — the dispatch boundary nobody defined

The plan (Section C) introduces `RelayProjectionCache` with "idempotent cache mutation from decoded relay read models into existing repositories" and says it derives `stream`+`entityKey` from kind+d-tag. But neither document defines the `DecodedProjectionEvent` type shape, how kind→family dispatch works, or which decode functions produce it. The export (line 560) calls it "a tagged union over supported kinds/families… intentionally an internal service type" but stops there. An implementer building `relay_projection_cache.go` must invent the tagged-union design, the decode-dispatch registry, and decide whether each family gets its own applier method or a generic one. This is the load-bearing seam between the bootstrapper and all downstream cache writes.

**Impact:** Items 10, 11, and 15 all depend on this type existing. Getting its shape wrong forces a rewrite across all three.

### 1b. Bootstrap timeout and failure semantics

The plan's bootstrap algorithm (Section B) says "Wait for EOSE on required snapshot groups" and "When required live groups reach EOSE, mark tier ready." Neither document specifies:
- What timeout applies if EOSE never arrives (relay has no events, relay is slow, relay drops connection mid-stream)
- Whether a partial EOSE (some groups complete, some don't) can promote to a lower tier
- Whether bootstrap failure leaves Bahia in a zombie state (HTTP up, `/ready` 503 forever) or triggers retry/shutdown

The export's `BootstrapPhase` enum includes `BootstrapPhaseFailed` but doesn't define what transitions into or out of it. An implementer of `bootstrapper.go` (Item 10, marked "Large") will have to design the entire failure/retry policy from scratch.

### 1c. Round-trip fidelity gap — which kinds are lossy?

The export (line 600) flags a critical validation: "For any family where current replaceable payload is not lossless enough to reconstruct the repo row, implementation must first extend the canonical payload." The plan's Item 15 mentions "round-trip fidelity validation per kind" as a done-when criterion but doesn't acknowledge this as a *sizing risk*. If many of the 82+ kinds have lossy payloads, Items 15–17 balloon from "implement cache appliers" to "redesign canonical event schemas first" — a fundamentally different and larger task. Neither document audits which families are lossy today.

**Impact:** Could double the size of Items 15–17 and force reordering (schema extension must precede cache appliers).

---

## 2. Specificity Balance

### Over-specified (plan should defer to implementer)

- **Tier table contents** (plan Section A): Assigning every subsystem (LLM, ML, packages, OCI, HiveCI, assistant, tool provisioning, blossom) to a specific tier is a tactical choice that will shift as implementation reveals which families are ready. The plan should define the tier *model* and *rules* but let implementers assign families based on what's actually feasible.

- **Relay quorum numbers** (plan Section F): "full=2/3, degraded=1+, emergency=1" are operator-tunable constants, not architecture. Hardcoding them in the plan risks confusion about whether they're configurable or structural.

### Useful framing the plan dropped from the export

- **"Reusable code to preserve" inventory** (export lines 229–238): The export explicitly lists six existing code paths to reuse (subscriber cursor logic, relay pool EOSE handling, continuity stores, projector serializers, BackgroundManager). The plan assumes this but doesn't call it out, making it easy for an implementer to accidentally duplicate rather than extract.

- **"Biggest architectural blocker" framing** (export line 183): The export explicitly names the authority inversion as the single biggest blocker — "if a DB write succeeds and relay publish fails, the DB remains 'truth'." The plan distributes this across Sections B, C, and D without surfacing the core tension in one place.

- **Validation-required file callouts** (export lines 1103–1111): The export explicitly flags that service constructor files for `RegistryService`, `LLMRegistryService`, and backup/package mutations were *not in the selected context* and must be located. The plan's Item 16 says "validate exact paths during implementation" but buries this in a parenthetical — it deserves a first-class open question since these are the files that must change for write-path inversion.

---

## 3. Contradictions and Missing Dependencies

### Item 11 → Item 10 dependency is inverted

Item 11 (cache applier) declares "Dependencies: Item 10 (bootstrapper provides decoded events)." But Section C of the plan itself says the cache applier is "reused for bootstrap and live replay" — meaning the **bootstrapper consumes the cache applier**, not the reverse. Item 10 should depend on Item 11. The export has the same inversion (step 12 depends on step 11). This matters because building the bootstrapper without a working cache applier means Item 10 can only test with in-memory stores, deferring the harder integration.

**Fix:** Swap the dependency: Item 11 depends on nothing beyond Item 4 (in-memory event repo) and existing repo interfaces. Item 10 depends on Items 4, 6, 9, **and 11**.

### Item 6 (Kind catalog) dependency on Items 4–5 is artificial

The kind catalog is a pure data definition (group names, kind lists, tier assignments). It has no runtime dependency on the cursor planner (Item 4) or reactor warm replay (Item 5). It could land as early as Item 1, unblocking Items 5, 10, and 14 sooner. Both documents sequence it late unnecessarily.

### Item 12 lists Item 8 (continuity wiring) as a dependency but Item 8 is marked "parallel"

This is consistent in intent (tier1 needs continuity) but creates a hidden critical path: Item 8 is "Small" and "parallel" — easy to deprioritize — yet it gates Item 12, which is "Large" and gates Items 13–19. If Item 8 slips, everything after Item 12 slips.

---

## 4. Risk of Over-Planning

### Section E (Self-Identity Events) can be folded into Item 9

Sections B and E both describe the same three event kinds (31410, 31411, 30360). Section E adds publisher dedup semantics, but this is implementation detail for `bahia_status_projector.go`. The plan would be tighter if Section E were a subsection of Section B rather than a standalone design section.

### 19 work items are too granular for a plan — collapse small items

Items 1+2+3 (config mode → health infra → health endpoints) are a single coherent deliverable: "mode-aware health system." Items 7+8 (relay health tracking) is another natural merge. Collapsing to ~12–14 items would reduce dependency tracking overhead without losing fidelity.

### The Background section (lines 1–100) restates what the export already proved

The plan's Background section re-derives findings from the codebase that the export already validated with file:line references. For a plan document, a 2-sentence summary + "see export for evidence" would suffice. The current 100 lines of background context make the plan feel like it's still in discovery rather than decision mode.

---

## 5. Questions Whose Answers Would Change Implementation Order

1. **How many kind families have lossy replaceable payloads today?** If >30% are lossy, canonical payload extension becomes a prerequisite phase before Items 15–17, and the plan needs a new "Item 14.5: audit and extend canonical payloads." A quick `repo model → encode → decode → compare` audit per family would answer this in a day.

2. **Does the bootstrapper need to handle relays that never send EOSE?** Some relay implementations are known to omit EOSE for empty filter results. If Bahia's relay set includes such relays, the bootstrap algorithm needs a timeout-based fallback, which changes the bootstrapper design (Item 10) and health check semantics (Item 2).

3. **Should the kind catalog be compile-time or runtime-configurable?** If operators can add custom event kinds (e.g., for plugins/extensions), the catalog needs a registration API, not just a static struct. This would expand Item 6 significantly and affect Items 10 and 14.

4. **Is there an existing integration test harness with mock relay streams?** Items 5, 10, 12, and 15 all require relay-level integration tests. If no mock relay infrastructure exists, building one is an unlisted prerequisite that should be Item 0.
