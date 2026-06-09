# bahia-zu2p.9.3 — Compose Phase-2 Fragment Optimization

## Acceptance Criteria

1. Design references final ComposeRenderer/ComposeDesiredStateApplier shape
2. Fragment layout and merge order specified under .bahia/fragments/
3. Eligibility rejects unsafe cases:
   - Dependency (depends_on) changes
   - Network declaration changes
   - Volume declaration changes
   - Project name changes
   - New service additions
   - Service removals
   - Multiple services changed simultaneously
   - No baseline exists (first render)
4. Eligible changes (image, env, command, ports, labels, healthcheck, restart, pull policy) use service-scoped fragment apply
5. Full project is always updated alongside fragment apply (no drift)
6. Operator visibility: fragment metadata in render-state.json, warnings in apply result
7. Secret redaction: fragments never contain plaintext secrets
8. Implementation covered by deterministic tests
9. Full-project path preserved as fallback for all ambiguous changes

## Test Matrix

| Scenario | Test File | Status |
|----------|-----------|--------|
| Fragment eligibility - no baseline | compose_fragment_eligibility_test.go | ✅ |
| Fragment eligibility - image change | compose_fragment_eligibility_test.go | ✅ |
| Fragment eligibility - dependency change | compose_fragment_eligibility_test.go | ✅ |
| Fragment eligibility - network change | compose_fragment_eligibility_test.go | ✅ |
| Fragment eligibility - volume change | compose_fragment_eligibility_test.go | ✅ |
| Fragment layout | compose_fragment_layout_test.go | ✅ |
| Fragment apply integration | compose_fragment_apply_test.go | ✅ |
| Safety: full project stays current | compose_fragment_safety_test.go | ✅ |
| Safety: secret redaction | compose_fragment_safety_test.go | ✅ |
| Safety: project name preservation | compose_fragment_safety_test.go | ✅ |
