# HIVECI_PRIVATE_MIRROR_WORKER_CLONE_AUTH verification

## Implemented

- Dedicated read-only mirror credential configuration, audited resolution, and service ownership validation.
- Exact two-key Loom secret handoff using the existing selected-worker NIP-44 encryption path.
- Credential-free, trusted-origin, selected-repository clone URL enforcement.
- Opaque credential reference in queued evidence and defensive scrubbing of both resolved credentials.
- Existing replay, capability, no-payment, kind-5401 publisher, and worker-signed kind-5402 behavior preserved.

## Load-bearing mutation verification

Each production behavior below was temporarily reverted, the named test failed with `-count=1`, the production file was restored byte-for-byte, and the same test passed:

- Missing credential allowed to continue: `TestConformanceMissingMirrorReadCredentialFailsClosedBeforePublish` failed because initiation returned nil error.
- Password key removed: `TestConformancePrivateMirrorBuildInitiation` failed with only `HIVE_CI_GIT_USERNAME` present.
- Encryption redirected to the first uncapable worker: `TestSubmitJob_HiveCIShapeSelectsCapableWorkerEncryptsSecretsAndOmitsPayment` failed with `invalid hmac` for the selected worker.
- Mirror clone URL validation bypassed: `TestConformanceUntrustedMirrorCloneURLFailsClosed` failed for userinfo, foreign origin, and wrong repository path.
- Plaintext secret tags injected: the Loom shape test failed with `plaintext secret leaked into kind-5100 event`.
- Plaintext secret log field injected: the Loom shape test failed with `plaintext secret leaked into Loom logs`.
- Loom error scrubbing removed: `TestConformanceLoomErrorsNeverCarryMirrorReadCredential` failed and displayed the injected credential.
- Replay record ignored: `TestConformancePrivateMirrorBuildInitiation` failed with two Loom submissions.
- Opaque evidence reference replaced with username: the conformance test failed on the evidence value mismatch.
- Capability feature removed: the conformance test failed with an empty required-feature set.
- Payment token injected: the conformance test failed because the internal job carried payment.

Raw fail/pass outputs were retained outside the repository in `/tmp/bahia-cloneauth-mutation-evidence/` for the session handoff.

## Final gates

- PASS: `GOFLAGS=-buildvcs=false go build ./...`
- PASS: `GOFLAGS=-buildvcs=false go test ./...`
- PASS: every changed Go file is clean under `gofmt -l internal cmd`.
- BASELINE: `gofmt -l internal cmd` reports 14 unrelated pre-existing files; none is changed by this task.
