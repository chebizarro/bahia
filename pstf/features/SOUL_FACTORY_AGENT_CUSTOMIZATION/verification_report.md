# Verification Report — SOUL_FACTORY_AGENT_CUSTOMIZATION

## Scope

Verification for Beads issues `bahia-pnlu` (Epic 1 Task 1.1), `bahia-2lpj` (Epic 4 Task 4.1), and `bahia-48jp` (Epic 5 Task 5.1), on 2026-05-15.

This report currently covers the Go domain foundation for v2 SoulFactory draft customization specs, Bahia memory configuration mapping, and Bahia personality mapping. The Task 5.1 slice maps `SoulPersonaSpec` to OpenClaw prompt sections, assembles a composite system prompt, validates persona fields, and defines the `soulfactory.persona.update` kind:38384 params contract. It does not claim completion of LLM prompt refinement, UI builders, reindex orchestration, or runtime bridge execution handlers.

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
