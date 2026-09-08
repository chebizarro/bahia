# FP_BAHIA_ARCANA_03_BUILD_UI verification

## Implemented

- Signed `build/request` ContextVM contract with exact public build-argument allowlist.
- Opaque, service-scoped credential references; no secret value field or browser persistence.
- Isolated `/builds` UI for request, projected status/log/evidence, and digest-pinned OCI candidates.
- Explicit fail-closed production wiring around fleet Gitea/HiveCI initiator availability.
- Fleet-local self-dispatch publishes grasp-compatible, tag-only kind `5401` and retains its signed event id as `ci_run_id`.
- Self-dispatched 5401 events pass Bahia's trusted subscriber and release lineage kind/id boundary; exact replay remains single-run and single-dispatch.
- Missing operator trust for the Bahia service pubkey emits `self_issued_run_untrusted` without modifying `trusted_ci_pubkeys`.
- Non-empty build arguments fail before credential resolution or external side effects because the interoperable tag-only 5401 contract has no build-argument field.

## Historical infrastructure blocker

`bahia-1tgwr` originally tracked the missing fleet Gitea private-mirror and HiveCI initiation adapter. The adapter now exists and remains the required boundary; the UI and handler must not be bypassed with a direct GitHub token-bearing runner.

## Verification

### 2026-09-08 self-dispatch protocol fix

- PASS: `GOFLAGS=-buildvcs=false go test ./internal/adapters/gitea ./internal/adapters/hiveci ./internal/config -count=1`
- PASS: `GOFLAGS=-buildvcs=false go build ./...`
- PASS: `GOFLAGS=-buildvcs=false go test ./...`
- FORMAT BASELINE: `gofmt -l internal cmd` reports 14 pre-existing files; none is changed by this task.
- MUTATION FAIL: reverting the outbound run kind to the old ContextVM binding (`25910`) fails the wire-kind assertion, subscriber round trip, and release lineage reference subtests.
- MUTATION FAIL: removing the existing-run replay guard fails with `runs=1 dispatches=2`.
- MUTATION FAIL: suppressing the self-trust warning fails `TestSelfDispatchWarnsWhenServicePubkeyIsNotTrusted`.
- MUTATION FAIL: disabling the unsupported-build-argument guard fails `TestSelfDispatchRejectsUnsupportedBuildArgsBeforeSideEffects`.
