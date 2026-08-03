# Verification report: bahia-b2lc7

## Implemented

- Structured readiness evidence for the hydrated PostgreSQL relay-policy projection.
- Pre/post same-or-newer rollout comparison with automatic Compose rollback.
- Digest-only production Compose inputs and OCI source revision/version labels.
- Versioned public-provenance export in signed backup runs and cached-only restore.

## Acceptance mapping

- AC1: deterministic Python rejection tests cover absence, older events, and same-timestamp hash mismatch.
- AC2: Compose mutation tests require `repo@sha256` references; Dockerfiles carry OCI metadata.
- AC3: service round-trip test preserves event ID/hash/author/timestamps and clears relay confirmation.
- AC4: same cached projection is accepted and startup hydration continues to use the PostgreSQL LKG.

## Verification

- `go test ./internal/repository ./internal/service ./internal/app ./internal/api/router` — pass.
- `python3 -m unittest discover -s test/scripts -p 'test_*.py'` — 10 tests pass.
- `go build ./...` — pass.
- Full `go test ./...` was also exercised during implementation; the only failure was the pre-existing SoulFactory issue tracked as `bahia-csxyx`.

## Security

No credentials or private keys are included in readiness details or backup envelopes. Restore mutation remains behind the existing signed, approved ContextVM backup-restore path.
