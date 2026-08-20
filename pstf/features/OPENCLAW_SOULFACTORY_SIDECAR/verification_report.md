# Verification Report — OPENCLAW_SOULFACTORY_SIDECAR

## Scope

Implemented Bahia-owned OpenClaw SoulFactory sidecar support for capability announcements, signed addressed runtime-control request validation, local control-driver execution, idempotency, and correlated result publication.

## Evidence

Focused verification run:

```text
go test ./internal/soulfactory ./cmd/openclaw-soulfactory-sidecar
ok  	github.com/openagentsinc/bahia/internal/soulfactory	0.212s
?   	github.com/openagentsinc/bahia/cmd/openclaw-soulfactory-sidecar	[no test files]
```

Reconfirmed during `bahia-i0rk.3` final verification on 2026-05-15:

```text
go test ./internal/soulfactory/... ./cmd/openclaw-soulfactory-sidecar
ok  	github.com/openagentsinc/bahia/internal/soulfactory	(cached)
?   	github.com/openagentsinc/bahia/cmd/openclaw-soulfactory-sidecar	[no test files]
```

## Acceptance mapping

- OCSS-AC-001: covered by `TestOpenClawSidecarPublishesCompatibleCapability`.
- OCSS-AC-002: covered by `TestOpenClawSidecarValidatesTrustAddressingAndRequiredParams`, `TestOpenClawSidecarIdempotentReplayDoesNotRepeatSideEffects`, and `TestOpenClawSidecarPersistsIdempotencyAcrossRestart`.
- OCSS-AC-003: covered by `TestOpenClawSidecarExecutesProvisionAndPublishesCorrelatedResult` and `TestOpenClawSidecarExecutesLifecycleSuspend`.
- OCSS-AC-004: covered by provision and lifecycle result publication assertions.
- OCSS-AC-005: sidecar uses scoped subscriptions, EOSE transition, relay OK-enforced publishing, durable replay protection in the CLI path, and contains no polling/sleep completion logic.

## Result

Verified for the owned sidecar vertical slice.

## Live runtime-update extension — 2026-07-20

- Bahia revision `618ab42e` adds `soulfactory.update` execution to the packaged OpenClaw control wrapper, including optimistic previous/new spec-hash checks, replace and merge modes, canonical resolved-spec provenance, deterministic replay, and identity refresh through the OpenClaw CLI.
- Focused Go verification passed for `./internal/soulfactory/...`, `./cmd/openclaw-soulfactory-control`, and `./cmd/openclaw-soulfactory-sidecar`.
- Soul Factory web capability/update verification passed: 73 tests across `souls-store.test.js` and `nostr-client-parsing.test.js`; the production web build also completed.
- The live `openclaw-soulfactory-sidecar` was rebuilt from `618ab42e`, recreated, and reached healthy status.
- Replacement kind `30317` event `b363bdf670979dfe9d4c6282e7b5ef524356398d17007a4f65408f5373968a43` is present on both `relay.sharegap.net` and the Bahia browser relay. Its signature verifies and its method set includes `soulfactory.update` alongside provision, persona update, and revoke.

## Release acceptance gate — 2026-08-20

- Bead `bahia-openclaw-complete-agent-provisioning-20260819.5` adds `.github/workflows/deploy-openclaw-soulfactory-sidecar.yml`; it is dispatch/published-release only and selects an exact Bahia commit before building.
- Repository evidence confirms the sidecar image source is in Bahia: `Dockerfile` builds and packages `cmd/openclaw-soulfactory-sidecar` and `cmd/openclaw-soulfactory-control`. No external source checkout is required.
- The workflow targets `[self-hosted, max, docker]`, overlays only the existing `openclaw-soulfactory-sidecar` Compose service, preserves the host Compose file and sibling services, captures the incumbent image digest, and restores that digest on rollout-gate failure.
- Acceptance gates require Docker health, `/ready` fields `ready`, `capability_published`, and `subscription_eose`, the expected running image ID, and `org.opencontainers.image.revision` equal to the selected release SHA. The sanitized artifact contains only public image provenance and the immutable image digest.
- Static verification: Ruby YAML parse, `bash -n` for every workflow `run` block, and `git diff --check`. `actionlint` was unavailable locally. No workflow was dispatched and no live infrastructure was changed.
