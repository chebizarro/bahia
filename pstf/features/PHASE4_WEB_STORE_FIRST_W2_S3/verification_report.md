# Phase 4 W2-S3 verification — bahia-irsry.12.6

## Acceptance evidence

| Intended behavior | Evidence |
| --- | --- |
| Activity, backup, ML, and SBOM read from the event store; addressable replacements and tombstones update views without collection rebuilds | `web/tests/unit/rest-store-first.test.js`; `web/tests/e2e/rest-store-first.spec.js` |
| SBOM references and availability route by kind and `t` topic | `rest-store-first.test.js`; `sbom-workflow.spec.js` |
| Payment, finding, schedule, and chunked finding-detail records decrypt with the fleet OCK; a missing key is shown as unreadable; a detail tombstone suppresses older chunks | `internal/controlplane/paysec_cross_language_fixture_test.go` asserts the committed fixture; `rest-store-first.test.js` covers decryption and chunk/tombstone behavior; `rest-store-first.spec.js` covers browser decryption, unreadable states, and the positive dashboard spend summary |
| Browsing payments and security does not invoke ContextVM reads | `rest-store-first.spec.js`; dashboard cost summary uses the payment store query |

## Gate

- `pnpm install`, `pnpm run test:unit`, `pnpm run lint`, `pnpm run build` — passed.
- Final CI-mode Playwright gate on port 4173 exited 0: **199 passed, 1 flaky (passed on retry), 4 skipped, 0 failed**. The unrelated service-secrets test passed 3/3 in isolation afterward; the preceding full CI run had 200 passed, 4 skipped. An earlier run used isolated port 4267 while 4173 was occupied by another worktree; the temporary config was removed.
- `CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...` — passed.
- `CGO_ENABLED=0 go test ./internal/archtest -count=1 -v -run TestNoNew` — passed, 0 added violations.

The shared events router and collection rebuild/persist machinery remain by the Wave 2 shared-file rule; the orchestrator removes them at integration. No `.beads/` files were touched, and this slice is not pushed.
