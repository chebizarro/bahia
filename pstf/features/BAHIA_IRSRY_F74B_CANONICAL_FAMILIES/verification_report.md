# F74b canonical family verification

The five family discriminators are `32030` package intent/claim/approval,
`32031` tool provisioning intent, `32032` tool denylist, `32033` tool profile,
and `32034` notification log. All publish as fleet-OCK-encrypted, service-signed
kind `30900` via `publishControlState`, with one `#t` topic each.

Mutation-bound package/tool/notification repository decorators publish after
successful persistence. The signed package intent handler publishes a separate
terminal read model because it does not persist a `PackageIntent` row. The
single-use package claim/approval store remains the authorization authority.
Notification history is a replaceable latest-50-per-channel index, bounded to
60 KiB plaintext with payload/error truncation and same-coordinate tombstones.

The DB-less tests cover publication counts, tombstones, size/confidentiality,
and MCP result-shape goldens. The direct MCP read handlers and their repository
query paths were deleted; package/tool/log reads use validated local signed
state.

Verification on this branch:

- `git diff --check`, gofmt, `CGO_ENABLED=0 go build ./...`,
  `CGO_ENABLED=0 go vet ./...`, and `CGO_ENABLED=0 go test ./...` passed with
  `GOFLAGS=-p=2` for the final full run. `go test ./internal/archtest -run
  TestNoNew -count=1` passed. One earlier full run hit a transient timeout in
  `TestAssistantExecutionReconnectRacingUserOperationKeepsOneChain` during
  concurrent worktree gates; its isolated rerun and the final full run passed.
- `pnpm run test:unit` passed (1069 tests, one skipped; one file skipped), as
  did `pnpm run lint` and `pnpm run build`.
- Playwright ran on the supported alternate port 4174 because another
  worktree owned 4173. Result: 222 passed, 4 skipped, 3 failed in untouched
  tests: `deployment-history-and-run-details.spec.js` could not find
  `pre.logs`; two `environments-crud-smoke.spec.js` cases call `fill()` on an
  organization `<select>`. No F74b browser code or tests were changed.
