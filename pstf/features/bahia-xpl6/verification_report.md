# Verification Report — bahia-xpl6

## Evidence

- Added `internal/service/continuity_graph.go` as a pure evaluator over injected read models.
- Added deterministic unit tests in `internal/service/continuity_graph_test.go` covering service assessment, stale heartbeat, stale replication, all-service ordering, and worker-failure simulation.

## Commands Run

```text
go test ./internal/service -run 'Continuity(Graph|Definition|Status|Heartbeat|Failover)'
go test ./internal/service
go test ./...
```

All commands passed on 2026-05-23.
