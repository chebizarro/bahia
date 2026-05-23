# Verification Report — bahia-w6xf

## Evidence

- Added `internal/mcp/backup_tools.go` with backup MCP tool definitions for registry apply, operational requests, restore approval/rejection, retention, list tools, and inspect tools for repositories, policies, recipes, runs, restores, retention runs, and definitions.
- Added `BackupCommandPublisher` and `BackupReadModelRepository` MCP dependencies, wired through `ServerDeps` and `NewServerWithOptions`.
- Registered backup tool definitions in `GetTools()` and dispatched backup tool names through `CallTool()`.
- Mutating tools publish through the configured backup command publisher and return correlation metadata; they do not invoke reactor internals, executors, sleep-based completion, or local polling.
- Query tools read durable backup read models and include verification evidence for backup run inspection when available.
- Added deterministic MCP tests for registration, signer-first command publishing, missing publisher/read-model failure, prefixed alias dispatch, and read-model list/inspect behavior.

## Tests Run

- `go test ./internal/mcp` — passed.
- `go test ./...` — failed outside this slice:
  - `internal/api/handlers` continuity tests call `NewContinuityHandler` with a stale one-argument signature. Tracked as Bead `bahia-a6f9`.
  - `internal/config` `TestDNSValidationEnabled/unsupported_backend_type` observes an earlier coredns `etcd_endpoints` validation error. Tracked as Bead `bahia-4r0g`.

## Nostr Semantics Review

- Mutating MCP tools preserve event-driven semantics by requiring a backup command publisher for Nostr request emission.
- Mutating MCP tools require `idempotency_key` so emitted backup command events can carry the addressable `d` tag required by the backup reactor.
- No polling loops, sleeps, timeout-based completion logic, fake execution paths, or direct control-plane handler invocation were introduced.
- Missing command/read-model dependencies fail closed with MCP error results.

## Remaining Tracked Work

- `bahia-512d` tracks production app wiring for a concrete backup command publisher and backup read-model dependency. This is outside the requested `internal/mcp/`-only change scope.
