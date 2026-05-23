# Verification Report: bahia-33f5

## Evidence

- Added `internal/domain/failover.go` with unified `ContinuityRecipe`, `RecipeTrigger`, and `RecipeStep` domain types.
- Added validation for required name, service key, failover trigger, non-empty steps, valid recipe kind, and supported step actions.
- Added deterministic unit tests in `internal/domain/failover_test.go` mapped to acceptance criteria.

## Commands Run

```sh
gofmt -w internal/domain/failover.go internal/domain/failover_test.go && go test ./internal/domain
```

Result: passed.
