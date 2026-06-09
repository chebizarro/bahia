# Verification Evidence

## Implementation Date
2026-06-08

## Files Changed
- `internal/adapters/runtime/compose_fragment_eligibility.go` — NEW
- `internal/adapters/runtime/compose_fragment_layout.go` — NEW
- `internal/adapters/runtime/compose_desired_state.go` — fragment apply integration
- `internal/adapters/runtime/compose_renderer.go` — ServiceHashes in metadata
- `internal/adapters/runtime/compose_executor.go` — fragment validation/apply methods
- Tests: compose_fragment_eligibility_test.go, compose_fragment_layout_test.go, compose_fragment_apply_test.go, compose_fragment_safety_test.go

## Verification Commands
```bash
go test ./internal/adapters/runtime/... -run "TestFragment" -v -count=1
go vet ./internal/adapters/runtime/...
```

## Design Decisions
- Fragments are strictly an optimization — full-project is always the fallback
- Fragment YAML files are written under .bahia/fragments/ (not the project root)
- Full docker-compose.yml is always updated alongside fragment apply
- Eligibility is conservative: any doubt → full-project
- Secret redaction applies to fragments just as it does to rendered env material
- Project name must match between fragment and full project
