# Bahia Beads Assessment: Plan

## Goal
Assess all currently open Bahia Beads issues, classify which are genuinely still open, which can be closed or normalized, which remain incomplete or deferred, and identify the highest-impact blockers that can be addressed immediately.

This document began as a plan for a follow-up `rp-build` or `rp-orchestrate` workflow. The original planning run did not close Beads, edit production code, or mutate issue state; the later `Orchestration Progress` section records the subsequent immediate-blocker execution pass.

## Background
A 2026-06-07 reconnaissance pass found 1104 total Beads issues: 43 open, 4 in progress, 5 blocked, and 39 ready to work. The same pass found `bd lint` warnings on 21 issues / 23 templates, mostly missing acceptance criteria and bug repro steps.

The high-signal issue clusters are:

- **Immediate blockers:** `bahia-87y2` -> `bahia-ho1r`; `bahia-sqfx.5` -> `bahia-q6ob`; `bahia-1bai` appears to block `bahia-f1ki` completion even though that relationship may not be encoded as a Beads dependency.
- **Concrete actionable defects:** `bahia-5lzn` (SQL placeholder bug in `pg_secret.go`), `bahia-prcf` (encrypted ContextVM completion must not depend on timeout semantics), `bahia-oikr` (navigation E2E mocked bootstrap console errors).
- **Signer-first / Nostr transition work:** `bahia-hygp`, `bahia-pbjq`, `bahia-65zb`, plus relay-settings work in `bahia-87y2`.
- **Production-readiness cleanup:** `bahia-j28b`, `bahia-4ez0`, `bahia-uue5`.
- **Evidence-bound AI/ML gaps:** `bahia-jicv` and `bahia-vn8o` remain genuinely open unless new production/executable evidence exists, because PSTF currently classifies related evidence as fake/mock-only or planning-only.
- **Desired-state rollout chain:** `bahia-zu2p.8.8`, `.8.9`, `.8.10` are open P0 verification tasks; `bahia-zu2p.9`, `.9.1`, `.9.2` are deferred behind that chain.

Load-bearing references:

- Prior hygiene/migration planning: `docs/plans/nostr-audit-bead-orchestration-2026-06-02.md:13`, `:55`, `:63`; `docs/plans/relay-strategy-2026-06-06.md:3`, `:41`, `:65`; `docs/plans/desired-state-runtime-architecture-2026-05-26.md`; `docs/plans/bahia-platform-maturity-2026-05-25.md`.
- PSTF evidence: `pstf/features/BAHIA_NOSTR_AUDIT_PARITY/verification_report.md:43`, `:78`, `:84`; `pstf/features/NOSTR_NATIVE_CONTEXTVM_MIGRATION/verification_report.md:3`, `:24`, `:42`; `pstf/features/AI_FABRIC_HF_VLLM_DEPLOYMENT/verification_report.md:5`, `:56`; `pstf/features/AI_FABRIC_SIGNER_FIRST_PROTOCOL/verification_report.md:5`, `:39`.
- Nostr authority: `docs/nostr-event-implementation-guide.md:1-6`, `:26-43`, `:78-178`, `:199-230`, `:379-392`; `docs/control-planes.md:55-107`, `:180-221`; `docs/nostr-commands.md:67-89`.
- Verification seams: `internal/kinds/kinds.go:139-174`; `internal/adapters/nostr/catalog.go:373-474`; `internal/adapters/nostr/catalog_test.go:132-219`; `internal/adapters/nostr/bootstrapper.go:82-86`, `:188-205`, `:259-283`; `Makefile:33-35`, `Makefile:50-52`; `web/package.json:7-16`; `.github/workflows/web-vitest-unit.yml:3-29`; `.github/workflows/web-playwright-e2e.yml:3-30`.

## Approach
Run the follow-up assessment in two lanes after a fresh Beads snapshot:

1. **Fast blocker lane:** validate and resolve the high-confidence blockers that can unblock other work without waiting for a full inventory pass.
2. **Full classification lane:** classify every open and in-progress issue, normalize stale metadata/dependencies, and update PSTF/Beads evidence.

