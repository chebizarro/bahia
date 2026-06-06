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
- Remaining tracked work: `bahia-23at` for Item 2, `bahia-euu6` for Item 3, and `bahia-2r4y` for full MVP closeout after Items 2 and 3.

## Acceptance mapping

- SFOM-AC-001: covered by `TestDefaults`.
- SFOM-AC-002: covered by `TestLoadSoulFactoryConfigFromYAMLAndEnv` and `TestLoadRejectsInvalidSoulFactoryConfig`.
- SFOM-AC-003: covered by `TestNewDoesNotRegisterSoulFactoryWhenDisabled`.
- SFOM-AC-004: covered by `TestNewRegistersSoulFactoryWhenEnabled`.
- SFOM-AC-005: covered by `TestNewRejectsInvalidSoulFactoryConfig`.
- SFOM-AC-006: covered by source inspection confirming no SoulFactory REST provisioning/lifecycle route was added under `internal/api`.

## Result

Verified for Item 1.
