# Verification Report — bahia-6hic.8

Date: 2026-07-03

## Scope verified

- `internal/service/assistant_orchestrator.go` now owns the `assistant.agentic.enabled` branch and keeps the default legacy planner path unchanged when the flag is false.
- `assistant/approval` validation accepts either `plan_hash` or `action_id`; `action_id` decisions resume `AssistantAgentLoop.ResumeAfterActionDecision`.
- `internal/service/assistant_session_recovery.go` resumes persisted `waiting_async` agent-loop sessions through `AssistantAgentLoop.ResumeAfterAsyncObservation`.
- `internal/app/app.go` adapts concrete `internal/mcp` registry/results to service-local runtime/schema interfaces and wires OpenAI model client, permission engine, transcript store, tool runtime, and loop behind `assistant.agentic.enabled`.

## Commands

```bash
go test ./internal/service -run 'TestAssistantOrchestrator|TestAssistantSessionRecoveryUsesLoopForWaitingAsync|TestAssistantToolRuntimeRestartRecoveryResumesWaitingAsync'
```

Result: passing.

```bash
go test ./internal/controlplane ./internal/app
```

Result: passing.

```bash
go build ./...
```

Result: passing.

## Notes

The broader `go test ./internal/service` suite was not used as the completion gate because this work item explicitly notes pre-existing SecurityScanner SBOM/Blossom fixture failures tracked by `bahia-x3km`. The targeted service tests cover the changed integration seams.

## Oracle review

Oracle review was run in review mode before commit. Actionable findings were addressed:

- Transcript key construction now fails closed instead of deriving from an empty service secret.
- `waiting_async` recovery now blocks/publishes when loop resume returns an error.
- Agent loop prompt/action errors defensively publish failed session state.
- `action_id` approvals return `agentic_disabled` while `assistant.agentic.enabled` is false.
