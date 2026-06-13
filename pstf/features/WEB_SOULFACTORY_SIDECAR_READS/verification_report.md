# WEB_SOULFACTORY_SIDECAR_READS Verification Report

## Beads

- Issue: `bahia-v0rp` — Allow Soul Factory event reads through sidecar

## Observed defect

The `/souls/new` route queried SoulFactory templates, souls, drafts, and runtime capabilities through the Bahia sidecar. The sidecar closed those subscriptions with messages such as `blocked: event kind 31952 is not readable from the Bahia sidecar`, producing incomplete EOSE warnings for `31950`, `31951`, `31952`, and `30317`.

## Intended behavior

SoulFactory kinds are direct Nostr interop events, not Bahia legacy control-plane migration inventory. The sidecar accepts valid signed events and browser-safe reads for the SoulFactory event family while preserving fail-closed behavior for unrelated legacy Bahia kinds.

## Review disposition

A review pass found two gaps before closeout:

- frontend `isOpenInteropKind` / `isReadableKind` helper semantics did not include SoulFactory kinds;
- protocol docs still described broad legacy-prohibited numeric ranges without explicitly excluding SoulFactory interop overlaps.

Both findings were fixed before final verification.

## Verification

Final commands:

```sh
gofmt -w internal/kinds/kinds.go internal/kinds/policy.go internal/kinds/policy_test.go internal/relaysidecar/server_test.go internal/nostrmigration/manifest.go &&   go test ./internal/kinds ./internal/nostrmigration ./internal/relaysidecar -count=1 &&   cd web && pnpm exec vitest run --config vitest.config.js tests/unit/nostr-client-parsing.test.js &&   pnpm lint &&   cd .. && git diff --check
```

Result:

- `ok github.com/openagentsinc/bahia/internal/kinds`
- `ok github.com/openagentsinc/bahia/internal/nostrmigration`
- `ok github.com/openagentsinc/bahia/internal/relaysidecar`
- `tests/unit/nostr-client-parsing.test.js`: 49 passed
- `pnpm lint`: `svelte-check found 0 errors and 0 warnings`
- `git diff --check`: passed

## Conclusion

The Bahia sidecar policy now treats SoulFactory kinds `31950`, `31951`, `31952`, `5950`, `6950`, `7950`, `1950`, `1951`, `30317`, `38384`, and `38386` as explicit open interop data. The `/souls/new` read filters for templates, souls, drafts, and runtime capabilities should no longer receive sidecar CLOSED rejections for unreadable kinds after deployment of this change.
