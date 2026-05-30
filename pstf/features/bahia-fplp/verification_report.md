# Verification report: bahia-fplp

## Evidence

- Pre-work gate searched `web/src/lib/api` and `web/src/routes`; `web/src/lib/api/client.js` has synchronous consumers for adoption and direct-runtime routes, so REST receipt changes were kept additive/compatible.
- Added canonical receipt DTO in `internal/api/dto/command_receipt.go`.
- Added idempotency-key receipt fields and no-relay/partial-failure handling in service, LLM, and ML command publishers.
- Added repository lookup support for `(kind, pubkey, d-tag)` and reactor duplicate suppression before command execution.
- Added default 30 second timeout plus deterministic idempotency key generation for signer-first operator client requests.
- Updated MCP deployment/rollback to publish canonical service command events when the publisher is configured.
- Updated `docs/control-planes.md` and `docs/api.md`.

## Tests run

```bash
GOCACHE=/tmp/bahia-gocache go test ./internal/controlplane ./internal/api/handlers ./internal/mcp
```

Result: passed.
