# Phase 5 R1 CLI service/environment reads — verification

Issue: `bahia-irsry.13.4`. Branch: `feat/irsry-p5-r1-svcenv`.

## Acceptance evidence

| Criterion | Evidence |
| --- | --- |
| Service and environment list/get read Nostr by default; REST is explicit | `TestCLIReadRESTNostrGolden` asserts eight Nostr subscriptions and only eight HTTP requests for eight fallback invocations; `services list --nostr` is absent. |
| Table and JSON output match the REST path | `TestCLIReadRESTNostrGolden` compares byte-for-byte output for list/get in both domains and both formats, including service runtime configuration and environment deployment units. |
| Cursor persists across invocations and stale reads succeed with a warning | `TestCLIReadCursorReuseAndStaleExit` reopens the local store, checks the second subscription's `since`, and verifies cached output plus the exact stderr warning with a successful command result. |
| Decoder preserves environment details | `TestDecodeEnvironmentDetailsRoundTrip` covers targeting, selectors, runtime configuration, explicit units, timestamps, and the implicit default; `TestDecodeServiceRoundTrip` covers service runtime configuration. |
| Canonical event carries REST detail fields | Environment-unit create timestamps are stamped before relay-first publication and retained by PostgreSQL; `TestRelayFirstEnvironmentCreateAndUpdatesAreSignedOnceWithExplicitUnits` verifies signed-once parity. |

## Gate (2026-10-03)

- `CGO_ENABLED=0 go build ./...` — pass
- `CGO_ENABLED=0 go vet ./...` — pass
- `CGO_ENABLED=0 go test ./...` — pass
- `CGO_ENABLED=0 go test ./internal/archtest -count=1 -v -run TestNoNew` — pass, zero added violations
- `gofmt` on all touched Go files and `git diff --check` — pass

Beads status was not changed: `bd show bahia-irsry.13.4` could not connect to the worktree's Dolt database (`beads_bahia` absent on the local server), and the slice forbids writing `.beads/`.
