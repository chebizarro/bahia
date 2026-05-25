# Critique: Resource Pressure Orchestration Plan

**Date:** 2026-05-24  
**Reviewed:** `docs/plans/resource-pressure-orchestration-2026-05-24.md`  
**Against:** `prompt-exports/oracle-plan-2026-05-24-172106-pressure-orchestrati-1856.md`

---

## 1. Top 3 Under-Specified Seams

### 1A — Cleanup keep-list has no data source today

The plan says Item 5's protected keep-list is "derived from assignments + standby state," but both structs lack the fields the plan assumes:

- `WorkerAssignment.Metadata` (`internal/domain/worker_read_models.go:25`) is `map[string]any` — nothing currently populates `artifact_id` or `image_ref` keys into it.
- `WorkerStandbyAssignment` (`internal/domain/worker.go:48-54`) carries `ServiceKey` and `Tier` but has no artifact/image reference at all.

An implementer hitting Item 5 will discover they need to extend the assignment projector *and* the standby struct first — work that is unsized, unscoped, and not listed as a dependency. Open Question 3 correctly flags the risk, but the plan doesn't resolve it or even add a conditional sub-item.

### 1B — Payment/token path for internal cleanup jobs

Both documents flag "validate during implementation" but neither identifies the provider or confirms internal jobs can be submitted without payment. If the existing path requires a paid invoice or token, there is no Loom-level bypass — this could block Item 5 entirely or force a Loom protocol change. The plan should at minimum name the candidate code path so an implementer knows where to look.

### 1C — Coordinator admission insertion point

The plan hedges ("likely `internal/workflow/coordinator.go` or equivalent") when the call site is unambiguous: `coordinator.go:143` selects a worker, `coordinator.go:167` calls `SubmitJob`. The gap between those two lines is exactly where Item 7's admission gate belongs. By leaving this as "validate exact path," the plan makes an M-sized item feel uncertain when it's straightforward — while hiding the harder question: what happens when the admission gate *rejects* at dispatch time? The coordinator currently has no retry/re-select path; it would need to surface an error to the intent lifecycle. That retry/error-propagation design is the real under-specification, not the file path.

---

## 2. Specificity Balance

### Over-specified (plan should defer to implementer)

- **Threshold table values** (plan §Pressure evaluation): Pinning exact GiB/percentage/temperature numbers as spec rather than "suggested defaults" constrains tuning. The plan already says "config-backed" — the table should be labeled as starting defaults, not design.
- **Cleanup env var naming** (export §E, echoed in plan): `BAHIA_CLEANUP_PROTECT_IMAGE_REFS`, `BAHIA_CLEANUP_TARGET_FREE_DISK_BYTES`, etc. are implementation choices for the cleanup script contract; the plan need only say "pass protected refs and target free space via env."

### Useful framing the plan dropped from the export

- **Error handling / edge cases section** (export §H): The export explicitly covers "cleanup recommendation requires docker reclaimable data; if missing, disk pressure becomes `operator_intervention`, not auto cleanup." The plan's capacity-class table implies this but doesn't state it, which is a gap an implementer would have to infer. The export's "thermal/memory/VRAM are NOT cleanupable" rule is similarly implicit in the plan but explicit in the export.
- **"Fixing existing bugs while touching the path" framing** (export §2, bottom): The export calls out the two replaceable-event ordering bugs as pre-existing defects fixed by this work. The plan mentions the fix but buries it — an implementer might not realize these are existing production bugs, not just future-proofing.

---

## 3. Contradictions & Missing Dependencies

- **Item 5 → Item 6 implicit dependency**: Item 5 (cleanup orchestrator) checks `CapacityClass == cleanup_only` to decide whether to dispatch, but it also must respect queue fullness. The plan says cleanup "does NOT bypass `MAX_CONCURRENT_JOBS`" — which is enforced worker-side, not Bahia-side. So Bahia submits, the worker rejects, and the orchestrator must handle the rejection gracefully. This rejection-handling path is unspecified. If the orchestrator doesn't catch the Loom-level rejection distinctly from other failures, it will burn through cooldown windows on full-queue workers.

- **Item 9 is under-sized**: It absorbs all three open questions (assignment metadata, payment path, deploy-dispatch validation) plus mixed-version testing. If *any* of those open questions reveals missing code, Item 9 becomes an L or larger — yet it's sized S with no sub-items and depends on all of Items 1–8 being done first. Consider promoting the three validation tasks into explicit pre-conditions checked before Items 5 and 7.

---

## 4. Risk of Over-Planning

- **Background §"Existing placement logic (fragmented)"**: The 4-bullet breakdown of each placement service's checks is restating what the export already captured. The plan's key insight is one sentence: "Common checks that overlap: worker liveness, scheduling state admission, …". The per-service breakdowns could be cut to a single reference to the export.

- **Item 4 (pressure monitor + state publisher extraction)** is two conceptually independent changes bundled as one M item. The state-publisher extraction is a refactor that could land earlier and benefit other work; bundling it with the pressure monitor creates an unnecessarily wide blast radius per-PR.

---

## 5. Questions Whose Answers Would Change Implementation Order

| # | Question | Impact if answer is unfavorable |
|---|----------|---------------------------------|
| 1 | Does the assignment projector currently populate `artifact_id`/`image_ref` in `WorkerAssignment.Metadata`? | **No** (confirmed: it doesn't). Item 5's keep-list needs a predecessor task to extend the projector. This should be sized and scheduled before or alongside Item 5, not discovered inside it. |
| 2 | Can internal jobs reuse the existing payment-token path without a billing record? | If no → requires a Loom-level or billing-level change that should precede Item 5, not be validated in Item 9. |
| 3 | Is 60s pressure-detection latency acceptable, or should critical thresholds trigger immediate republish from the worker? | If immediate republish is needed → Item 2 grows and needs threshold awareness in `loom-worker` itself, creating a soft dependency on Item 3's threshold definitions. Currently Items 2 and 3 are independent. |
