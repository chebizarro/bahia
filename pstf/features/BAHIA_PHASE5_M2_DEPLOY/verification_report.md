# Phase 5 M2 deployment CLI verification

Issue: `bahia-irsry.13.7`. Base: `5905cf47`.

## Daemon capability audit

`internal/app/app.go` registers service, environment, policy, LLM, package,
org, secret, notification, and backup intent handlers, but no deployment or
runtime intent handler. `IntentProcessor.process` returns without a status when
a domain has no handler. The reactor's deployment and runtime requests and the
ContextVM handlers are separate paths; they are not 30900 intent handlers.
Publishing a deployment/runtime 30900 now would strand the request without
canonical reconciliation or a 30315 status.

## Verified behavior

| Criterion | Evidence |
| --- | --- |
| Each CLI deployment/approval invocation receives a fresh retryable UUIDv7 unless supplied | `TestDeploymentContextVMKeyIsFreshUUIDv7OrCallerSupplied`, `TestDeploymentAndApprovalCommandsMintContextVMRetryKeys` |
| Runtime restart forwards the CLI key through ContextVM `_meta.progressToken` | `TestRuntimeRestartCommandForwardsExplicitRetryKey`, `TestOperatorRuntimeActionUsesExplicitProgressToken` |
| `-32011` exposes the retry key without treating an unknown outcome as a failed deployment | `TestOperatorInterruptedDeploymentExplainsRetryKey` |

Deploy, rollback, approval, and services runtime deploy/restart/stop remain on
signed ContextVM, not direct 30900. No migrated `run*Nostr` or ContextVM methods
were deleted. The `--http-fallback` behavior remains unchanged for those
unmigrated commands.

The requested `ProcessInProcess` acceptance/rejection/canonical-state and
intent-outbox E2E cases cannot truthfully run through a deployment handler in
this base: none is registered. They are blocked on implementing daemon
deployment and runtime intent handlers, including canonical state publication
and bounded 30315 statuses. This is not a test skip or a simulated handler.

Verification: `CGO_ENABLED=0 go build ./...`, `CGO_ENABLED=0 go vet ./...`,
`CGO_ENABLED=0 go test ./...`, `git diff --check`, and `gofmt -l` all passed.
An initial full-suite attempt returned nonzero with output suppressed; the
captured test rerun and subsequent complete build/vet/test gate both passed.
