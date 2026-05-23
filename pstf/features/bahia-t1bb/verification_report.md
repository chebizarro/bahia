# Verification Report: bahia-t1bb

## Observed behavior

- `internal/service/continuity_definition_store.go` defines an RWMutex-protected latest-value in-memory store for service continuity profiles, replication policies, and failover/recovery recipes.
- `internal/service/failover_trigger_engine.go` defines a ticker-driven evaluator with healthy, suspect, firing, and active trigger phases.
- Expired primary heartbeat snapshots move through suspect before the engine emits one `continuity.failover_requested` event with service, recipe, primary, selected standby, run ID, and heartbeat evidence.
- Missing standby replication targets emit one `continuity.failover_failed` event instead of repeatedly retrying.
- Once firing or active, a service run is not auto-recovered when heartbeats return.

## Verification evidence

- `GOCACHE=/tmp/bahia-go-cache go test -count=1 ./internal/domain ./internal/service` passed.

## Known boundaries

- Nostr event ingestion/publishing, reactor registration, recipe execution, status projection, DNS cutover, and bootstrap composition were intentionally not modified in this issue because they are owned by other Wave 2 agents.
