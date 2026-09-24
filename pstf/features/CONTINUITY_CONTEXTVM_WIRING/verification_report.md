# Continuity ContextVM wiring

## Scope and decision

Worktree: `fix/wire-cont`, based on `78ce2f74`. The supplied issue ID
`bahia-60hu4` is tool approval in this snapshot; the matching continuity issue is
`bahia-ir8v0`. Issue state is coordinator-owned and was not modified.

The architecture authority (`AGENTS.md`, `docs/nostr-event-implementation-guide.md`)
requires ContextVM for mutation intent. The startup migration manifest already
maps failover/recovery to `continuity/failover` and `continuity/recovery`.
The command kind constants/catalog decoders are not permission to restore the
retired production command subscription path. They remain unchanged for
historical decoding/migration.

The two command implementations and their source/correlation helper are migrated
to authenticated ContextVM requests. Execution calls the existing recipe executor
and propagates errors, instead of republishing commands or using the asynchronous
in-process bus as evidence of completion. The domain registration returns a
Tier-1 runner; `internal/app/app.go` changes by exactly one line.

Three formerly unused definition handlers now synchronously update the existing
continuity store behind the same fail-closed gate. The subscriber also hydrates
profiles needed by that store. Filters contain canonical kinds and operator
authors, start at the beginning of valid history, and remain live after EOSE.
An unbounded historical count is deliberate: a limit/cursor would silently
truncate this disposable in-memory inventory. Signatures and inbound timestamp
constraints are checked before trust. Replay converges through the store's
latest-value guard; the equal-timestamp tie-break now follows NIP-01.

The relay adapter exposes strict all-initial-stream EOSE evidence separately
from terminal exhaustion, and forwards buffered history before EOSE. Existing
callers retain their terminal-channel behavior. Only continuity opts into the
stricter completion check.

## Deliberate exclusions and remaining acceptance

`handleStandbyNodeDefinition` stays unused; `31402` is excluded from the backend
subscription and rejected by the dispatcher. No downstream inventory consumer
exists. Building one requires deciding how inventory authority interacts with
replication-policy placement and explicit targets, which is outside this slice.
This remaining part of `bahia-ir8v0` must not be closed as complete.

No package/artifact handler or tool-approval changes are included. No issue-state
changes, push, or merge are authorized. Runtime adapters are existing dependencies;
this change does not provision or implement them. Tests prove routing, real recipe
execution using an injected action adapter, failure propagation and replay, not
live VM failover or operational recovery acceptance. Retained transport responses
provide replay safety within their configured retention, not crash-atomic execution.

## Verification

Acceptance-to-test mapping is in `acceptance_criteria.json`.

| Gate | Outcome |
|---|---|
| `go build ./...` | PASS (exit 0), including final constructor guard |
| `go vet ./...` | PASS (exit 0) |
| `go test ./...` | PASS (exit 0) |
| `go test ./internal/controlplane ./internal/adapters/nostr ./internal/service -run 'TestContinuity\|TestInMemoryContinuityDefinitionStore' -count=1` | PASS in all three packages |
| `golangci-lint run --max-issues-per-linter=0 --max-same-issues=0` | Exit 1: exactly 9 unused findings, down by 6 from the supplied 15-finding baseline; standby plus 7 package and 1 tool-approval findings |
| `make race` | Initial run failed to link the Nostr adapter test binary: Apple clang 17 terminated with `Segmentation fault: 11`; all other packages passed |
| `GOFLAGS=-p=1 make race` | Full-suite PASS (exit 0), serializing package build/test jobs after the linker crash |
| `git diff --check`, gofmt, acceptance JSON validation | PASS |

A Go build overlay removed only the two continuity method registrations. Running
`TestContinuityContextVMReachableAuthorizedAndReplaySafe` against that overlay
failed (exit 1) for both methods and all three envelopes. The counterfactual error
was `Expected nil, but got: &contextvm.Error{Code:-32601, Message:"method not found"}`.
The real source was never removed or changed by this check; the focused suite
then passed with the registrations present.

The final patch leaves `internal/app/app.go` at exactly +1/-0. Beads, reactor.go,
and package handler files are unchanged; the main checkout was checked clean.
No new fake/stubbed production adapter was introduced: commands use the existing
executor and expose its failures, while standby remains explicitly out of scope.

The required Oracle review was attempted but the tool failed with
`targetBindingMismatch`, including after clearing its stale sibling-worktree
selection. No wrong-checkout review was accepted. The change was manually reviewed
against the exact worktree instead.


Context-selection tooling used six Jev requests (25,558 input tokens; reported
cost $0.001073) to narrow authority-guide and wiring reads. Its partial results
were checked against the exact worktree source; no memory-derived behavior was
assumed current.