The lanes can run in parallel after the preflight snapshot. Issue-state mutations still require evidence: close only when verification is sufficient, normalize only when no product implementation is implied, and leave work open when PSTF or code evidence shows unresolved behavior.

Nostr-related classifications must follow the canonical docs rather than local intuition. ContextVM acknowledgments are not terminal truth; durable progress comes from canonical observables and event-driven subscriptions. Any recommendation that would rely on polling, timeout-based completion, ignored OK/CLOSED/AUTH, or fake request/response semantics should be classified as real work, not closeout.

## Preflight Checklist for the Follow-up Workflow
- Run `bd prime`.
- Claim an existing Beads issue for this assessment, or create one with acceptance criteria before mutating Beads.
- Refresh the inventory with `bd stats`, `bd list --status=open`, `bd list --status=in_progress`, `bd blocked`, and `bd lint`.
- Compare the fresh output against the 2026-06-07 reconnaissance snapshot in this plan.
- Audit all assumed relationships, not only formal Beads dependencies. If an issue appears to block another issue but is not encoded in Beads, verify and either add the dependency or record why it is not real.

## Assessment Classification Rules
Assign each open or in-progress Bead exactly one primary classification:

| Classification | Meaning | Required evidence |
|---|---|---|
| `genuinely_open_actionable` | Intended behavior is incomplete and the next implementation step is known. | Bead details, affected files, PSTF/doc/code evidence, and acceptance criteria or missing criteria. |
| `closable_or_normalization_only` | Work may already be complete, or only status/metadata/dependency/template cleanup remains. | Historical verification or PSTF evidence for closeout; Beads field/dependency evidence for normalization; no-code-change rationale. |
| `incomplete_or_deferred` | Issue is valid but blocked, intentionally deferred, externally dependent, or waiting on production evidence. | Blocking dependency, PSTF defect mapping, explicit external blocker, or deferred milestone. |
| `immediate_blocker` | Issue blocks another issue, a quality gate, or closeout confidence and can be addressed immediately. | Dependency evidence, blocked issue IDs, concrete verification seam, and recommended owner/workflow. |

Rules for all classifications:

- Do not close based on prose alone.
- Do not upgrade fake/mock PSTF evidence into production evidence.
- Distinguish observed behavior from intended behavior in closeout notes.
- If an issue lacks acceptance criteria or repro steps, fix the Beads metadata before implementation or mark the gap as part of the issue.
- For Nostr-related issues, cite the Nostr implementation guide and control-plane docs as the authority instead of duplicating protocol details in the Beads notes.

## Initial Classification From Existing Reconnaissance
This is the starting table for the follow-up workflow. Refresh Beads first; then update classifications from fresh evidence.

