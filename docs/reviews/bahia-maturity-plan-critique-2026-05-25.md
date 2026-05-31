# Critique: Bahia Platform Maturity Plan

**Date:** 2026-05-25  
**Scope:** `docs/plans/bahia-platform-maturity-2026-05-25.md` vs. oracle export

---

## 1. Top 3 Under-Specified Seams

1. **`DeploymentUnit` implicit-to-explicit transition trigger (Item 3).** The plan says "persist lazily on first write/apply/observe" but never defines _what_ causes an environment to graduate from implicit default unit to explicit multi-unit. An implementer must guess whether this is operator-initiated (CLI/API call), automatic (on config change), or migration-driven. The export (R2, "Data flow" section) is equally vague.

2. **`CommandReceipt` publish-and-wait timeout & failure semantics (Item 13).** The plan says REST returns `202` + receipt with "optional publish-and-wait compatibility" but specifies no timeout, no partial-failure shape, and no behavior when the relay is unreachable. The export (R10) adds "temporary publish-and-wait server path" but also leaves the contract hollow. An implementer writing `internal/api/handlers/` has no spec to code against.

3. **Reconciler ↔ deploy lock interaction under mixed modes (Items 6, 12).** Item 6 introduces the scheduler; Item 12 turns on auto-apply. The plan says "same lock implementation as deploy/apply" but doesn't specify what happens when a user deploy arrives while auto-remediation holds the unit lock — queue? reject? preempt? The export (R5, "Concurrency") lists the serialization requirement but also punts on priority.

## 2. Specificity Balance

**Over-specified (let the implementer own these):**
- Item 9 prescribes the exact `RuntimeControlClient` method set (`ApplyDesiredPlan`, `InspectManagedResources`, `RestartManagedResource`, `StopManagedResource`, `StreamLogs`). This is an interface design decision that should emerge from actual adapter extraction, not be dictated up front. The plan should state the _constraint_ (narrow seam, no business logic in adapters, execution-mode metadata returned) and stop.
- Item 3 specifies the full `DeploymentUnit` struct shape (ID, Key, DisplayName, RuntimeType, etc.) — copied verbatim from the export. Column-level schema belongs in implementation PRs, not a roadmap.

**Dropped from export:**
- The export's R6 ("Deeper environment targeting") included a concrete backward-compatibility rule: "moved fields are read from typed fields first, then `runtime_config`; writes should project back into persisted typed fields." The plan's Item 6 has no environment-targeting item at all — R6 content was silently absorbed into Items 3/4 without preserving this migration constraint.
- The export's R10 explicitly called out searching `web/src/lib/api` and `web/src/routes` before changing REST defaults. The plan's "Open Questions" section mentions "Web UI routes" but weakened this to a vague check instead of a concrete pre-work gate.

## 3. Contradictions and Missing Dependencies

- **Item 8 depends on Item 6 (reconcile policy), but Item 6 depends on Item 5 (drift hashes), which depends on Items 2 AND 4.** The plan's dependency table shows R8 depending on R2, R6, R7 — but the work-item level dependencies list Items 3, 6, 7. Item 6 itself depends on Item 5 which depends on Items 2, 4. This means Item 8 has a transitive depth of 5, making it a late-stage item, yet the plan doesn't flag this as the critical-path bottleneck it is.
- **Item 14 depends on Item 3 (units) but not Item 9 (control client).** Projection enrichment for "execution mode" metadata requires the control client seam to exist first — this dependency is missing.
- **Workstream table vs. work items:** R6 ("Deepen environment targeting") has no dedicated work item. Its scope is scattered across Items 3, 4, and 6 without an explicit owner.

## 4. Risk of Over-Planning

- **Item 14 (docs, schemas, PSTF) is an XL-equivalent item that bundles everything.** It should not exist as a single work item — doc/schema updates should be acceptance criteria on the items that change behavior, not a trailing mega-item that will either be skipped or block release. Cut it; add "update normative docs" to each item's done-when.
- **Item 1 (validate baseline landing state) is a discovery task, not a work item.** It produces a "gap list" — this is pre-work that should happen before the plan is finalized, not as the first item in a 15-item roadmap. Either do it now or fold it into Item 2's prerequisites.
- **Item 15 (production rollout by opt-in boundary)** is a sequencing constraint, not implementable work. It restates the ordering already implied by the dependency graph. Cut it; add rollout gates to the items that introduce new behavior.

## 5. Questions Whose Answers Would Change Implementation Order

1. **Is the `5961` reactor → `RuntimeLifecycleService` call chain already fully wired?** If not, Item 2 (persistence) can't be tested end-to-end, and Items 1+2 should merge into a single "land and validate baseline" item.
2. **Are there any non-Bahia-owned Compose dirs in production environments today?** If yes, Item 11 (unit-owned Compose) becomes blocked on an operator migration that isn't in this plan, and Item 8 (adoption) should move earlier to inventory them.
3. **Does any external client (web UI, third-party integration) depend on synchronous REST mutation responses?** If yes, Item 13 (interface unification) needs a compatibility shim _before_ any route changes, which means it can't be a single atomic item — it splits into "add receipts additively" and "switch defaults," with a soak period between them.
