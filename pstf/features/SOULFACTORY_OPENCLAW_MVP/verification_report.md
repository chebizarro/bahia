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

## Item 3 verification — Workspace/Nostr client/web/docs alignment

Scope implemented for Bead `bahia-euu6`:

- Workspace OpenClaw config generation now requires operator-supplied relays, trusted controller pubkeys, model, Nostr private-key secret reference, and agent-memory MCP URL reference before writing `config/openclaw.json`.
- Generated workspace config no longer embeds Sharegap relay placeholders, static controller placeholders, inline/private-key placeholders, a hardcoded Claude model, or fake agent-memory MCP URLs.
- Invalid or short soul/controller pubkeys and invalid gateway ports fail explicitly in the workspace manager before slicing or writing production workspace config.
- App-level `soul_factory.workspace_*` config fields thread optional workspace repository generation inputs into `FullProvisionerConfig.Workspace`; workspace secret/config references are required when `workspace_gitea_url` is configured.
- `NostrClient.PublishProvisionRequest` now publishes browser-compatible kind `5950` requests with `draft`, `draft-event`, draft `e` marker, `spec-hash`, runtime/capability tags, `method=soulfactory.provision`, `request-kind=5950`, and `schema=soulfactory-provisioning/v1` content.
- The browser Soul creation path was reviewed and already uses the Nostr-first publisher; no REST provisioning or lifecycle calls were added.
- Docs now describe the signed `31952`/`5950` → `6950` → `38384`/`38386` → `31951` → `7950` flow and state that REST provisioning/lifecycle routes are non-goals.

Focused verification on 2026-06-06:

```text
go test ./internal/soulfactory ./internal/config ./internal/app
ok  	github.com/openagentsinc/bahia/internal/soulfactory	0.342s
ok  	github.com/openagentsinc/bahia/internal/config	(cached)
ok  	github.com/openagentsinc/bahia/internal/app	0.378s
```

REST-route source check:

```text
grep -R "soul_factory\|soulfactory" internal/api
# exit code 1, no matches
```

Full Go test gate after Item 3 changes:

```text
go test ./...
ok  	github.com/openagentsinc/bahia/cmd/cli	0.236s
...
ok  	github.com/openagentsinc/bahia/test/integration	0.296s
```

Item 3 acceptance mapping:

- SFOM-AC-010: covered by `TestWorkspaceOpenClawConfigUsesConfiguredValues`, `TestWorkspaceOpenClawConfigRejectsInvalidOrMissingValues`, and `TestLoadSoulFactoryConfigFromYAMLAndEnv`.
- SFOM-AC-011: covered by `TestNostrClientPublishProvisionRequestMatchesBrowserEventShape`.
- SFOM-AC-012: covered by docs updates and source inspection confirming no SoulFactory REST provisioning/lifecycle route was added under `internal/api`.

Review follow-up applied:

- Optional provisioning request fields are trimmed before tags are appended, preventing whitespace-only empty `draft-event`, `spec-hash`, runtime, or capability tags.
- Workspace manager validates gateway port range directly, so direct construction fails closed even outside app config validation.

## Item 4 verification — closeout, production-readiness scan, Beads, and push prep

Scope implemented for Bead `bahia-2r4y`:

- Verified Items 1–3 evidence against `docs/plans/soulfactory-openclaw-mvp-orchestration.md`.
- Confirmed no SoulFactory REST provisioning/lifecycle handlers exist under `internal/api`.
- Removed closeout hardcoded production-path fallbacks in `internal/soulfactory`: the reactor no longer carries global authorized provisioner pubkeys or a global SoulFactory signing pubkey fallback; authorization, replay detection, and success-result coordinates now require explicit config.
- Added deterministic regression coverage for explicit `authorized_pubkeys`, explicit SoulFactory factory pubkey preflight before provisioning side effects, and success-result factory pubkey requirements.
- Re-scanned `internal/soulfactory` for the removed hardcoded fallback pubkeys; only test forbidden-value assertions remain.
- Found the dormant legacy `Provisioner` placeholder path remains fail-closed and is not wired into app startup; tracked removal/productionization as Bead `bahia-j28b` rather than treating it as completed work.

Focused verification on 2026-06-06 after closeout hardening:

```text
go test -count=1 ./internal/soulfactory ./internal/config ./internal/app
ok  	github.com/openagentsinc/bahia/internal/soulfactory	0.394s
ok  	github.com/openagentsinc/bahia/internal/config	0.172s
ok  	github.com/openagentsinc/bahia/internal/app	2.007s
```

REST-route source check on 2026-06-06:

```text
grep -R "soul_factory\|soulfactory" internal/api
# exit code 1, no matches
```

Hardcoded fallback source check on 2026-06-06:

```text
Search under internal/soulfactory for AuthorizedProvisioners, SoulFactoryPubkey assignment, and the removed Biz/Stew hardcoded pubkeys.
Result: no production-code matches; only workspace_test.go forbidden-value assertions matched.
```

Full Go quality gate on 2026-06-06 after closeout hardening:

```text
go test -count=1 ./...
ok  	github.com/openagentsinc/bahia/cmd/cli	0.473s
...
ok  	github.com/openagentsinc/bahia/internal/soulfactory	1.869s
...
ok  	github.com/openagentsinc/bahia/test/integration	1.828s
```

Review evidence:

- Oracle review identified a preflight gap where missing `SoulFactoryPubkey` could be detected after provisioning side effects; this was fixed before final verification by failing closed before run creation/provisioning and by requiring configured factory authors for replay checks.

Item 4 Beads closeout:

- Worked/closing: `bahia-2r4y`.
- Remaining SoulFactory follow-up tracked: `bahia-j28b` for removing or productionizing dormant `internal/soulfactory/provisioner.go` placeholder logic that is currently fail-closed and not wired into production startup.

