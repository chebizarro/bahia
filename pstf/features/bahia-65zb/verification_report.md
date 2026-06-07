# Verification Report: bahia-65zb

Date: 2026-06-07

## Intended behavior

Representative transitional REST write endpoints with existing Nostr analogs publish signer-first ContextVM/Nostr commands and return `dto.CommandReceipt` metadata. REST receipts are acknowledgments only; durable progress and terminal truth come from scoped subscriptions to canonical observables.

## Observed behavior before implementation

ML mutation handlers already returned command receipts. Services, deployment intents, LLM route creation, and policy writes either were not mounted as Nostr-backed receipt routes or did not publish command receipts through the REST surface.

## Implementation evidence

- Added REST command-receipt helpers in `internal/api/handlers/command_receipts.go`.
- Added Nostr-backed REST write handlers for:
  - `POST /api/v1/services`
  - `POST /api/v1/deployments/intents`
  - `POST /api/v1/llm/routes`
  - `POST /api/v1/policies`
  - `PUT /api/v1/policies/{id}`
  - `DELETE /api/v1/policies/{id}`
- Added `service/create` and policy `create/update/delete` ContextVM command publishers using existing signer and relay-publisher infrastructure.
- Wired app/router dependencies so production routes require configured control-plane publishers; no fake production publisher is added.
- Added `policies:read`, `policies:write`, and `llm_routes:write`; policy mutations now require `policies:write` scoped to the target environment/existing policy, and LLM route creation requires org-scoped `llm_routes:write`.
- Preserved partial policy update semantics by omitting unset fields instead of serializing omitted booleans as `false`.
- Added LLM route-create validation so empty names fail before any publish attempt.
- REST `CommandReceipt` adapters now preserve publisher-supplied `d_tag` and `timeout_seconds`, defaulting timeout only when omitted.
- Updated API and user-guide docs to describe transitional `202` command receipts, Nostr observable completion, and the `policies:write` authorization requirement.

## Verification

- `go test ./internal/domain ./internal/controlplane ./internal/api/handlers ./internal/api/router` — passed.
- `go test ./internal/app ./internal/api/...` — passed.

## Review evidence

Oracle reviews identified policy authorization, policy partial update semantics, LLM empty-name validation, direct LLM route-create publisher coverage, LLM route-create RBAC, command receipt metadata preservation, and policy write org-scope resolution. Those findings were addressed before the final verification commands above.

## Scope guard

The integration compile-fix file `test/integration/e2e_ci_registry_test.go` is outside this Bead's scope and was not inspected or edited for this implementation.

## Status

AC1, AC2, AC3, AC4, AC5, and AC6 are verified for the targeted representative vertical slice.
