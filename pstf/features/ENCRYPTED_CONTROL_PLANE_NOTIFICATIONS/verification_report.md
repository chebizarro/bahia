# Verification Report — ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS

## Summary

This PSTF slice is grounded and largely verified at the unit and backend transport layers.

The encrypted control-plane contract for notifications is well-supported by:
- current control-plane docs
- frontend encrypted transport tests
- frontend notification-store tests
- backend encrypted transport tests

The main remaining weakness is the lack of selected browser E2E evidence for the full notifications journey over the encrypted transport.

## Commands Run

```bash
npm --prefix web run test:unit -- tests/unit/encrypted-controlplane.test.js tests/unit/notifications-store.test.js
go test ./internal/controlplane -run 'TestEncrypted'
```

## Results

- `npm --prefix web run test:unit -- tests/unit/encrypted-controlplane.test.js tests/unit/notifications-store.test.js`
  - **passed**
  - 2 files, 12 tests
- `go test ./internal/controlplane -run 'TestEncrypted'`
  - **passed**

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
| ECPN-AC-010 | gap | No selected browser E2E proves the notifications page over encrypted transport end-to-end. |

## Test Matrix Status

- Criteria total: 10
- Criteria fully passing: 9
- Criteria with known gaps: 1
- Overall current-evidence status: **strong but not complete**

## Defects

No implementation defect was identified while producing this PSTF slice.

The main issue is a verification/documentation gap rather than a confirmed behavioral defect:
- missing browser E2E coverage for the notifications encrypted journey
- encrypted operation catalog not documented as comprehensively as the public control-plane families

## Ambiguities / Human Decisions Needed

1. Should encrypted-domain browser journeys have mandatory E2E coverage before being considered production-complete?
2. Should the encrypted operation catalog become a first-class normative spec table per domain?
3. Is the current split between unit/backend transport verification and route-level verification acceptable for this slice?

## Confidence Assessment

**Confidence: medium-high**

Why not full high:
- The transport and store behavior are strongly evidenced.
- The backend decrypt/authorize/result flow is strongly evidenced.
- The selected evidence does not yet include a full browser E2E proof for the notifications feature journey.

## Recommendation

This slice is suitable as the **first PSTF onboarding slice**.

Next actions:
1. Treat the current artifacts as the working feature contract.
2. Add at least one browser E2E path for notifications over encrypted transport.
3. Decide whether to formalize the encrypted operation catalog at the same level as the public command families.
