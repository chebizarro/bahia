# Verification Report — SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME

## Summary

The signer-first adoption/import and direct-runtime operator slice is **fully verified against the approved acceptance criteria and ready test matrix**.

Current evidence covers:
- CLI relay precedence and explicit fallback boundaries
- raw-target compatibility-only behavior for both scan and import
- subscribe-before-publish request handling in the operator client
- correlated and deduplicated reply handling
- signer-first adoption target validation (`docker_host` forbidden, endpoint-ref-based target content on valid requests)
- scoped authorization for adoption and direct-runtime requests
- sanitized adoption preview/import result shaping
- direct-runtime artifact validation, scoped authorization, processing status, and runtime result DTOs across deploy, restart, and stop
- stderr-only progress reporting in table mode

Policy is now explicit: deterministic package tests establish the implementation slice, a stored local Docker+relay rehearsal artifact is required before release approval, and staged/live signer-first signoff remains the production enablement gate.

## Commands Run

```bash
go test ./internal/controlplane -run 'TestHandle(ServiceAction|Adoption)'
go test ./internal/api/handlers -run 'Test(AdoptionHandler|ServiceActionHandler|RuntimeLifecycleErrorStatusMapping)'
go test ./pkg/client -run 'TestOperator'
go test ./cmd/cli -run 'Test(RootCommandExposesOperatorFlags|ResolveOperatorRelaysPrecedence|ServiceActionCommandUsesSignerFirstClientByDefault|AdoptRawTargetCommandRequiresExplicitFallback|OperatorHTTPFallbackOnlyForExplicitPreAcceptanceFailures|RawTargetRequiresExplicitFallbackAndSkipsNostrWhenAllowed|OperatorStatusCallbackWritesOnlyInTableModeToStderr)'
```

## Acceptance Criteria Status

| Criterion | Status | Basis |
|---|---|---|
| SFOAR-AC-001 | pass | `cmd/cli/operator_nostr_test.go` proves flag → env → system discovery precedence and deduplication. |
| SFOAR-AC-002 | pass | `cmd/cli/operator_nostr_test.go` proves raw-target requires explicit fallback and skips signer-first publish for both scan and import when fallback is used. |
| SFOAR-AC-003 | pass | `pkg/client/operator_nostr_test.go` proves subscribe-before-publish and correlated reply filtering/deduplication. |
| SFOAR-AC-004 | pass | `pkg/client/operator_nostr_test.go` and `internal/controlplane/reactor_operator_actions_test.go` prove signer-first adoption forbids `docker_host`, publishes endpoint-ref-only request content, and normalizes target tags reactor-side. |
| SFOAR-AC-005 | pass | `internal/controlplane/reactor_operator_actions_test.go` proves unauthorized scan and import requests fail closed without service dispatch. |
| SFOAR-AC-006 | pass | `internal/controlplane/reactor_operator_actions_test.go` plus `internal/api/handlers/adoption_handlers_test.go` prove sanitized preview DTO behavior and docker-host omission. |
| SFOAR-AC-007 | pass | `internal/controlplane/reactor_operator_actions_test.go` proves mixed import outcomes publish `partial_failure` and preserve per-result status/error data. |
| SFOAR-AC-008 | pass | `internal/controlplane/reactor_operator_actions_test.go` proves invalid artifact usage fails validation, unauthorized requests fail closed, and valid authorized actions execute. |
| SFOAR-AC-009 | pass | `internal/controlplane/reactor_operator_actions_test.go` and `pkg/client/operator_nostr_test.go` prove signed `6963`/`7962` sequencing and DTO decoding across deploy, restart, and stop. |
| SFOAR-AC-010 | pass | `cmd/cli/operator_nostr_test.go` plus `pkg/client/operator_nostr_test.go` prove fallback is limited to pre-acceptance failures. |
| SFOAR-AC-011 | pass | `cmd/cli/operator_nostr_test.go` proves status chatter is stderr-only in table mode. |

## Test Matrix Status

| Test ID | Status | Evidence |
|---|---|---|
| SFOAR-T-001 | pass | Included in the focused `go test ./cmd/cli` run. |
| SFOAR-T-002 | pass | Included in the focused `go test ./cmd/cli` run, including raw-target fallback behavior for both scan and import. |
| SFOAR-T-003 | pass | Included in `go test ./pkg/client -run 'TestOperator'`. |
| SFOAR-T-004 | pass | Covered by `go test ./pkg/client -run 'TestOperator'` for endpoint-ref-only publish content and by `go test ./internal/controlplane -run 'TestHandle(ServiceAction|Adoption)'` for reactor-side normalized target tagging. |
| SFOAR-T-005 | pass | Included in `go test ./internal/controlplane -run 'TestHandle(ServiceAction|Adoption)'`, covering unauthorized scan and import requests. |
| SFOAR-T-006 | pass | Included in `go test ./internal/controlplane -run 'TestHandle(ServiceAction|Adoption)'`. |
| SFOAR-T-007 | pass | Included in `go test ./internal/controlplane -run 'TestHandle(ServiceAction|Adoption)'`. |
| SFOAR-T-008 | pass | Included in `go test ./internal/controlplane -run 'TestHandle(ServiceAction|Adoption)'`. |
| SFOAR-T-009 | pass | Included across the focused controlplane and pkg/client test runs, covering successful deploy, restart, and stop result flows. |
| SFOAR-T-010 | pass | Covered by the focused `go test ./cmd/cli` run plus `go test ./pkg/client -run 'TestOperator'` for pre-acceptance operator-client failures. |
| SFOAR-T-011 | pass | Included in the focused `go test ./cmd/cli` run. |

Overall matrix status: **ready**.

## Defects

No open product defects or open test defects were identified for the current signer-first operator slice scope.

## Ambiguities / Human Decisions Needed

Resolved in HITL review:
- Slice approval posture: `APPROVED_WITH_RISK`
- Acceptance criteria: `APPROVE_AS_IS`
- Rehearsal artifact gate: `REQUIRED_BEFORE_RELEASE_APPROVAL`

## Confidence Assessment

**Confidence: high**

Why:
- Verification spans the reactor, HTTP DTO mirror, operator client, and CLI boundary.
- The exercised tests explicitly cover the signer-first path and its fallback boundaries rather than relying on legacy REST execution.
- The remaining work is operational evidence capture, not an implementation or specification ambiguity.

## Recommendation

Treat `SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME` as fully verified for current implementation behavior.

Recommended next moves:
1. Use this slice as the reference PSTF pattern for future operator-only signer-first flows.
2. Require a stored Docker+relay rehearsal artifact before release approval for operator-only signer-first slices.
3. Keep staged/live SF-01 through SF-11 as the production enablement gate.
