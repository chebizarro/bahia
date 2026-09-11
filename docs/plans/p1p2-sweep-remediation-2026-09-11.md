# P1/P2 Remediation from Docs Sweep — 2026-09-11

**Trigger:** the 2026-09-11 documentation sweep filed 10 P1/P2 issues that describe real code defects. This plan implements them.

## Ground rules for sub-agents

- **You are working in `/Users/bizarro/Documents/Projects/bahia`.** Real checkout, not the fleet-planning overlay.
- **DO NOT commit or push.** The orchestrator batches and squash-commits after all buckets pass review.
- **DO NOT touch files outside your assigned bucket.** Other agents run in parallel.
- **Prefer minimal safe fixes.** If a fix keeps expanding into architectural work, stop and file a follow-up bd issue rather than pushing through.
- **Update the bd issue** (`bd show <id>` to read, `bd update <id> --notes "…"` to add implementation notes, `bd close <id>` when done).
- **Tests.** Add or update tests for each fix. If the change is impossible to unit-test, note why in the bd issue.
- **Report back concisely:** per-issue: what changed, tests added, and any follow-ups filed.

## Buckets

### Bucket 1 — Security: allowlist validation (P1)
- [x] `bahia-9sav5` — empty `allowed_pubkeys` and empty `nostr.authorized_pubkeys` currently mean *allow any signer*. Config validation also accepts subject/email-only allowlists. Make empty explicit (empty = deny-all, or require an explicit `allow_any: true` flag) and reject subject/email-only allowlists at load time.

### Bucket 2 — ContextVM method surface consistency (P2 × 3)
- [x] `bahia-ubg10` — discovery advertises ~20 method names with no registered server handler (see the issue for the list; several are emitted by Bahia's own publishers). Reconcile: trim discovery, add handlers, or fix publisher names — whichever is minimal and correct per method.
- [x] `bahia-vfw9c` — LLM approval method-name mismatch: discovery advertises `llm/deployment-approval` while the publisher sends `llm/approval`. Pick one name; align both sides; update tests.
- [x] `bahia-tgjvj` — DNS `record-override` vs `record-set` and other method-name disagreements. Same treatment: align, don't fabricate.

> **Sibling coordination:** Bucket 5 also touches MCP publishers (for legacy kinds). Bucket 5 is changing `Kind` fields, not method names — but if you both edit the same publisher struct, coordinate through the orchestrator rather than pushing overlapping edits.

### Bucket 3 — CLI + policy enforcement wiring (P2 × 2)
- [x] `bahia-5i4gi` — `bahia config` CLI command group is defined but never registered on the root command. Register it and add a smoke test.
- [x] `bahia-nejw5` — the `require_approval` policy rule is offered in the UI but not enforced by the policy engine. Wire enforcement (evaluation + deployment-time gate) and add tests. If wiring turns out to be architecturally larger than expected, stop and file a follow-up.

### Bucket 4 — Deployment / CI infra (P2 × 2)
- [x] `bahia-zdbam` — `docker compose up --build` likely fails to start the web container because `PUBLIC_BAHIA_BOOTSTRAP_RELAYS` and `PUBLIC_BAHIA_SERVICE_PUBKEYS` are only passed as build args, not runtime env, and the pubkey default is empty. Choose a fix: expose them as runtime env (compose `environment:`), have `web/docker-entrypoint.d/40-bahia-bootstrap-env.sh` skip when build-time substitution already happened, or derive the pubkey from `BAHIA_NOSTR_PRIVATE_KEY`. Verify with a real `docker compose up --build`.
- [x] `bahia-viscy` — `hive-ci-build.yml` doesn't print the `BAHIA_ARTIFACT` line Loom's `ci/workflow-run` handler needs, and the seeding example points at `.gitea/workflows/release.yml`, which doesn't exist. Emit the artifact line and correct or add the release workflow.

### Bucket 5 — Observability + legacy kinds (P2 × 2)
- [x] `bahia-tqndf` — `internal/soulfactory/saga` OpenClaw saga metrics/operator aren't linked into any binary, so the six `BahiaOpenClaw*` alerts can't fire. Wire the saga into whichever binary owns provisioning (likely `cmd/openclaw-soulfactory-control` or `cmd/server`) and add a startup test that asserts the metric is registered.
- [x] `bahia-n5uin` — MCP policy/artifact/tool-approval publishers still sign legacy kinds `5985`–`5989` and `7977`. Migrate publishers to canonical `30900` (or whichever kind the doc sweep listed as canonical) with `legacy_kind` tag preserved for compatibility if needed. Update tests.

> **Sibling coordination:** Bucket 2 is touching the same publisher structs to fix method names. If your edits collide, ping the orchestrator instead of pushing through.

## Handoff format per bucket

```
## Bucket <N>

Per-issue:
  - `bahia-<id>` — <one line>: files changed, tests added, closed? yes/no.

Follow-ups filed (bd IDs): …
Notes for orchestrator: …
```
