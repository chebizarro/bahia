# Verification Report — bahia-qtoq

Date: 2026-06-02

## Scope

Shared public, DNS, encrypted browser, and operator Nostr request/result lifecycle semantics.

## Verification status

Verified for the implemented slice.

## Evidence

- `npm test -- --run tests/unit/controlplane-requests.test.js tests/unit/public-controlplane.test.js tests/unit/dns-controlplane.test.js tests/unit/encrypted-controlplane.test.js`
  - Result: 4 files passed, 36 tests passed.
- `go test ./pkg/client ./internal/controlplane`
  - Result: `ok github.com/openagentsinc/bahia/pkg/client`; `ok github.com/openagentsinc/bahia/internal/controlplane`.

## Criteria coverage

- QTOQ-AC-001: status events cannot satisfy terminal command completion.
  - `web/tests/unit/public-controlplane.test.js` verifies LLM deploy/rollback await only `7973` terminal results.
  - `web/tests/unit/dns-controlplane.test.js` keeps DNS `6941` status separate from `794x` result completion.
- QTOQ-AC-002: request/result correlation is required.
  - `web/tests/unit/controlplane-requests.test.js` verifies scoped `#e` correlation and author filtering.
  - `web/tests/unit/encrypted-controlplane.test.js` verifies cleartext tags and decrypted `request_event_id` correlation.
  - `pkg/client/operator_nostr_test.go` verifies signed, correlated operator replies and ignores invalid/uncorrelated replies.
- QTOQ-AC-003: publish OK and zero-accepted outcomes are explicit.
  - Public and encrypted browser helpers reject zero accepted OKs.
  - Operator tests cover zero accepted relay publish as pre-acceptance failure.
- QTOQ-AC-004: CLOSED and AUTH failures are explicit.
  - Public and encrypted browser tests cover auth closure and all-known-relay closure.
  - Operator tests cover post-acceptance reply subscription closure.
- QTOQ-AC-005: abort and timeout semantics are explicit and deterministic.
  - Public helper tests cover explicit AbortSignal cancellation.
  - Encrypted helper tests cover configured timeout.
  - Operator tests cover context cancellation after publish acceptance.

## Contract summary

- Status events are progress-only.
- Terminal completion requires correlated terminal result kinds.
- Publish requires at least one accepted relay OK.
- AUTH, CLOSED, zero-accepted publish, abort, and timeout are explicit non-success outcomes.

## Remaining work

No remaining work is known within this slice. EOSE helper behavior, kind-catalog consolidation, and audit taxonomy updates remain owned by sibling Beads in the orchestration plan.
