# Verification Report — bahia-38qqz

## Specification evidence

- Implemented NIP-CAS-0007 Communikeys write access using the section kind-`30000` profile-list `p` set.
- Badges are not read or written by the permission path.
- Historical list reads use a scoped `kind + author + d + limit=1` filter and complete on relay EOSE through the SoulFactory relay bus.
- Replacement publication uses the Signet/controller signer and requires relay `OK`.

## Implementation evidence

- Added normalized `soul_factory.communikeys_communities` configuration and app wiring.
- Added controller-owned profile-list lookup, latest-wins validation, tag/content preservation, idempotent member detection, signed replacement publication, and AUTH-race retry.
- Added fail-closed provisioning integration immediately after Signet identity creation.
- Recorded assigned `30000:<community-pubkey>:<section>` coordinates in the Signet step output.
- Updated SoulFactory operator and user documentation.

## Commands run

- `go test ./internal/soulfactory -run 'TestCommunikeys|TestFullProvisioner.*Communikeys'` — passed.
- `go test ./internal/config -run 'TestLoadSoulFactoryConfigFromYAMLAndEnv|TestLoadRejectsInvalidSoulFactoryConfig'` — passed.
- `go test ./internal/app` — passed.
- `go build ./...` — passed.
- `go test ./...` — all packages completed, with one known unrelated failure in `TestOpenClawCommandDriverDefaultsToWrapperSupportedMethods`; the stale expectation is already tracked by open bead `bahia-rp4l7`.

## Result

Focused deterministic tests cover scoped profile-list retrieval, deterministic latest-wins selection, controller-signed preservation and append behavior, idempotency, AUTH-race retry, missing-list failure, ownership failure, relay rejection, provisioning fail-closed behavior, recorded assignments, and configuration normalization. The feature build and focused gates pass; the only repository-wide test failure is outside this bead and already tracked by `bahia-rp4l7`.
