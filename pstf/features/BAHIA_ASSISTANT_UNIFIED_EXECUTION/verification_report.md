# Verification report — assistant unified execution

Item 1 is a contract/classifier delivery, not production activation. The Go
hash fixtures were generated independently with Node's JSON serialization and
lexicographic UTF-16 key ordering, then verified byte-for-byte in Go. RFC 8785
Appendix B number cases and invalid I-JSON Unicode/duplicate-key cases are also
covered by focused tests.

The checkpoint kind/tag verdict comes from the Bahia Nostr implementation guide
and Cascadia NIP-CAS-0001: 4903 regular append-only audit, required
`domain`/`type`/`schema`; `session`/`run` scope and optional `e`/`p`; no `d`.
Publication, accepted OK and retention are unverified until item 2.

Quality-gate outcomes are recorded at item 1 handoff after execution. Items 2,
3 and 4 acceptance criteria remain unverified here.

## Item 1 gate evidence (2026-09-26, macOS worktree `bahia-asst`)

- `go build ./...` — pass.
- `go vet ./...` — pass.
- `go test ./...` — pass, including `internal/api/router`; the known
  `bahia-frj9d` hang did not occur in this run, so no exclusion was used.
- `golangci-lint run --max-issues-per-linter=0 --max-same-issues=0` — exits 1
  with exactly one known pre-existing `unused` finding:
  `internal/controlplane/continuity_definition_handlers.go:42:19`,
  `(*Reactor).handleStandbyNodeDefinition`. No item 1 finding was added.
- Focused `go test ./internal/domain ./internal/service -run
  'TestAssistant|TestClassify|TestDecode' -count=1` — pass after enum validation.

These gates prove only the additive item 1 code and existing test suite. They
do not prove checkpoint publication, unified dispatch, browser behavior or live
migration.

Final rerun after checkpoint-envelope, strict I-JSON, and migrated-hash fixes:
`go build ./...` and `go vet ./...` passed; `go test ./...` passed all 90
reported packages; uncapped `golangci-lint` again reported only the same known
`handleStandbyNodeDefinition` finding. No router exclusion was needed.

## Item 2 gate evidence (2026-09-26, macOS worktree `bahia-asst`)

Item 2 (`bahia-0th5g`) delivers the executor, store, observer, runtime
gateway, evidence resolver and single recovery path; production DI and the
atomic switch remain item 3, so these results prove the executor through
service-level seams (deterministic in-memory relay with OK/EOSE/CLOSED, real
encrypted checkpoint store, real transcript store and observer), not live
relays or the joined browser flow.

- `go build ./...`, `go vet ./...` — pass.
- `go test ./...` — pass, 83 packages including `internal/api/router`; the
  `bahia-frj9d` hang did not occur, so no exclusion was used.
- `GOFLAGS=-p=1 go test -race ./...` — pass, 83 packages, no data race.
- `go test -race ./internal/service -run 'TestAssistantExecution|TestAssistantRecovery|TestAssistantCheckpoint|TestAssistantTranscript' -count=25` — pass (flake check).
- `golangci-lint run --max-issues-per-linter=0 --max-same-issues=0` — only the
  known `handleStandbyNodeDefinition` finding.
- Mutation checks: dispatching after a rejected reservation, and removing the
  session fence, each make `TestAssistantExecutionCheckpointFailureAtEveryBoundaryBlocksSafely`
  fail; the boundary matrix covers all 13 checkpoint revisions of a
  sync -> async -> sync batch.

Criteria A7-A14 map item 2 claims to tests. A5's
`TestAssistantUnifiedExecutionIntegration` (joined provider -> approval ->
restart) is still owed by item 3.

## Item 3 gate evidence (2026-09-26, macOS worktree `bahia-asst`)

Item 3 (`bahia-oknmu`) is the atomic switch: routing, proposers, config, DI and
removal of the v1 dispatchers. A5 now names the joined tests
(`TestAssistantUnifiedExecutionJoined*`); A15-A20 map the item 3 claims.
The joined tests run real service wiring end to end (production `ChatClient`
and `OpenAIAgentClient` against an `httptest` provider, real transcript and
checkpoint stores, observer, runtime, permission engine, both proposers,
engine, orchestrator and recovery runner) with only the relay and the tool
provider as deterministic boundaries, and rebuild every service on restart.

- `go build ./...`, `go vet ./...` — pass.
- `go test ./...` — pass, 83 packages including `internal/api/router`; the
  `bahia-frj9d` hang did not occur, so no exclusion was used.
- `GOFLAGS=-p=1 go test -race ./...` — pass, 83 packages, no data race; after
  three final `internal/service`-only edits, `GOFLAGS=-p=1 go test -race` of
  `internal/service`, `internal/controlplane`, `internal/app` and
  `internal/config` passed again.
- `go test -race ./internal/service -run 'TestAssistantUnifiedExecutionJoined|TestAssistantOrchestrator|TestAssistantIterative|TestAssistantRuntime' -count=10` — pass (flake check).
- `golangci-lint run --max-issues-per-linter=0 --max-same-issues=0` — only the
  known `handleStandbyNodeDefinition` finding.

Not proven here: live relays, the browser-connected E2E (item 4's skipped
spec) and live migration inventory.
