# Verification Report — SOUL_FACTORY_AGENT_CUSTOMIZATION

## Scope

Verification for Beads issue `bahia-pnlu`, Epic 1 Task 1.1, on 2026-05-15.

This slice covers only the Go domain foundation for v2 SoulFactory draft customization specs. It does not claim completion of event codec merge semantics, web client builders, provider validation, or runtime customization methods.

## Evidence

Commands run from `/Users/bizarro/Documents/Projects/bahia`:

```text
go test ./internal/domain ./internal/soulfactory
ok  	github.com/openagentsinc/bahia/internal/domain	0.262s
ok  	github.com/openagentsinc/bahia/internal/soulfactory	0.219s

go test ./...
ok  	github.com/openagentsinc/bahia/internal/domain	(cached)
ok  	github.com/openagentsinc/bahia/internal/soulfactory	(cached)
... all Bahia Go packages passed or had no test files
```

## Acceptance status

| AC ID | Status | Evidence |
| --- | --- | --- |
| SFAC-AC-001 | Verified | `SoulDraftContent.Schema`, schema constants, `SchemaVersion`, `IsV2`, and schema auto-marking for v2 customization content; covered by `TestSoulDraftContentV2CustomizationSpecsRoundTrip`, `TestSoulDraftContentAutoMarksV2WhenCustomizationSpecsPresent`, and `TestSoulDraftContentV1MigrationKeepsLegacyFields`. |
| SFAC-AC-002 | Verified | New persona/avatar/voice/memory structs plus v2 identity theme/emoji fields round-trip through JSON in `internal/domain/soul_test.go`. |
| SFAC-AC-003 | Verified | Legacy no-schema draft content remains parseable and `MigrateToLatest` preserves v1 fields while seeding v2 avatar/voice fields; runtime param assembly defensively invokes migration. |
| SFAC-AC-004 | Verified | `TestHashDraftContentIncludesCustomizationSpecs` proves stable hash computation and hash change when a nested customization field changes. |
