# Verification Report — ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS

## Summary

The encrypted notifications slice is **mostly verified but not fully verified**.

Current automated evidence confirms the core encrypted transport, notification channel list/mutation flows, backend decrypt/authorization handling, and the browser notifications journey over encrypted relays. The implementation satisfies every acceptance criterion that has executable coverage today.

The slice is not fully verified because three criteria still depend on tests that do not exist yet:
- `ECPN-AC-008` lacks negative-path verification for encrypted log retrieval failure handling.
- `ECPN-AC-010` lacks browser proof that valid form data survives encrypted submit failure.
- `ECPN-AC-011` lacks explicit accessibility assertions for labeled controls and alert behavior.

Those are verification gaps, not confirmed product defects.

## Commands Run

```bash
npm --prefix web run test:unit -- tests/unit/encrypted-controlplane.test.js tests/unit/notifications-store.test.js
go test ./internal/controlplane -run 'TestEncrypted'
npm --prefix web run test:e2e -- tests/e2e/notifications-encrypted-smoke.spec.js
```

## Acceptance Criteria Status

| Criterion | Status | Basis |
|---|---|---|
| ECPN-AC-001 | pass | `web/tests/unit/notifications-store.test.js` proves store-side failure before publish when encrypted discovery is unavailable. |
| ECPN-AC-002 | pass | `web/tests/unit/encrypted-controlplane.test.js` proves public relay URLs do not count as encrypted request relays. |
| ECPN-AC-003 | pass | `web/tests/unit/encrypted-controlplane.test.js` proves request kind `5980`, service pubkey targeting, encrypted wire tag, and ciphertext payload construction. |
| ECPN-AC-004 | pass | `web/tests/unit/encrypted-controlplane.test.js` proves publish requires an accepted OK and propagates relay rejection reasons. |
| ECPN-AC-005 | pass | `web/tests/unit/encrypted-controlplane.test.js` proves correlation by `#e`, `#p`, service author, and duplicate-safe cleanup. |
| ECPN-AC-006 | pass | `web/tests/unit/notifications-store.test.js` and `web/tests/e2e/notifications-encrypted-smoke.spec.js` prove channel listing loads and renders via encrypted transport. |
| ECPN-AC-007 | pass | `web/tests/unit/notifications-store.test.js` plus `web/tests/e2e/notifications-encrypted-smoke.spec.js` prove channel create/test flow stays on encrypted operations and updates local state. |
| ECPN-AC-008 | partial | Success-path coverage exists in `web/tests/unit/notifications-store.test.js`, but failure semantics for clearing stale logs and setting `logsError` are not automated. Source inspection of `web/src/lib/stores/notifications.svelte.js` indicates the intended behavior is implemented. |
| ECPN-AC-009 | pass | `internal/controlplane/encrypted_transport_test.go` proves decrypt failures and unauthorized requesters receive terminal encrypted errors without handler dispatch. |
| ECPN-AC-010 | partial | Source inspection of `web/src/routes/notifications/new/+page.svelte` and `web/src/routes/notifications/NotificationChannelForm.svelte` indicates form values should persist after submit failure, but no automated test proves it. |
| ECPN-AC-011 | partial | Source inspection shows headings, labels, and `role=alert` markup, but the selected test suite does not explicitly assert those accessibility behaviors. |
| ECPN-AC-012 | pass | `web/tests/e2e/notifications-encrypted-smoke.spec.js` proves the browser journey publishes only to encrypted relay URLs and not to public relay URLs. |

## Test Matrix Status

### Executed tests

| Test ID | Status | Evidence |
|---|---|---|
| ECPN-T-001 | pass | Included in `npm --prefix web run test:unit -- tests/unit/notifications-store.test.js` (12/12 tests passed across both unit files). |
| ECPN-T-002 | pass | Included in `npm --prefix web run test:unit -- tests/unit/encrypted-controlplane.test.js tests/unit/notifications-store.test.js`. |
| ECPN-T-003 | pass | Same unit run as above. |
| ECPN-T-004 | pass | Same unit run as above. |
| ECPN-T-005 | pass | Same unit run as above. |
| ECPN-T-006 | pass | `npm --prefix web run test:e2e -- tests/e2e/notifications-encrypted-smoke.spec.js` passed (1/1). |
| ECPN-T-007 | pass | Same Playwright run as above. |
| ECPN-T-008 | pass | Included in the notifications store unit run. |
| ECPN-T-009 | pass | Included in the notifications store unit run. |
| ECPN-T-011 | pass | `go test ./internal/controlplane -run 'TestEncrypted'` passed. |
| ECPN-T-012 | pass | Same Go test run as above. |

### Missing tests

| Test ID | Status | Classification |
|---|---|---|
| ECPN-T-010 | not_implemented | test defect / verification gap |
| ECPN-T-013 | not_implemented | test defect / verification gap |
| ECPN-T-014 | not_implemented | test defect / verification gap |

Overall matrix status: **draft**.

## Defects

The current verification produced **three open test defects** and **no confirmed product defects**.

- `ECPN-D-001` — Missing negative-path automated coverage for encrypted notification log retrieval
- `ECPN-D-002` — Missing browser verification for notification form state retention after encrypted submit failure
- `ECPN-D-003` — Accessibility assertions for notifications routes are incomplete

See `pstf/features/ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS/defects.json` for structured details.

## Ambiguities / Human Decisions Needed

1. Should the slice remain in `draft` acceptance status until the three missing tests exist, or is source inspection sufficient to upgrade `ECPN-AC-010` and `ECPN-AC-011` to approved with manual evidence?
2. Should encrypted notification log failure semantics (`ECPN-AC-008`) be release-gating, or is success-path coverage sufficient for this onboarding slice?
3. Should accessibility coverage for this slice stay browser-E2E based, or should there be a dedicated accessibility test layer for PSTF slices?

## Confidence Assessment

**Confidence: medium-high**

Why:
- The implemented transport and core browser flow have direct passing automated evidence.
- The remaining gaps are narrow and test-focused rather than broad architectural unknowns.
- The slice is not fully verified because three acceptance criteria still rely on source inspection instead of executable evidence.

## Recommendation

Do **not** treat `ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS` as fully verified yet.

Recommended next steps:
1. Implement `ECPN-T-010` to close the encrypted log failure-path gap.
2. Implement `ECPN-T-013` to verify form-value persistence after encrypted submit failure.
3. Implement `ECPN-T-014` to verify accessibility-critical labels and alert behavior.
4. After those tests pass, rerun PSTF verification and upgrade the slice from `draft` to `fully_verified` if no product defects emerge.
