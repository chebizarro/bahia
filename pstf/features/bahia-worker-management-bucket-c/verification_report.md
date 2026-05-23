# Verification Report: Bahia Worker Management Bucket C

Date: 2026-05-23

## Scope Verified

- Worker command publisher for cordon, uncordon, drain, undrain, maintenance enter, maintenance exit, and labels update.
- Worker command handlers that validate requests, update the worker repository, publish status/result events, and publish a replaceable worker state read model.
- Reactor command kind registration, dispatch wiring, subscription filters, and production app worker repository injection.

## Commands Run

```bash
GOCACHE=/tmp/bahia-go-build go test ./internal/controlplane/...
GOCACHE=/tmp/bahia-go-build go test ./internal/app/...
GOCACHE=/tmp/bahia-go-build go test ./internal/repository/...
GOCACHE=/tmp/bahia-go-build go test ./...
```

## Results

- `go test ./internal/controlplane/...`: passed.
- `go test ./internal/app/...`: passed.
- `go test ./internal/repository/...`: passed.
- `go test ./...`: passed.

## Evidence Summary

- Publisher tests verify signed Nostr command publication, worker and command tags, d-tag/idempotency handling, content fields, receipt correlation, and zero-relay rejection.
- Handler tests verify repository updates for scheduling state and labels, status/result emission, replaceable worker state publication, label read-model tags, and validation failure without mutation.
- Handler tests verify tag/content correlation conflicts and disabled-worker transition attempts are rejected before mutation.
- Subscription filter test verifies all worker command request kinds are included in reactor subscriptions.
- App package test verifies production wiring compiles with `WithWorkerRepository(workerRepo)`.
- Repository package test verifies targeted worker scheduling-state and label update methods compile with the existing repository layer.

## Remaining Work

Remaining work is tracked in existing Beads outside Bucket C, including scheduling enforcement, MCP worker tools, worker assignment state, and drain status read models.
