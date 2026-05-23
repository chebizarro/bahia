# Verification Report: bahia-avtn

## Observed behavior

- `internal/service/continuity_status_store.go` defines `ContinuityStatus`, operation-state constants, a `ContinuityStatusReader` interface, and an RWMutex-protected in-memory store keyed by normalized service key.
- The store supports `Update`, `GetServiceStatus`, DNS-facing `GetServiceContinuityStatus`, and deterministic `ListAllStatuses` queries.
- `internal/service/continuity_status_projector.go` defines service-local continuity runtime event constants and subscribes via the injected `events.Publisher` constructor dependency.
- The projector updates the status store and publishes Nostr read-model events through an injected publish function that owns signing and OK verification.
- Kind 30351 uses `d=continuity-status:<service>` and only republishes when the requested idempotency fields change.
- Kind 30352 is append-only and is emitted when a service enters degraded, emergency, or offline profile from a changed profile state.
- Kind 30353 uses `d=recovery-progress:<service>:<runID>` and is idempotent per service/run progress fingerprint.
- Publish failures are returned from direct projection calls and are not cached as successful publication state, allowing retry on the next event.

## Verification evidence

- `go test ./internal/service` passed.
- `go test ./...` was run and failed in concurrently modified files outside this slice: `internal/adapters/nostr/continuity_serialization_test.go` expected UTC heartbeat observation time but observed local PST time, and `internal/controlplane/reactor_validation_test.go` expected three subscription filters but observed four.

## Known boundaries

- Adapter-level kind constants, signing, relay OK verification, and persisted Nostr event recording are intentionally delegated to the injected Nostr publish function and were not implemented in this service-only slice.
- DNS projector wiring is owned by the concurrent DNS cutover work. This implementation exposes `GetServiceContinuityStatus` without modifying DNS files.
- Recipe executor and trigger engine runtime event production are separate Wave 2 slices; this projector defines the event contracts it consumes.
