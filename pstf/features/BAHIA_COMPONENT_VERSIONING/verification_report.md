# BAHIA_COMPONENT_VERSIONING Verification Report

## Scope

Implemented component version metadata for separately packaged Bahia artifacts and surfaced it on the web Settings page.

## Evidence

- `go test ./internal/version ./internal/adapters/nostr ./internal/api/router` passed.
- `make build` passed and stamped Go build flags with `0.1.0-2089fa826fddf149f1789ddf712f3bea4f913733`.
- `npm run test:unit -- --run tests/unit/version.test.js` passed (3 tests, including missing backend `versions` compatibility).
- `npm run build` passed. The build emitted pre-existing Svelte warnings in unrelated files (`src/routes/policies/+page.svelte`, `src/lib/components/assistant/AssistantPlanApproval.svelte`) and a pre-existing qrcode import warning, but completed successfully.

## Review Hardening

An Oracle review pass identified SemVer edge cases, redundant direct router linker stamping, and older discovery payload compatibility as actionable hardening items. Follow-up changes added commit identifier sanitization/fallback tests, removed direct `router.Version` ldflags in favor of the shared version package, and added a frontend test for discovery payloads without `versions`.

## Acceptance Mapping

- AC1: Covered by `internal/version` tests and `make build`.
- AC2: Covered by `internal/adapters/nostr` system discovery test.
- AC3: Covered by `web/tests/unit/version.test.js` and production web build.
