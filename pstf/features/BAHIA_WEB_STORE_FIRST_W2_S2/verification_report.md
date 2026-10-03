# W2-S2 verification

Issue: `bahia-irsry.12.5`.

The worker and operation read paths now use the signed, persisted event store. The
binding tests ingest real signed Nostr events; the advertisement subscription
test uses welshman's `MockAdapter` and counts actual REQ frames. Playwright
tests navigate the worker list and detail page and inject live relay events.

The shared collection rebuild/persist machinery and events router remain for
the Phase 4 Wave 2 integration slice. No worker or operation applicator remains
in that router. Mutations are unchanged.

Final gate (2026-10-03):

- `pnpm install --frozen-lockfile`: passed.
- `pnpm run test:unit`: 1,070 passed, one skipped.
- `pnpm run lint`: zero errors and zero warnings.
- `pnpm run build`: passed.
- `CGO_ENABLED=0 go build ./...`: passed.
- `CGO_ENABLED=0 CI=1 npx playwright test`: 199 passed, four skipped. The
  test server used port 4183 because another worktree occupied the default
  port 4173; the test configuration was otherwise unchanged.

The existing `workers-events-smoke`, route-console, and new store-first worker
browser tests all passed in the final run. No push was performed, per the
slice-specific instructions.
