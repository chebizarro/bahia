# Verification Report: bahia-cx3ms

Date: 2026-07-11

## Result

Passed.

## Evidence

- Replaced HiveCI live no-op decoders with concrete workflow-run and workflow-result decoders.
- Workflow-run events project into `domain.HiveCIWorkflowRun` and a running `DecodedQualityGate`.
- Workflow-result events project into `domain.HiveCIWorkflowResult` and a completed `DecodedQualityGate` with `pass`/`fail` result and merge-blocking failures.

## Tests

- `go test ./internal/adapters/nostr`
- `go test ./...`

Both commands passed on 2026-07-11.
