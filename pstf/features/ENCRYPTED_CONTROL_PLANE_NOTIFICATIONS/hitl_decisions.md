# HITL Review — ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS

## Decision Needed
Approve / Request Changes / Defer / Reject

## Feature Intent

This slice defines and verifies Bahia's signer-first encrypted browser request/result flow for sensitive notification operations.

What it is supposed to do:
- bootstrap from `/api/v1/system/info` using `nostr.service_pubkey` and `nostr.browser_encrypted_request_relays`
- publish sensitive browser requests as encrypted kind `5980` events to encrypted-request relays only
- receive correlated encrypted kind `7980` result events from the Bahia service pubkey
- support notification channel list/create/update/delete/test and delivery log retrieval without leaking payloads to public relays
- surface terminal errors in the UI while preserving valid user input where applicable

## Acceptance Criteria Summary

Current criteria cover:
- discovery availability and relay-boundary enforcement
- request event kind/tag/content rules
- accepted-OK publish semantics
- correlated result filtering and cleanup
- notification channel read/write operations over encrypted transport
- encrypted log success and failure handling
- backend decrypt-failure and unauthorized-request handling
- create/edit form error retention
- accessibility-critical headings, labels, and alert regions
- end-to-end proof that browser notification flows stay off public relays

Status note:
- the criteria file itself still says `draft`
- the implementation has been fully verified against those current criteria

## Verification Evidence

Executed and passing:
```bash
npm --prefix web run test:unit -- tests/unit/notifications-store.test.js tests/unit/encrypted-controlplane.test.js
go test ./internal/controlplane -run 'TestEncrypted'
npm --prefix web run test:e2e -- tests/e2e/notifications-encrypted-smoke.spec.js tests/e2e/notifications-form-error.spec.js
```

Observed evidence threshold met:
- 13 unit tests passed across encrypted controlplane + notifications store
- encrypted backend transport tests passed
- 4 Playwright tests passed covering:
  - encrypted notifications happy path
  - create-form error retention
  - edit-form error retention
  - notifications accessibility-critical UI semantics

Artifact state:
- `feature_spec.json` marks the slice `fully_verified`
- `test_matrix.json` marks the matrix `ready`
- `defects.json` shows prior gaps resolved and `verified`
- `verification_report.md` marks the slice fully verified for implementation behavior

## Open Risks

There are no open product defects or open test defects for this slice.

Remaining risks are process/spec-governance only:
1. `acceptance_criteria.json` is still marked `draft`, so approval state is not yet reflected in the artifact set.
2. The encrypted operation catalog is still not a first-class normative table in the docs, even though this slice is now well tested.

## Required Human Decisions
1. Should `ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS` be approved as a completed PSTF slice and `acceptance_criteria.json` promoted from `draft` to `approved`?
2. Should the encrypted operation catalog be promoted to a normative spec table alongside public control-plane command families, or remain implementation/documentation by example?

## Recommended Decision

**Approve**

Reason:
- all currently defined acceptance criteria have executable passing coverage
- prior verification gaps are closed and reverified
- no unresolved product defects remain
- remaining questions are governance/documentation questions, not implementation correctness questions

If you want to be stricter on PSTF process, approve the slice and separately require a small follow-up to flip `acceptance_criteria.json` to `approved` and record the encrypted operation catalog decision.

## Decision Log
- 2026-05-03: Pending human review — packet prepared with recommendation to approve based on passing unit, backend, and Playwright evidence.