## Bead bahia-j28b verification — remove dormant legacy Provisioner placeholder

Scope implemented for Bead `bahia-j28b`:

- Deleted `internal/soulfactory/provisioner.go`, removing the dormant `Provisioner`, `NewProvisioner`, `soulFactoryReady` gate, skipped-stage placeholder provisioning flow, and legacy lifecycle methods that could acknowledge partial behavior if made reachable.
- Preserved the reactor's default `unavailableProvisioningEngine` fail-close behavior when no production provisioning engine is explicitly installed.
- Added deterministic hardening coverage proving a configured reactor without an installed production engine publishes no final `31951` soul event and emits only a terminal error `7950` result.
- Confirmed app startup remains on `NewFullProvisioner` plus OpenClaw runtime adapter through the existing focused startup regression test.

Focused verification on 2026-06-07:

```text
go test -count=1 ./internal/soulfactory
ok  	github.com/openagentsinc/bahia/internal/soulfactory	0.375s

go test -count=1 ./internal/app -run TestNewRegistersSoulFactoryWhenEnabled
ok  	github.com/openagentsinc/bahia/internal/app	0.326s

if grep -R "NewProvisioner\|soulFactoryReady" internal/soulfactory --include='*.go'; then exit 1; fi
# exit code 0, no matches
```

Bead `bahia-j28b` acceptance mapping:

- SFOM-AC-013: covered by `TestDefaultReactorProvisioningFailsClosed`, `TestDefaultReactorProvisioningPublishesOnlyErrorWithoutEngine`, and the legacy-symbol absence scan (`SFOM-T-022` through `SFOM-T-024`).

PSTF defect closeout:

- `SFOM-D-001` moved to `resolved` with verification notes for Bead `bahia-j28b`.

## M4 provisioning proof acceptance verification — 2026-07-07

Scope for Bead `bahia-o9xtp`:

- Verified the code-side SF-AC1 mint mechanism against `fleet-planning/docs/execution/pstf-acceptance.md` and the Soul Factory MVP plan.
- Code path: `internal/app/soulfactory.go` wires the enabled app runtime to a real Signet client with `AllowMock: false`, `NewFullProvisioner`, and the OpenClaw runtime adapter.
- Signet identity: `internal/adapters/signet/client.go` provisions agents via ContextVM kind `25910` (`cascadia.CAS_INTENT`) method `agent/provision` and records only `{pubkey, npub, bunker_uri}` on `AgentSoul`; no production `nsec` is stored on the soul or generated workspace config.
- Eight-step provisioner: `internal/soulfactory/provisioner_full.go` runs generate → Signet → avatar → kind:0 profile → Qdrant → agent-memory → workspace → deploy/finalize. Avatar, Qdrant, agent-memory, Blossom, NIP-05, and workspace paths are optional and record skipped/failed non-fatal steps where configured as enrichment, while Signet/profile/runtime publish failures fail the provisioning path.
- Runtime bind: when the resolved draft/runtime has a target, step 8 calls `executeRuntimeProvision`, which uses the runtime adapter to discover trusted `30317`, publish signed `38384`, require correlated `38386`, and apply runtime fields before final read-model publication.
- Bahia registration: step 8 calls `BahiaIntegration.RegisterSoulAsService`, optional initial deployment intent from real runtime artifact metadata, and status sync before final `31951` publication.
- Active soul publication: final authoritative `31951` is published only after immediately-known runtime and Bahia fields are populated; runtime failure coverage proves no final `31951`/success `7950` is emitted on bind failure.
- Capability advertisement: `cmd/openclaw-soulfactory-sidecar` publishes `30317` capabilities with `schema=soulfactory-runtime-capability/v1`, `control_schema=soulfactory-runtime-control/v1`, runtime, methods, controller pubkeys, and relay hints; the runbook marks the live `max` sidecar + `relay.sharegap.net` verification as infra/operator work.
- Canonical kind guardrails: `git.sharegap.net/cascadia/cascadia-go v0.2.1` is in `go.mod`; `internal/adapters/signet` uses `cascadia.CAS_INTENT`; `internal/kinds.CASAudit` uses `cascadia.CAS_AUDIT`; SoulFactory runtime capability constants now use `cascadia.CAS_AGENT_CAPABILITY` instead of raw `30317`. Added `TestCascadiaGeneratedKindAliasesStayCanonical` to catch `CAS_AUDIT`→`CAS_INTENT` alias regressions and `30317` raw/incorrect drift.
- Memory step-6 disposition: the Bahia typed client calls `agent_register` + `memory_add`, but the fp-31 Go `agent-memory-mcp` service exposes `memory_task_start`, `memory_event`, `memory_task_complete`, `memory_search`, `memory_reflect`, and `memory_artifact` with D4 fail-closed gating. The seed endpoint is therefore not wire-compatible yet; app startup intentionally remains skip-clean with empty `agentmemory.Config{}`. Follow-up tracked as `bahia-lsxe8`.
- Live e2e disposition: not faked in code verification. `docs/soul-factory-sidecar-runbook.md` remains the deploy-verification path for sidecar-on-`max`, real container start, and `31951` + `30317` on `relay.sharegap.net`.

Focused verification commands for this pass:

```text
go test ./internal/kinds ./internal/domain ./internal/adapters/signet ./internal/soulfactory ./internal/app
GOPRIVATE=git.sharegap.net/* go build ./...
GOPRIVATE=git.sharegap.net/* go test ./...
```

## Result

Verified for Items 1, 2, 3, 4, Bead bahia-j28b, and the M4 code-side provisioning proof pass (`bahia-o9xtp`). Live sidecar-on-`max` relay verification remains infra/operator verification.
