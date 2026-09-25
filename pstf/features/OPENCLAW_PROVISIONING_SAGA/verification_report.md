# OpenClaw Provisioning Saga Verification

Task: `bahia-openclaw-transaction-reconciliation-20260819`

The saga package persists cross-process-locked, compare-and-swap JSON checkpoints with mode 0600 files in a mode 0700 state directory. Stage and compensation idempotency keys derive from the immutable Soul Factory request/run identity. Persisted external identifiers are one-way public references. Current-version writes cannot rewrite run identity or append-only lineage.

Every driver mutation is bracketed by inspection. Ambiguous responses are refetched and correlated. Matching reality must have the requested spec and request correlation. Compensation inspects each individual resource and follows Signet policy/binding → transient credential → runtime account/route → container → projection order; only resources created by the same run are eligible.

The terminal driver must inspect and correlate both kind 7950 provisioning-result lineage and kind 31951 agent-soul lineage with the authoritative running, rolled-back, or failed-terminal checkpoint. Terminal reconciliation also uses inspect-before-publish and refetch-after-publish behavior.

Deterministic tests cover exact replay, response loss, wrong-spec conflicts, multi-resource rollback, adopted-resource preservation, stale and current-version rewrite rejection, retry and process restart, concurrent isolation, every-forward-stage injected failure, dry-run commands, secret-free reports, correlated terminal state, and terminal retention; recoverable ownership lineage remains until reconcile or safe-abort.

Verification commands:

- `go build ./...` — passed
- `go test ./...` — passed
- `golangci-lint run ./internal/soulfactory/saga/...` — passed
- `golangci-lint run ./...` — reports 154 pre-existing findings outside this feature package


## bahia-lf0s4 — adoption decision (2026-09-25)

Decision: **adopt the existing governed saga**, not retire it and not install a
second saga inside the Docker wrapper. Evidence at starting commit `06cc8bf6`:

- `internal/app/soulfactory.go:195-219` already constructs the production engine,
  gets its monitor over the same store, and installs it in the reactor;
  `internal/app/app.go:616` attaches that monitor to `/metrics`. The issue's
  original "Engine invoked by nothing" premise is stale on this branch.
- `openclawcontrol/runtime_orchestrator.go` validates immutable image/spec labels,
  inspects before Compose up/down, reuses healthy owned containers, and verifies
  health afterwards. This is useful single-runtime idempotency, not a persisted
  cross-system provisioning transaction.
- `saga/engine.go` records requested intent before effects, reconciles inspected
  reality before/after each stage, records resources/transitions/failures with
  immutable identity and CAS, resumes retryable failures, and compensates only
  resources owned by that run. `saga/store.go` atomically syncs checkpoints and
  checks append-only history. `governed_provisioning_production.go` supplies the
  real reservation, Bahia service/unit/release/deployment, Signet/runtime,
  relay/model/readiness, and Soul-projection adapters.
- Removing saga would delete the reachable governing path, not redundant Docker
  machinery. Exposing a new lower-level saga would duplicate orchestration.

### Changes and regression evidence

Normal ContextVM provisioning used to lose `runtime.runtime_release_id` in
`event_codec.go`, failing before Start with `invalid UUID length: 0`. Preserving
that field makes the normal request adapter -> Reactor -> production engine
write real run/checkpoint files. `TestNormalProvisioningPopulatesSagaMetricsAndStuckAlert`
uses this path (no seeded saga runs), a controlled unavailable generator, the real
FileStore and telemetry handler, and an observation clock to prove the unchanged
stage-stuck threshold stays true for its five-minute hold.

Operator commands reconstruct the original resolved inputs from the private
adapter ledger after restart. The shared nonblocking file lock serializes live
provisioning and recovery for a request. Identity/spec/runtime mismatch fails
closed. The four ContextVM methods use the transport's operator registration,
which applies `FleetOperatorGate` before progress acknowledgments, replay cache,
and execution. Default dry-run requires explicit false for mutation.

An existing identity reservation is not proof that preparation completed. A
retry must resume unfinished generation rather than skip it. Safe-abort retains
reserve-only identity facts and relinquishes ownership of durable registry
facts; it is not an instruction to delete a live container or recreate keys.
Original ContextVM method provenance survives restart, so failure/rollback also
publish canonical state/audit results. Success delivery retains its signed event
before publication, resumes after loss of acknowledgment, derives stable
canonical event IDs from the source result, and stops publishing after all
result/state/audit outputs are accepted.

The six alert definitions, metric names, metric catalog, dashboard guard, and
alert-reference tests are unchanged. HELP text and the operations guide clarify
that readiness/terminal metrics describe checkpoint evidence, not fresh Docker
or DM tests nor proof of success-result delivery. Historical OpenClaw names also
cover governed Metiq runs in the shared store. Disabled SoulFactory emits no saga
series. This is deterministic local integration evidence, not a deployed
Prometheus firing test or live Docker/Signet/DM acceptance.

Review: one excerpt-based Oracle review (worktree artifact routing failed).
Terminal success-delivery feedback was applied. Failure/abort canonical routing
was already implemented and gained assertions; retained reserve-only identity
records are intentional, not destructively compensated.


Counterfactual: removing the preparation-completion guard makes
`TestProductionOperatorRestartRetryAndSafeAbort` fail at the retry assertion:
`An error is expected but got nil.` The same test passes with the guard restored;
the guarded retry invokes generation again and preserves the original RunID.

Context tooling: Jev `filter-search`, `find-lines`, and `rank-files` surfaced the
already-live production provisioner and narrowed reads. Its results were checked
against source; the race-log query correctly found no detector stack trace (the
failure was a test assertion). Recorded usage: 20 requests, 98,787 input tokens,
approximately $0.004149. No code-writing subagents were used.


### Final verification

- `go build ./...` — passed.
- `go vet ./...` — passed.
- `go test ./...` — passed on final source.
- `make race` — passed on final source. The initial run failed only the untouched
  `internal/adapters/nostr/TestBootstrapperLiveCatchupCompletesAfterFirstRelayEOSE`
  assertion (`bootstrapper_test.go:151`, expected 1, actual 0); no race-detector
  warning. The complete rerun passed without changing that package.
- `golangci-lint run --max-issues-per-linter=0 --max-same-issues=0` — exactly one
  known finding: unused `handleStandbyNodeDefinition` at
  `internal/controlplane/continuity_definition_handlers.go:42`. No added findings.
- Focused SoulFactory/app/telemetry tests passed. Existing metric catalog,
  one-directional dashboard metric guard, and alert-reference tests passed
  without modification. No installed `promtool`; no deployed alert firing claimed.
- `git diff --check` — passed. Main checkout stayed clean. App change is one
  handler registration line; sibling-owned backend/build-request code untouched.

Legacy adapter ledgers lacking captured inputs fail closed at the operator
boundary; new accepted requests persist them before effects. No live deployment,
Docker mutation, push, merge, or issue-state mutation was performed for this task.
