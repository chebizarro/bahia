# bahia-irsry.73 — canonical intent revision verification

## Observed defect and contract

The merged parser had duplicate `case string` branches and encoded revisions as incompatible Unix units. Canonical `30900` records publish `updated_at` as RFC3339Nano at PostgreSQL microsecond precision. An intent now carries that string unchanged on the wire; the daemon parses it to `time.Time`, rejects numeric or malformed values, and compares at canonical precision.

## Acceptance evidence

| Criterion | Evidence |
| --- | --- |
| Invalid revision never reaches a handler and a known author receives bounded rejection on relay and authenticated gift-wrap ingress | `TestParseIntent_RejectsInvalidRevision`, `TestIntentProcessor_InvalidRevisionPublishesBoundedRejection`, `TestSignedUnwrappedIntent_InvalidRevisionPublishesBoundedRejection` |
| All registered domain schemas parse the same revision string and distinguish an older token | `TestRegisteredDomainIntentRevisionWireContract` |
| Revision-bearing handler updates accept current and conflict on stale tokens | service, environment, policy, org, LLM, deployment, runtime, backup, ML, worker, secret, notification, and package handler tests in `internal/controlplane` |
| Go CLI and web signers emit compatible RFC3339 strings | `TestIntentPublisher_ParseIntentRoundTrip_WithExpectedUpdatedAt`, `TestIntentPublisher_PreservesCanonicalRevisionString`, `TestIntentCrossLanguageFixture`, web intent signer/fixture unit tests |
| Non-revisioned DNS operations do not silently ignore a supplied token | `TestD70DNSIntentRejectsUnsupportedRevision` |

`BAHIA_REGEN_FIXTURE=1` regenerated `intent-go.json` from Go and `intent-web.json` from Vitest. Deployment and D70 fixture content already used RFC3339 strings; D70's test now uses `json.Number` like the parser when comparing unrelated numeric fields.

## Gates

- `CGO_ENABLED=0 go build ./...`, `go vet ./...`, `go test ./...`: passed.
- `go test ./internal/archtest -run TestNoNew -v`: five tests passed, zero new violations.
- `gofmt -l` on changed Go files and `git diff --check`: clean.
- `pnpm run test:unit`: 1089 passed, 1 skipped.
- `pnpm run lint` and `pnpm run build`: passed.
- `CGO_ENABLED=0 CI=1 npx playwright test`: 211 passed, 4 skipped, 1 test passed on retry; its isolated rerun passed on first attempt.

The Beads Dolt server reported `database "beads_bahia" not found`, so issue status could not be changed without touching `.beads/`, which this integration task expressly prohibits. Revision checks remain optional when the intent omits `expected_updated_at`, as specified in Phase 3 §1.5; this fix does not change the existing persistence transaction boundaries.
