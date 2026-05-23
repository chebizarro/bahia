# Verification Report: Bahia Worker Management Bucket G

Date: 2026-05-23

## Scope Verified

- Bead `bahia-513v`: `web/src/routes/workers/+page.svelte` only.
- Workers list shows separate liveness and scheduling badges.
- Workers list exposes scheduling state, workload/task kind, label key/value, accelerator, runtime, and online-only filters.
- Per-row action menu publishes worker lifecycle and label update commands through the existing Nostr `publishCommand` flow.

## Evidence

- Worker command request kinds used by the page match the registered backend worker command kinds: `5997` through `6003` with terminal worker result kind `7997`.
- Published requests include `d`, `worker`, and `command` tags plus JSON content containing `worker_pubkey`, `idempotency_key`, `reason`, `operator_metadata`, and labels for label updates.
- The page waits for correlated worker result events via `publishCommand(..., resultKinds: [7997])` and does not locally mutate worker read-model state.
- In-flight command state is tracked per worker/action and disables additional actions for the same worker while a publish is pending.
- Worker capability/filter helpers defensively handle legacy or malformed non-array `software` and `accelerators` fields.

## Commands Run

```bash
cd web && npm run build
cd web && npm run build
```

## Results

- First `npm run build`: passed; unrelated existing Svelte warnings were reported outside the workers page.
- Oracle review: found four issues; fixes applied.
- Second `npm run build`: passed; same unrelated existing warnings remained outside the workers page.

## Remaining Work

No remaining work was identified inside bead `bahia-513v` scope. Broader worker assignment, drain status, eligibility preview, MCP tools, and detail-page work remain tracked by existing Beads outside this slice.