| Bead | Initial classification | Rationale | Next action |
|---|---|---|---|
| `bahia-87y2` | `immediate_blocker` | Canonical relay-settings state hydration blocks `bahia-ho1r`. | Validate current issue details, then run the relay-settings slice. |
| `bahia-ho1r` | `incomplete_or_deferred` | Blocked by `bahia-87y2`. | Do not advance until `bahia-87y2` is resolved. |
| `bahia-prcf` | `genuinely_open_actionable` | Encrypted ContextVM completion still risks timeout-based semantics. | Verify against ContextVM migration docs and event-driven tests. |
| `bahia-5lzn` | `genuinely_open_actionable` | Concrete SQL placeholder bug in the `pg_secret.go` nil-`envID` branch. | Targeted backend fix and regression test in follow-up workflow. |
| `bahia-hygp`, `bahia-pbjq`, `bahia-65zb` | `genuinely_open_actionable` | Signer-first CLI/MCP/REST transition work remains open. | Coordinate through Nostr command semantics and receipt evidence. |
| `bahia-oikr` | `genuinely_open_actionable` | Navigation E2E mocked bootstrap console errors remain actionable test debt. | Web E2E-focused follow-up. |
| `bahia-4ez0`, `bahia-uue5` | `genuinely_open_actionable` | Web quality gate script and build-warning cleanup remain open. | Web build/lint/test follow-up. |
| `bahia-j28b` | `genuinely_open_actionable` | Dormant SoulFactory placeholder path is a production-readiness smell. | Verify removal path and affected tests/docs. |
| `bahia-vn8o` | `incomplete_or_deferred` | PSTF says signer-first AI/ML protocol evidence is planning/contract-only. | Keep open until executable protocol verification exists. |
| `bahia-jicv` | `incomplete_or_deferred` | PSTF says HF/vLLM evidence is fake/mock-only, not production readiness. | Keep open until production integration evidence exists. |
| `bahia-zu2p.8.8`, `.8.9`, `.8.10` | `genuinely_open_actionable` | Desired-state P0 rollout verification tasks remain open. | Verify against desired-state runtime docs, tests, and rollout evidence. |
| `bahia-sqfx.5` | `closable_or_normalization_only` or `genuinely_open_actionable` | Stale in-progress; all listed dependencies appear closed; blocks `bahia-q6ob`. | Apply the evidence threshold below. |
| `bahia-sqfx` | `incomplete_or_deferred` | Likely remains open until `bahia-sqfx.5` resolves. | Reassess after `.5`. |
| `bahia-q6ob` | `incomplete_or_deferred` | Blocked by `bahia-sqfx.5`. | Do not advance until blocker resolves. |
| `bahia-f1ki` | `closable_or_normalization_only` or `incomplete_or_deferred` | Tests reportedly passed, but push was blocked by loom-worker permission; overlaps `bahia-1bai`. | Validate whether the external blocker is still active. |
| `bahia-1bai` | `immediate_blocker` if still active | External maintainer permission appears to block `bahia-f1ki`, but dependency may not be encoded. | Validate blocker type and encode dependency/status if real. |
| `bahia-zgov` | `closable_or_normalization_only` | P4 issue has started metadata that appears stale. | Normalize metadata/status if no active work exists. |
| `bahia-zu2p.9`, `.9.1`, `.9.2` | `incomplete_or_deferred` | Explicitly deferred behind desired-state verification chain. | Leave open/deferred until `bahia-zu2p.8` chain completes. |

## Immediate Blocker Queue

### 1. `bahia-87y2` -> `bahia-ho1r`
**Decision seam:** Determine whether `bahia-87y2` means read-model hydration, relay discovery bootstrap, UI/API consumption of hydrated state, or a combination. The expected direction from reconnaissance is: consume canonical `30900` relay-settings state with scoped subscriptions and expose it to the relay-settings/NIP-86 UI path without inventing a new relay preference source.

**Done when:** the issue text, relay-strategy plan, and Nostr docs agree on the target behavior; the implementation workflow has named the affected files/tests; and `bahia-ho1r` is either unblocked or remains blocked with a precise reason.

### 2. `bahia-sqfx.5` -> `bahia-q6ob`
**Decision seam:** Historical evidence is sufficient only if it includes all of the following:

- the specific acceptance criteria for `bahia-sqfx.5`,
- the verification commands or live/staged evidence tied to those criteria,
- PSTF or Beads notes showing the result was reviewed under signer-first operator verification rules,
- no intervening code/docs changes that invalidate the evidence.

If any element is missing, keep `bahia-sqfx.5` open and reclassify it as actionable re-verification rather than closing from stale memory.

**Done when:** `bahia-sqfx.5` is closed/normalized with evidence, or remains open with the missing evidence recorded; `bahia-q6ob` blocker state is updated accordingly.

### 3. `bahia-5lzn`
**Decision seam:** Confirm the SQL placeholder defect still exists in the current branch before implementation. If present, this is a narrow backend fix and should not wait for broad triage.

**Done when:** follow-up workflow has a claimed issue, a regression test target, and a verification command.

### 4. `bahia-prcf`
**Decision seam:** Confirm whether current encrypted ContextVM result handling still uses timeout-based terminal semantics. If yes, this is protocol work, not test flake cleanup.

**Done when:** follow-up workflow names the event-driven observable path and deterministic tests that inject relay messages rather than sleeping.

### 5. `bahia-1bai` / `bahia-f1ki`
**Decision seam:** Identify the external blocker type: ngit remote maintainer permission, GitHub collaborator access, CI secret/deploy credential, or human approval. If it is no longer active, `bahia-f1ki` may become a quick closeout candidate; if active, encode the dependency and do not let it appear as ordinary code work.

**Done when:** the blocker is either resolved, formally represented in Beads, or dismissed with evidence.

