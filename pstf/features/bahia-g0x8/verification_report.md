# Verification Report: bahia-g0x8

## Scope

Final REST deprecation work item for LLM route/release creation, adoption scan/import, and direct runtime deploy/restart/stop mutation endpoints.

## Changes Verified

- Removed route registrations from `internal/api/router/router.go` for:
  - `POST /llm/routes`
  - `POST /llm/routes/{routeId}/releases`
  - `POST /adoption/scan`
  - `POST /adoption/import`
  - `POST /services/{serviceId}/environments/{envId}/deploy`
  - `POST /services/{serviceId}/environments/{envId}/restart`
  - `POST /services/{serviceId}/environments/{envId}/stop`
- Removed corresponding exported HTTP handler methods from `llm.go`, `adoption.go`, and `service_actions.go`.
- Preserved LLM read route registrations and `PUT /llm/routes/{id}`.
- Updated client helpers to fail before HTTP with signer-first Nostr guidance.
- Updated tests and docs to reflect removal.

## Automated Verification

- `go test ./internal/api/... ./pkg/client` — passed.
- `go build ./...` — passed.

## Notes

No Nostr reactor behavior was modified. The canonical replacement path remains signed Nostr request events handled by `internal/controlplane/reactor.go`.
