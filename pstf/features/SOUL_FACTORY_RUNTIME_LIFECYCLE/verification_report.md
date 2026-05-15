# Verification Report — SOUL_FACTORY_RUNTIME_LIFECYCLE

## Summary

Product/protocol artifacts exist for the SoulFactory runtime lifecycle slice. Previous buckets implemented Bahia lifecycle ownership unification for kind `1950` actions. This bucket adds Bahia-side runtime adapters for OpenClaw and Metiq while intentionally avoiding draft-backed provisioning wiring, route UX, and OpenClaw/Swarmstr runtime bridge execution changes.

## Artifact status

- `feature_spec.json` — created
- `acceptance_criteria.json` — created with 8 acceptance criteria
- `test_matrix.json` — created with 13 mapped planned tests
- `defects.json` — created with seeded plan-derived dependencies/risks
- `hitl_decisions.md` — created
- `docs/soulfactory-runtime-control.md` — created as shared runtime-control contract

## Acceptance Criteria Status

| AC ID | Status | Basis |
| --- | --- | --- |
| SFRL-AC-001 | Planned | Requires implementation/static audit evidence. |
| SFRL-AC-002 | Planned | Requires relay-bus tests from Work Item 4. |
| SFRL-AC-003 | Planned | Requires draft/read-model implementation tests. |
| SFRL-AC-004 | Artifact-ready | Contract document exists; shared fixture tests remain planned. |
| SFRL-AC-005 | Partially verified for Bahia adapter bucket | `go test ./internal/soulfactory` covers Bahia parsing/discovery of compatible OpenClaw and Metiq `30317` capabilities, controller/method gating, deterministic selection, and relay-hint parsing. UI and bridge publication evidence remain planned. |
| SFRL-AC-006 | Partially verified for Bahia adapter bucket | `go test ./internal/soulfactory` covers signed Bahia `38384` request construction, required operator/soul/target/idempotency/spec tags, relay OK publication through the transport, correlated `38386` result acceptance, and rejected/failed runtime responses. Runtime-side validation/idempotency remains planned for bridge buckets. |
| SFRL-AC-007 | Verified for Bahia lifecycle bucket | `go test ./internal/soulfactory` covers canonical 6950/7950 lifecycle results, migration alias default-off behavior, and replay idempotency for duplicate kind 1950 delivery. |
| SFRL-AC-008 | Planned | Requires OpenClaw and Metiq vertical-slice e2e tests. |

## Test Matrix Status

- Total tests in matrix: 13
- Passing/partial evidence: lifecycle-handler compatibility/idempotency slice plus Bahia runtime-adapter capability/control-result slice (`go test ./internal/soulfactory`)
- Planned: bridge/UI/e2e portions remain planned
- Blocked by implementation: draft-backed provisioning wiring, UI runtime selection, and cross-runtime e2e slices

## Verification evidence from this bucket

Verification runs:

- `go test ./internal/soulfactory` passed on 2026-05-14 local time. Evidence covers the Bahia-only lifecycle unification bucket: `lifecycle_handler.go` is the kind `1950` orchestrator, lifecycle actions emit `6950/7950` tags, legacy `1951` remains migration-only/default-off, and replayed action events do not duplicate signer side effects.
- `go test ./internal/soulfactory/...` passed on 2026-05-15 local time for Bahia runtime adapters. Evidence covers `30317` OpenClaw/Metiq capability parsing, runtime-scoped discovery filters, draft/capability/NIP-65 relay selection, service-key signed `38384` requests with required correlation tags/content, deterministic capability selection, correlated `38386` success acceptance, and rejected/failed runtime response handling.
- `go test ./...` passed on 2026-05-15 local time as the broader Go quality gate.

## Open dependencies / risks

See `defects.json` for seeded dependencies related to relay-bus readiness, shared fixtures, and REST-only deployment paths.
