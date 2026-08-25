# Verification Report — SOUL_FACTORY_AGENT_CUSTOMIZATION

## Scope

Verification for Beads issues `bahia-pnlu` (Epic 1 Task 1.1), `bahia-z4gu` (Epic 1 Task 1.2), `bahia-2lpj` (Epic 4 Task 4.1), and `bahia-48jp` (Epic 5 Task 5.1), on 2026-05-15.

This report currently covers the Go domain foundation for v2 SoulFactory draft customization specs, event codec parsing/building/validation/merge behavior, Bahia memory configuration mapping, and Bahia personality mapping. The Task 5.1 slice maps `SoulPersonaSpec` to OpenClaw prompt sections, assembles a composite system prompt, validates persona fields, and defines the `soulfactory.persona.update` kind:38384 params contract. It does not claim completion of LLM prompt refinement, UI builders, reindex orchestration, or runtime bridge execution handlers.

## Evidence

Commands run from `/Users/bizarro/Documents/Projects/bahia`:

```text
go test ./internal/soulfactory
ok  	github.com/openagentsinc/bahia/internal/soulfactory	0.214s

go test ./...
ok  	github.com/openagentsinc/bahia/cmd/cli	(cached)
...
ok  	github.com/openagentsinc/bahia/internal/soulfactory	(cached)
ok  	github.com/openagentsinc/bahia/test/integration	(cached)
```

PSTF JSON validation:

```text
python3 -m json.tool pstf/features/SOUL_FACTORY_AGENT_CUSTOMIZATION/acceptance_criteria.json >/tmp/sfac_ac.json
python3 -m json.tool pstf/features/SOUL_FACTORY_AGENT_CUSTOMIZATION/test_matrix.json >/tmp/sfac_tm.json
# both passed
```

Task 1.2 focused evidence:

```text
go test ./internal/soulfactory ./internal/domain
ok  	github.com/openagentsinc/bahia/internal/soulfactory	0.224s
ok  	github.com/openagentsinc/bahia/internal/domain	(cached)
```

Earlier Task 1.1 evidence retained:

```text
go test ./internal/domain ./internal/soulfactory
ok  	github.com/openagentsinc/bahia/internal/domain	0.262s
ok  	github.com/openagentsinc/bahia/internal/soulfactory	0.219s
```

## Acceptance status

| AC ID | Status | Evidence |
| --- | --- | --- |
| SFAC-AC-001 | Verified | `SoulDraftContent.Schema`, schema constants, `SchemaVersion`, `IsV2`, and schema auto-marking for v2 customization content. |
| SFAC-AC-002 | Verified | Persona/avatar/voice/memory structs plus v2 identity theme/emoji fields round-trip through JSON in `internal/domain/soul_test.go`. |
| SFAC-AC-003 | Verified | Legacy no-schema draft content remains parseable and `MigrateToLatest` preserves v1 fields while seeding v2 avatar/voice fields. |
| SFAC-AC-004 | Verified | `TestHashDraftContentIncludesCustomizationSpecs` proves stable hash computation and hash change when a nested customization field changes. |
| SFAC-AC-005 | Verified | `TestMapSoulMemorySpecToOpenClawMemorySearch` and `TestMapSoulMemorySpecSupportsLocalEphemeralStrategy` cover provider/strategy normalization and OpenClaw memorySearch mapping. |
| SFAC-AC-006 | Verified | `TestValidateSoulMemorySpecRejectsInvalidSearchConfig` covers unsupported provider/strategy, top_k, score threshold, retention, and rerank_model validation. |
| SFAC-AC-007 | Verified | `TestBuildMemoryConfigureRuntimeParamsSerializesInto38384Envelope` covers runtime params serialization in a kind:38384 envelope, with portable `memory_config` separated from OpenClaw-native replacement config. |
| SFAC-AC-008 | Verified | `TestPersonalityServiceMapsPersonaToOpenClawPromptSections` covers normalization and mapping of traits/style/tone/constraints/system sections into role/guidelines/red_lines. |
| SFAC-AC-009 | Verified | `TestPersonalityServiceMapsPersonaToOpenClawPromptSections` and `TestAssembleOpenClawSystemPromptOmitsEmptySections` cover deterministic composite prompt assembly and empty-section omission. |
| SFAC-AC-010 | Verified | `TestPersonalityValidationRejectsInvalidFields` covers duplicate traits, unsupported section names, excessive constraint counts, and control-character rejection. |
| SFAC-AC-011 | Verified | `TestBuildPersonaRuntimeControlParamsDefines38384Contract` covers `soulfactory.persona.update`, `soulfactory-persona/v1`, OpenClaw prompt payload fields, OpenClaw-native `systemPromptOverride` patching, and embedding params in a parsed kind:38384 request. `docs/soulfactory-runtime-control.md` documents the same params contract. |
| SFAC-AC-012 | Verified | `openclaw/extensions/nostr/src/soulfactory-execution.test.ts` covers `soulfactory.config.reload` applying `tts`, `memorySearch`, `systemPromptOverride`, and `identity` changes with `session_restarted=false`, clearing stale optional config on resolved-spec sync, and asserts no `deleteSession` or additional `spawnSessionDirect` call after initial provisioning. |
| SFAC-AC-013 | Verified | `TestEventCodecDraftV2CustomizationSpecsRoundTrip` covers building/parsing kind:31952 v2 draft content with persona, avatar, voice, and memory specs. |
| SFAC-AC-014 | Verified | `TestEventCodecRejectsInvalidDraftSpecReferences` covers invalid avatar provider, voice model, memory embedding model, and memory rerank model validation failures. |
| SFAC-AC-015 | Verified | `TestMergeSoulDraftContentPartialUpdateDeepMerges` covers recursive object merge, scalar replacement, null deletion, and false boolean patching for hot-reload draft updates. |
| SFAC-AC-016 | Verified | `TestEventCodecMaintainsV1DraftBackwardCompatibility` covers no-schema v1 draft parsing and legacy field preservation through the event codec. |

