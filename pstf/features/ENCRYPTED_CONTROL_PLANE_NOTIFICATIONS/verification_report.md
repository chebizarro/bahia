# Verification Report — ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS

## Summary

This PSTF slice is now fully verified against the selected evidence set.

The encrypted control-plane contract for notifications is supported by:
- current control-plane docs
- frontend encrypted transport unit tests
- frontend notification-store unit tests
- backend encrypted transport tests
- route-access unit coverage for signer-first access
- a browser E2E proving the notifications journey over encrypted transport

A real product inconsistency was found during verification:
- `/notifications` was still gated by REST compatibility in `route-access.js`

That gate was removed, the route-access unit coverage was updated, and the browser E2E now passes without any test-only compatibility override.

## Commands Run

```bash
npm --prefix web run test:unit -- tests/unit/route-access.test.js tests/unit/encrypted-controlplane.test.js tests/unit/notifications-store.test.js
go test ./internal/controlplane -run 'TestEncrypted'
npm --prefix web run test:e2e -- tests/e2e/notifications-encrypted-smoke.spec.js
```

## Results

- `npm --prefix web run test:unit -- tests/unit/route-access.test.js tests/unit/encrypted-controlplane.test.js tests/unit/notifications-store.test.js`
  - **passed**
  - 3 files, 17 tests
- `go test ./internal/controlplane -run 'TestEncrypted'`
  - **passed**
- `npm --prefix web run test:e2e -- tests/e2e/notifications-encrypted-smoke.spec.js`
  - **passed**
  - 1 Playwright browser E2E

## Acceptance Criteria Status

| Criterion | Status | Notes |
|---|---|---|
| ECPN-AC-001 | pass | Relay discovery uses only `browser_encrypted_request_relays`. |
| ECPN-AC-002 | pass | Store fails before publish if encrypted discovery contract is absent. |
| ECPN-AC-003 | pass | Request event construction, tags, recipient pubkey, and ciphertext behavior are covered. |
| ECPN-AC-004 | pass | Publish requires accepted OK; relay rejection reasons propagate. |
| ECPN-AC-005 | pass | Result correlation by request id, requester pubkey, and service author is covered. |
| ECPN-AC-006 | pass | Backend decrypt failure and unauthorized requester handling are covered. |
| ECPN-AC-007 | pass | Notification channel/log operations are covered as encrypted-only store paths. |
| ECPN-AC-008 | pass | Terminal encrypted errors surface to callers. |
| ECPN-AC-009 | pass | Public relay set is not treated as encrypted-request relay set. |
| ECPN-AC-010 | pass | Browser E2E now proves the notifications page over encrypted transport end-to-end. |

## Test Matrix Status

- Criteria total: 10
- Criteria with tests: 10
- Criteria fully passing: 10
- Known gaps in current selected evidence: 0
- Overall slice status: **fully verified**

## Defects

### Resolved during verification
- **Stale route gate for `/notifications`**
  - Problem: the route still required REST compatibility even though the page uses encrypted signer-first transport.
  - Evidence: browser E2E initially needed a test-only override to access `/notifications`.
  - Resolution: removed `/notifications` from `ROUTE_COMPATIBILITY_REQUIREMENTS` and updated `web/tests/unit/route-access.test.js`.

No remaining behavioral defects were identified in the selected slice.

## Ambiguities / Human Decisions Needed

1. Should the encrypted operation catalog be promoted to a first-class normative table alongside public command families?
2. Is one browser E2E for notifications sufficient release evidence for the encrypted control-plane slice, or should additional edit/delete/log scenarios be required?

These do not block the current slice from being considered fully verified against the selected acceptance criteria.

## Confidence Assessment

**Confidence: high**

Why high:
- Unit tests cover relay discovery, request construction, publish OK handling, result correlation, and notification store behavior.
- Backend tests cover decrypt failure, unauthorized requester handling, and encrypted result publication.
- Browser E2E now proves an actual notifications journey over encrypted transport without relying on a compatibility-gate override.

## Recommendation

Treat `ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS` as the reference PSTF onboarding slice.

Recommended next moves:
1. Reuse this slice structure for the core service-to-deployment slice.
2. Decide whether to formalize the encrypted operation catalog as a normative spec table.
3. Optionally expand browser E2E coverage to edit/delete/log scenarios if you want broader regression protection, but this is no longer required to call the current slice fully verified.
