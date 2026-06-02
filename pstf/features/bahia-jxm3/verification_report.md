# Verification Report — bahia-jxm3

Date: 2026-06-02

## Scope

ML browser import/deploy REST-to-Nostr bridge semantics and receipt verification.

## Verification status

Verified for the implemented/documented slice.

## Evidence

- `go test ./internal/controlplane ./internal/api/router`
  - Result: `ok github.com/openagentsinc/bahia/internal/controlplane`; `ok github.com/openagentsinc/bahia/internal/api/router`.
- `npm run test:unit -- --run tests/unit/route-transport-matrix.test.js`
  - Result: 1 file passed, 5 tests passed.

## Criteria coverage

- JXM3-AC-001: ML ingress policy is recorded.
  - `route_transport_matrix.json`, `docs/control-planes.md`, `docs/user-guide/features/ml-models.md`, and `web/src/routes/ml/+page.svelte` classify ML browser import/deploy as `rest_to_nostr_bridge`.
- JXM3-AC-002: HTTP receipt exposes Nostr correlation and publish acceptance semantics.
  - `TestMLRESTAsyncRoutesReturnNostrCorrelationMetadata` asserts `202 Accepted` receipts include Nostr request/result metadata, relay acceptance count, timeout metadata, and completion guidance.
  - `TestMLRESTAsyncRoutePublishFailureDoesNotReturnSubmittedReceipt` asserts bridge publish failures return `502` without submitted receipt metadata.
- JXM3-AC-003: ML command publisher verifies relay acceptance.
  - `TestMLCommandPublisherPublishesAddressableDeployRequestWithCorrelation` verifies signed scoped request events and receipt correlation.
  - `TestMLCommandPublisherFailsWhenNoRelayAccepts` now supplies a valid deploy payload and proves zero accepted relays fail after payload validation.

## Remaining work

No remaining work is known within this slice.
