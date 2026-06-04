# Verification Report — bahia-gkg7

Date: 2026-06-03

## Scope

ML web import/deploy Nostr-native migration and web API client pruning.

## Verification status

Verified for this changeset.

## Evidence

- `npm run test:unit -- --run tests/unit/api-client.test.js tests/unit/api-client-core.test.js tests/unit/api-client-extended.test.js tests/unit/api-client-retry-and-edges.test.js tests/unit/route-transport-matrix.test.js tests/unit/repositories-store.test.js tests/unit/stores-index.test.js`
  - Result: 7 files passed, 48 tests passed.
- `npm run test:unit -- --run tests/unit/auth-store.test.js -t "installs direct NIP-98 provider"`
  - Result: 1 test passed, 34 skipped.
- `npm run build`
  - Result: passed. Existing Svelte warnings remain in policies and AssistantPlanApproval files outside this change scope.
- `go test ./internal/controlplane ./internal/api/router`
  - Result: passed.
- Static stale-string checks
  - Removed ML bridge method names no longer appear in `web/`, `docs/`, `pstf/`, or `internal/`.
  - `/ml` no longer imports `$lib/api/client.js`; only artifact Blossom/SBOM pages remain route-level REST client exceptions.

## Notes

A full `auth-store.test.js` run still exposes an unrelated pre-existing NIP-44 capability-state failure. The touched direct NIP-98 test passes independently.