### 6. Beads lint/template warnings
**Decision seam:** Lint warnings block confident closeout only when they hide acceptance criteria, repro steps, or dependencies needed for classification. Otherwise they are metadata cleanup.

**Done when:** each warning is either fixed during classification or converted into a concrete Beads hygiene task.

## Work Items

### Item 1 — Fast-lane the unblockable issues
**Goal:** Resolve or precisely reclassify the blockers that can unblock other active work before the full inventory pass finishes.

**Done when:**
- `bahia-87y2`, `bahia-sqfx.5`, `bahia-5lzn`, `bahia-prcf`, `bahia-1bai`, and `bahia-f1ki` have refreshed evidence and a next workflow assignment.
- Effective but informal dependencies are encoded in Beads or dismissed with evidence.
- `bahia-ho1r` and `bahia-q6ob` blocker states reflect current truth.

**Key files:** `docs/plans/relay-strategy-2026-06-06.md`; `docs/nostr-event-implementation-guide.md`; `docs/control-planes.md`; `internal/kinds/kinds.go:139-174`; `internal/adapters/nostr/catalog.go:373-474`; `internal/adapters/nostr/catalog_test.go:132-219`; `internal/adapters/nostr/bootstrapper.go:82-86`, `:259-283`.

**Dependencies:** Preflight checklist.

**Size:** Medium.

### Item 2 — Classify the complete open/in-progress inventory
**Goal:** Apply the classification rules to every issue in the fresh open/in-progress inventory.

**Done when:**
- Every issue has exactly one primary classification.
- Classification evidence is recorded in Beads notes/design/acceptance fields, not only in a markdown report.
- Lint warnings that affect classification are corrected before closeout decisions.
- Nostr-related issues cite the canonical docs and preserve event-driven semantics.

**Key files:** Beads database via `bd`; `AGENTS.md`; `docs/nostr-event-implementation-guide.md:1-6`, `:26-43`, `:379-392`; `docs/control-planes.md:55-107`, `:180-221`; `docs/nostr-commands.md:67-89`.

**Dependencies:** Preflight checklist. Can run in parallel with Item 1.

**Size:** Medium to large.

### Item 3 — Validate closeout and deferred-work evidence
**Goal:** Make close/keep-open decisions for stale, fake-evidence, deferred, or production-readiness issues.

**Done when:**
- `bahia-sqfx.5`, `bahia-f1ki`, `bahia-zgov`, and any other stale candidates have explicit close/keep-open recommendations.
- AI/ML issues `bahia-jicv` and `bahia-vn8o` remain open unless new executable production evidence exists.
- Desired-state deferred issues remain blocked/deferred behind the correct P0 verification chain.
- Historical evidence is accepted only when it maps to acceptance criteria and current production-readiness rules.

**Key files:** `pstf/features/NOSTR_NATIVE_CONTEXTVM_MIGRATION/verification_report.md:3`, `:24`, `:42`; `pstf/features/BAHIA_NOSTR_AUDIT_PARITY/verification_report.md:43`, `:78`, `:84`; `pstf/features/AI_FABRIC_HF_VLLM_DEPLOYMENT/verification_report.md:5`, `:56`; `pstf/features/AI_FABRIC_SIGNER_FIRST_PROTOCOL/verification_report.md:5`, `:39`; `docs/plans/desired-state-runtime-architecture-2026-05-26.md`.

**Dependencies:** Preflight checklist; coordinates with Items 1 and 2.

**Size:** Medium.

### Item 4 — Normalize Beads state and publish assessment closeout
**Goal:** Leave Beads as the source of truth for what is closed, open, blocked, deferred, or ready next.

**Done when:**
- Closed issues include evidence-backed reasons.
- Metadata-only issues have exact status/dependency/template updates.
- Remaining implementation work has Beads with acceptance criteria and dependencies.
- PSTF verification artifacts are updated when a classification depends on PSTF evidence.
- Relevant quality gates have been run, or a Beads blocker records why they could not run.
- The follow-up workflow commits, pushes, and leaves `git status` clean/up to date.