## Epic 8.2 evidence — 2026-05-15

OpenClaw command run from `/Users/bizarro/Documents/Dev/openclaw`:

```text
pnpm test extensions/nostr/src/soulfactory-execution.test.ts
[test] passed 1 Vitest shard in 3.07s
Test Files  1 passed (1)
Tests       8 passed (8)
```

Notes:

- The OpenClaw bridge now implements `soulfactory.config.reload` for supported managed-agent patches and resolved-spec syncs.
- Runtime patching writes `agents.list` with `afterWrite: { mode: "auto" }`; OpenClaw's reload planner treats `agents.list` as a hot path rather than a gateway/session restart path.
- Verification is unit-level for the OpenClaw bridge behavior; a full gateway reload integration test remains outside this slice.

## Phase 3 runtime configuration evidence — 2026-08-24

Beads issue: `bahia-ee84m`.

The OpenClaw provisioner now deep-merges validated per-agent memory and voice draft settings after the fleet snapshot and before wrapper-owned runtime keys. Memory is emitted at `agents.defaults.memorySearch`, voice/TTS at `messages.tts`, and portable retention/reranker intent is retained in `bahia-runtime.json` without inventing unsupported OpenClaw keys. The creation wizard also supports an optional `runtime.model` override while leaving a blank value to inherit fleet/runtime defaults.

Focused Go coverage:

- `TestFleetConfigMergePrecedenceAndDefaults` verifies fleet → agent → wrapper precedence, preservation of unrelated fleet fields, memory/TTS placement, and portable-only retention/reranker metadata.
- `TestProvisionRejectsInvalidDraftMemoryBeforeRuntimeMutation` verifies invalid customization input fails before runtime commands or orchestration begin.

Honesty and durability coverage:

- Browser-local avatar blob URLs and the unsupported file-upload control were removed; operators may enter an existing durable Blossom/HTTPS reference.
- Pre-deployment avatar generation, voice playback, and memory reindex actions are gated with explicit availability hints.
- Tool grants and approval policy are labeled as signed draft intent rather than claimed OpenClaw enforcement across create, detail, and edit views.
- Fleet configuration state resets when the authenticated operator changes.

Commands run from `/Users/bizarro/Documents/Projects/bahia`:

```text
go build ./...
# passed

go test ./internal/soulfactory/...
ok  github.com/openagentsinc/bahia/internal/soulfactory
ok  github.com/openagentsinc/bahia/internal/soulfactory/openclawcontrol
ok  github.com/openagentsinc/bahia/internal/soulfactory/saga

cd web && npm test
Test Files  90 passed (90)
Tests       687 passed (687)

cd web && npm run lint
svelte-check found 0 errors and 0 warnings
```
