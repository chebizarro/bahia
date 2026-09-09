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
- Every accepted self-issued 5401 now submits a kind-5100 job through the existing Loom client on the same control-plane relay pool and signer, with `p`, `method=ci/workflow-run`, `e`/`run` correlation, and mirror/ref/workflow parameters.
- Placement requires the configured trusted worker allowlist plus signed kind-10100 workload `ci/workflow-run` and feature `hive_ci_profile`; generic capability JSON is now retained by Bahia's worker projection.
- Fleet-internal CI jobs omit payment because Bahia cannot mint Cashu tokens. Bahia deliberately keeps its service-key 5401 publisher and trusted worker-signed 5402 acceptance model; it does not deliver `HIVE_CI_NSEC`.
- Loom secret tags remain NIP-44 ciphertext addressed to the selected worker and are never projected into plaintext event content, tags, or argv.

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

### 2026-09-08 Hive-CI Loom job dispatch

- MUTATION FAIL/PASS: disabling the initiator's Loom submit call fails `TestConformancePrivateMirrorBuildInitiation` with `Loom submissions = 0, want 1`; restoring it passes.
- MUTATION FAIL/PASS: disabling the initiation-store replay guard fails the same test with `replay duplicated Loom dispatch, got 2 submissions`; restoring it passes.
- MUTATION FAIL/PASS: removing the kind-5100 `e` correlation fails `TestSubmitJob_HiveCIShapeSelectsCapableWorkerEncryptsSecretsAndOmitsPayment` with the emitted tag list missing `e`; restoring it passes.
- MUTATION FAIL/PASS: removing `p` and `method` projection fails the same job-shape test with both tags absent; restoring them passes.
- MUTATION FAIL/PASS: bypassing the `hive_ci_profile` selector fails `TestSelectWorker_FailClosedOnCriteria` and selects the deliberately uncapable first worker in the full job-shape test; restoring it passes.
- MUTATION FAIL/PASS: forcing a payment tag fails the job-shape test with `fleet-internal Hive-CI request carried a payment tag`; restoring absent payment passes.
- MUTATION FAIL/PASS: replacing NIP-44 ciphertext with plaintext fails the job-shape test with `plaintext secret leaked into kind-5100 event`; restoring encryption passes.
- MUTATION FAIL/PASS: dropping generic capabilities during kind-10100 ingestion fails `TestProcessorWorkerAdvertisementParsesGenericCapabilities`; restoring capability persistence passes.
- PASS: `GOFLAGS=-buildvcs=false go build ./...`
- PASS: `GOFLAGS=-buildvcs=false go test ./...`
- FORMAT BASELINE: `gofmt -l internal cmd` reports the same 14 pre-existing files documented above; `git diff --name-only -z -- '*.go' | xargs -0 gofmt -l` reports no changed Go files.