**Key files:** Relevant PSTF feature directories under `pstf/features/`; `Makefile:33-35`, `Makefile:50-52`; `web/package.json:7-16`; `.github/workflows/web-vitest-unit.yml:3-29`; `.github/workflows/web-playwright-e2e.yml:3-30`.

**Dependencies:** Items 1–3.

**Size:** Small to medium.

## Assessment Acceptance Criteria
- Every fresh open or in-progress Bead has one primary classification.
- Every `closable_or_normalization_only` issue has explicit evidence for closeout or exact metadata/dependency changes.
- Every `incomplete_or_deferred` issue has a blocking dependency, PSTF defect, external blocker, or deferred milestone.
- Every `immediate_blocker` names blocked issue IDs, affected files/docs, verification gates, and the recommended next owner/workflow.
- Informal dependencies discovered during reconnaissance are either encoded in Beads or dismissed with evidence.
- AI/ML fake/mock-only PSTF evidence is not upgraded to production evidence.
- Nostr-related recommendations preserve ContextVM/canonical event semantics.
- Remaining implementation work is represented in Beads, not markdown-only prose.

## Open Questions
- Which Beads issue should own this assessment/triage work before changes are made in a follow-up build workflow? If none exists, create a new Beads task before modifying issue state.
- Is `bahia-1bai` still an active external blocker, and what type of permission/approval does it represent?

## Orchestration Progress — 2026-06-07
- `bahia-5lzn`: fixed service-wide secret `DeleteByName` SQL placeholder, added targeted repository regression test, added PSTF evidence, and marked the Bead ready for closeout.
- `bahia-prcf`: removed encrypted ContextVM timeout-based terminal completion, added deterministic event/cancellation lifecycle tests, updated ContextVM migration PSTF evidence, and closed the Bead.
- `bahia-sqfx.5` / `bahia-q6ob`: refreshed evidence; closeout threshold is not met, so `bahia-sqfx.5` remains the real blocker for `bahia-q6ob` pending staged/live signer-first SF-01–SF-11 signoff.
- `bahia-1bai` / `bahia-f1ki`: identified blocker as ngit/Nostr remote maintainer permission, formally added `bahia-f1ki` -> `bahia-1bai` dependency, and confirmed `bahia-f1ki` cannot close until loom-worker pushes succeed.
- `bahia-87y2`: implemented canonical `30900` relay-settings hydration across backend and settings UI, added tests/docs/PSTF evidence, closed the Bead, unblocked `bahia-ho1r`, and created follow-up `bahia-2kjh` for atomic relay-pool reconfiguration from hydrated policy.

## References
- `bd stats`, `bd list --status=open`, `bd list --status=in_progress`, `bd blocked`, `bd lint` outputs gathered on 2026-06-07.
- `docs/plans/nostr-audit-bead-orchestration-2026-06-02.md`
- `docs/plans/relay-strategy-2026-06-06.md`
- `docs/plans/desired-state-runtime-architecture-2026-05-26.md`
- `docs/plans/bahia-platform-maturity-2026-05-25.md`
- `pstf/features/BAHIA_NOSTR_AUDIT_PARITY/verification_report.md`
- `pstf/features/NOSTR_NATIVE_CONTEXTVM_MIGRATION/verification_report.md`
- `pstf/features/AI_FABRIC_HF_VLLM_DEPLOYMENT/verification_report.md`
- `pstf/features/AI_FABRIC_SIGNER_FIRST_PROTOCOL/verification_report.md`

## Relay Blocker Closeout — 2026-06-07
- `bahia-2kjh`: closeout evidence added under `pstf/features/bahia-2kjh/`; targeted backend gate passed for relay-pool reconfiguration, hydrator snapshot callbacks, topology coordinator convergence, and relay-settings/NIP-86 validation seams.
- `bahia-ho1r`: follow-up web evidence resolved the dirty-edit/canonical-state blocker by using current valid Nostr timestamps in `web/tests/e2e/settings-relay-visibility.spec.js`; unit relay-settings helpers pass (8 tests), dirty-edit E2E passes (1 test), and the full settings relay visibility E2E passes (4 tests). PSTF defect `DEF-2026-06-07-DIRTY-CANONICAL-E2E` is resolved and `bahia-ho1r` is eligible for Beads closeout.
