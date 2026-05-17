# bahia-gs4j Verification Report

## Observed issue
`internal/api/handlers/secrets.go` hardcoded `CreatedBy` to `"api"`, so secret creation audit attribution did not identify the authenticated caller.

## Intended behavior
Secret creation must derive `CreatedBy` from the authenticated request principal and fail closed when no authenticated subject is available.

## Implementation evidence
- `internal/api/handlers/helpers.go` adds `authenticatedSubject`, which returns a non-empty `auth.Principal.Subject` only when the principal exists and `IsAuthenticated()` is true.
- `internal/api/handlers/secrets.go` uses that subject for `domain.ServiceSecret.CreatedBy` and returns `401 Unauthorized` before encryption or persistence when no authenticated subject is present.
- `internal/api/handlers/secrets_test.go` covers principal attribution, missing-principal fail-closed behavior, and existing encryption fail-closed behavior.

## Verification
Command run:

```sh
GOCACHE=/tmp/bahia-go-build go test ./internal/api/handlers
```

Result: passed.
