# Verification Report: Bahia Bead Hardening 2026-08-29

## Scope

- `bahia-cggor`: removed the dead legacy service rollback entry points and pre-approved intent creation; protected intent creation now rejects caller-supplied approval state.
- `bahia-udb2f`: signed `service/update` requires `expected_updated_at` and checks it under a repository transaction row lock before publication or mutation.
- `bahia-t2gn5`: runtime action errors cross a fail-closed secret scrub boundary before structured logging, including JSON-escaped values.
- `bahia-sbw41`: Soul Factory loads and wires `agent_memory_task_id_file` into the durable agent-memory task-ID store configuration.

## Verification

- `GOFLAGS=-mod=mod go test ./internal/service ./internal/controlplane ./internal/repository ./internal/config ./internal/app ./internal/adapters/nostr`
- `GOFLAGS=-mod=mod go build ./...`
- `GOFLAGS=-mod=mod go vet ./internal/service ./internal/controlplane ./internal/repository ./internal/config ./internal/app ./internal/adapters/nostr`
- `cd web && npm run test:unit -- tests/unit/public-controlplane.test.js`
- `cd web && npm run lint`

All commands passed.
