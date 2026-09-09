# BAHIA_GITEA_INITIATOR_SOURCE_PROVIDER_AUTH verification

## Implemented

- Explicit fail-closed GitHub/private-Gitea source configuration.
- Provider-specific migration auth with credential-free HTTPS clone URLs.
- Strict post-create and replay-time mirror source validation.
- Scrubbed and bounded Gitea error response diagnostics.

## Verification

### Load-bearing mutation checks

Each mutation was applied to a temporary copy, its targeted test was run with `-count=1`, and the production file was restored immediately afterward.

- Old `service=github`/`auth_token` payload on the private-Gitea path: `TestConformancePrivateGiteaMirrorBuildInitiation` failed with the fake Gitea HTTP 422.
- GitHub mapped away from token auth: `TestConformancePrivateMirrorBuildInitiation` failed because git migration required username/password.
- Missing/unsupported providers silently defaulted to GitHub: `TestConformanceSourceProviderConfigFailsClosed` failed for both cases.
- Exact-replay store result ignored: `TestConformancePrivateMirrorBuildInitiation` failed with an unexpected mirror sync on replay.
- Empty `original_url` accepted: `TestConformanceUnexpectedExistingMirrorFailsClosed` failed by advancing to mirror sync.
- Case-sensitive source paths lowercased: `TestValidateMirrorPreservesSourcePathCase` failed by accepting the mismatch.
- Gitea error response excerpt removed: `TestMigrateMirror422IncludesScrubbedBoundedResponse` failed because the 422 reason was absent.
- Error response scrubbing removed: the same test failed because source and fleet credentials survived in the returned error.
- Error excerpt length bound removed: the same test failed with a 6,160-rune error.
- Clone URL validation removed: `TestMigrateMirrorRejectsUnsafeCloneURL` failed for HTTP, userinfo, query, and fragment inputs.
- Config provider validation removed: `TestValidateHiveCIInitiatorSourceProvider` failed for missing and unsupported providers.

After restoration, all targeted tests passed.

### Final gates

- PASS: `GOFLAGS=-buildvcs=false go build ./...`
- PASS: `GOFLAGS=-buildvcs=false go test ./...`
- PASS: every changed Go file is clean under `gofmt -l`.
- BASELINE: `gofmt -l internal cmd` reports the same 15 unrelated files present on master; none is changed by this task.
