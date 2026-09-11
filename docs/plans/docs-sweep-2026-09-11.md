# Bahia Documentation Sweep — 2026-09-11

**Goal:** Update all onboarding-oriented documentation in the `bahia` repo to reflect the current code state.

**Priority:** Onboarding documentation. When you find drift, prioritize what a new engineer/operator needs to succeed: install, run, deploy, extend, debug.

## Ground rules for sub-agents

- **You are working in `bahia/` (the `bahia` repo root within this workspace).** All paths below are relative to that.
- **DO NOT commit or push.** The orchestrator will squash and push after all buckets complete review.
- **DO NOT touch files outside your assigned bucket.** Other agents are working in parallel.
- **Read code before you edit docs.** For every non-trivial factual claim in a doc (CLI flag, config key, kind number, module path, endpoint, service name, env var), verify against the code. Prefer removing/marking stale claims over leaving them wrong.
- **Preserve intent and voice.** Rewrite only where the code has moved on. Trim dead sections rather than fabricating replacements.
- **When in doubt, mark it.** A `> **NOTE (2026-09-11):** verify against …` beats silently hallucinating.
- **Small oracle review at end** (`ask_oracle` mode=review) is optional; only run if your bucket touched >5 files with substantive changes.
- **Report back concisely:** what you changed, per file, plus anything that needs follow-up (create a `bd` issue for anything deferred).

## Excluded from sweep (do not modify)

- `docs/plans/`, `docs/reviews/`, `docs/investigations/`, `docs/analysis/`, `docs/designs/`, `docs/archive/`, `docs/specs/`, `docs/inventory/` — dated historical artifacts.
- `pstf/**` — test/spec output.
- `test/**/README.md`, `test/**/DELIVERY.md`.
- `docs/WEB_APP_PRODUCTION_PLAN.md` — dated plan.
- `.beads/README.md`, `.claude/**`, `.github/**`.

## Buckets

### Bucket 1 — Core & Top-Level Onboarding Docs ✅ COMPLETE (11 files edited; bd: zdbam, 93dtn, ubg10)
- [ ] `README.md`
- [ ] `AGENTS.md`
- [ ] `CLAUDE.md`
- [ ] `DOCKER.md`
- [ ] `docs/architecture.md`
- [ ] `docs/api.md`
- [ ] `docs/deployment.md`
- [ ] `docs/control-planes.md`
- [ ] `docs/event-spec.md`
- [ ] `docs/protocol-compatibility.md`
- [ ] `docs/vm-runtimes.md`

### Bucket 2 — User Guide ✅ COMPLETE (29 files edited; bd: 5i4gi, nejw5, tgjvj, qljc5, 9p7x2)
- [ ] `docs/user-guide/index.md`
- [ ] `docs/user-guide/getting-started.md`
- [ ] `docs/user-guide/core-concepts.md`
- [ ] `docs/user-guide/cli-reference.md`
- [ ] `docs/user-guide/nostr-integration.md`
- [ ] `docs/user-guide/mcp-tools.md`
- [ ] `docs/user-guide/troubleshooting.md`
- [ ] `docs/user-guide/features/*.md` (all)
- [ ] `docs/user-guide/guides/managed-dns-and-https-routes.md`

### Bucket 3 — Runbooks, Rollout, Adoption ✅ COMPLETE (15 files edited; bd: 9sav5, tqndf, viscy, b86m9)
- [ ] `docs/runbooks/*.md` (all)
- [ ] `docs/rollout/*.md` (all)
- [ ] `docs/adoption-live-network-operator-checklist.md`
- [ ] `docs/adoption-live-network-verification.md`
- [ ] `docs/adoption-production-rollout.md`
- [ ] `docs/adoption-signer-first-operator-checklist.md`
- [ ] `docs/push-to-deploy-and-hiveci-runbook.md`
- [ ] `docs/soul-factory-sidecar-runbook.md`

### Bucket 4 — Component / Subsystem Docs ✅ COMPLETE (15 files edited; bd: vfw9c, n5uin, bxkex, czyiq, 8c1jq)
- [ ] `docs/soul-factory.md`
- [ ] `docs/soulfactory-runtime-control.md`
- [ ] `docs/openclaw-soulfactory-control-wrapper.md`
- [ ] `docs/openclaw-soulfactory-sidecar.md`
- [ ] `docs/relay-sidecar.md`
- [ ] `docs/nostr-commands.md`
- [ ] `docs/nostr-event-implementation-guide.md`
- [ ] `docs/operator-assistant-protocol.md`
- [ ] `docs/web-api-client.md`
- [ ] `docs/web-app-setup.md`
- [ ] `docs/web-components.md`
- [ ] `docs/web-testing.md`
- [ ] `web/README.md`
- [ ] `cmd/bahia-dns-agent/README.md`
- [ ] `deploy/dns-agent/README.md`

## Handoff format

When you finish, print a short report:

```
## Bucket <N> — <name>
- Files touched: <count>
- Substantive corrections: <bullet list, one line each>
- Removed / trimmed: <bullet list>
- Deferred (bd issues filed): <bullet list with bd IDs, if any>
- Suggested follow-ups: <bullet list>
```
