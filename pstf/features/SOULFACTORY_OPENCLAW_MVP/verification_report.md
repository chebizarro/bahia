# Verification Report — SOULFACTORY_OPENCLAW_MVP

## Scope

Item 1 only: Beads/PSTF skeleton, disabled-by-default app config, app startup construction of SoulFactory reactor/provisioner/OpenClaw runtime adapter when enabled, and deterministic config behavior tests.

## Initial evidence notes

- Beads issue: `bahia-0wc4` created and claimed for this slice.
- Plan source: `docs/plans/soulfactory-openclaw-mvp-orchestration.md`.
- Production signer construction uses the existing Signet adapter and does not enable `AllowMock` for `soul_factory` app startup.
- No REST provisioning or lifecycle route is part of this Item 1 scope.

## Verification runs

Focused verification on 2026-06-06:

```text
go test ./internal/config ./internal/app
ok  	github.com/openagentsinc/bahia/internal/config	0.297s
ok  	github.com/openagentsinc/bahia/internal/app	0.324s
```

Reactor API regression check after adding app installation wiring:

```text
go test ./internal/soulfactory
ok  	github.com/openagentsinc/bahia/internal/soulfactory	0.260s
```

REST-route source check:

```text
grep -R "soul_factory\|soulfactory" internal/api
# exit code 1, no matches
```

Full Go test gate:

```text
go test ./...
ok   github.com/openagentsinc/bahia/cmd/cli 0.465s
...
ok   github.com/openagentsinc/bahia/test/integration 2.077s
```

Post-review hardening verification on 2026-06-06:

```text
go test ./internal/config ./internal/app ./internal/soulfactory
ok  	github.com/openagentsinc/bahia/internal/config	0.282s
ok  	github.com/openagentsinc/bahia/internal/app	0.328s
ok  	github.com/openagentsinc/bahia/internal/soulfactory	(cached)
```

A first post-review full-suite run exposed an unrelated transient timeout in `internal/service` (`TestAssistantOrchestratorSuppressesDuplicateApprovalWhileExecuting`). The focused rerun passed, and the full suite then passed:

```text
go test ./internal/service -run TestAssistantOrchestratorSuppressesDuplicateApprovalWhileExecuting -count=1
ok  	github.com/openagentsinc/bahia/internal/service	0.264s

go test ./...
ok   github.com/openagentsinc/bahia/test/integration (cached)
```

Review follow-up applied:

- Signet clients opened during app startup are closed if later app construction fails.
- Signet startup connection and public-key resolution are bounded by `soul_factory.startup_timeout`.
- `llm_base_url` validation requires an API origin with no path because Bahia appends `/v1/messages`.
- Inferred Signet public keys are validated before reactor/runtime construction.

Beads closeout:

- Worked/closed: `bahia-0wc4`.
- Item 1 remaining tracked work at that time: `bahia-23at` for Item 2, `bahia-euu6` for Item 3, and `bahia-2r4y` for full MVP closeout after Items 2 and 3.

## Acceptance mapping

- SFOM-AC-001: covered by `TestDefaults`.
- SFOM-AC-002: covered by `TestLoadSoulFactoryConfigFromYAMLAndEnv` and `TestLoadRejectsInvalidSoulFactoryConfig`.
- SFOM-AC-003: covered by `TestNewDoesNotRegisterSoulFactoryWhenDisabled`.
- SFOM-AC-004: covered by `TestNewRegistersSoulFactoryWhenEnabled`.
- SFOM-AC-005: covered by `TestNewRejectsInvalidSoulFactoryConfig`.
- SFOM-AC-006: covered by source inspection confirming no SoulFactory REST provisioning/lifecycle route was added under `internal/api`.

## Item 2 verification — reactor publication hardening + runtime/Bahia projection correctness

Scope implemented for Bead `bahia-23at`:

- `6950`, error `7950`, success `7950`, and final `31951` provisioning publications now use a normalized primary/additional relay target set and return publish errors instead of silently continuing.
- Full provisioning treats progress publication failures as provisioning failures; terminal error/result publication failures are logged and reflected on the in-memory run state where applicable.
- Runtime provisioning returns the `38386` result envelope to `ProvisionFull`; the result is applied before Bahia projection, final `31951`, and terminal success `7950` publication.
- Bahia service projection no longer fabricates `agents/<id>:latest` artifacts, empty digests, synthetic builds, or initial deployment intents. Initial deployment intent creation is disabled unless explicitly opted in via `BahiaIntegrationConfig.DeployRuntimeArtifacts` and requires runtime artifact metadata with image repo plus digest.
- Lifecycle resume/redeploy no longer falls back to creating synthetic initial deployables when no desired artifact exists.
- No REST provisioning or lifecycle route was added.

Focused verification on 2026-06-06:

```text
go test ./internal/soulfactory
ok  	github.com/openagentsinc/bahia/internal/soulfactory	0.346s
```

Item 2 acceptance mapping:

- SFOM-AC-007: covered by `TestProvisioningPublicationUsesNormalizedCombinedRelaysAndSurfacesErrors`.
- SFOM-AC-008: covered by `TestDraftBackedRuntimeProvisioningPublishesFinalSoulWithResolvedFields` and `TestRuntimeProvisionFailurePublishesErrorWithoutFinalSoulOrSuccess`.
- SFOM-AC-009: covered by `TestBahiaIntegrationDoesNotCreateSyntheticInitialDeployment`, `TestBahiaIntegrationCreatesInitialDeploymentFromRuntimeArtifactMetadata`, and `TestBahiaIntegrationRuntimeArtifactOptInRequiresDigestMetadata`.

Beads closeout:

- Worked/closed: `bahia-23at`.
- Remaining tracked work: `bahia-euu6` for Item 3 and `bahia-2r4y` for full MVP closeout after Items 2 and 3.

Review follow-up applied:

- Bahia lifecycle resume/redeploy treats missing desired artifacts as a no-op instead of fabricating initial deployables or blocking signer/read-model lifecycle side effects.
- Runtime artifact projection now requires an explicit runtime artifact envelope and validates `sha256:<64 hex>` digest shape before registering Bahia artifacts/intents.
- Added deterministic runtime failure coverage to prove no final `31951` or success `7950` is published when the runtime adapter fails.

## Result

Verified for Items 1 and 2.
