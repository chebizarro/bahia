# bahia-2kjh Verification Report

## Scope

Closeout evidence for Bead `bahia-2kjh`: atomically reconfigure runtime relay pools from hydrated canonical `30900` relay-settings policy after sibling code work.

## Evidence

- `internal/adapters/nostr/relay_pool.go` adds `RelayPool.ReconfigureRelayURLs`, normalizes/deduplicates configured URLs, returns cloned URL snapshots, preserves unchanged relay entries, closes removed relay connections, and creates added relay entries disconnected for existing connect/publish/subscribe paths.
- `internal/controlplane/relay_settings_hydrator.go` adds an `OnSnapshotApplied` callback invoked after trusted canonical state validation/storage; convergence is triggered by relay EVENT handling, not polling or timeout completion.
- `internal/app/relay_topology_coordinator.go` applies hydrated ContextVM/service relay topology to existing control-plane and service pools while preserving sidecar precedence, Loom relay inclusion, and existing pool topology for topology-empty snapshots.
- `internal/app/app.go` wires the topology coordinator into the relay-settings hydrator without mutating shared `config.Config`.

## Verification

Local command result in this closeout pass:

- PASS: `GOCACHE=/tmp/bahia-go-cache go test ./internal/adapters/nostr ./internal/app ./internal/controlplane -run 'TestRelayPool|TestRelayTopologyCoordinator|TestRelaySettings|TestRelayAdmin'`

## Boundaries

- No new relay-routing event kinds or ContextVM methods were introduced by this evidence/docs closeout.
- No polling, sleeps, timeout-based completion, or shared `config.Config` mutation are required for relay-pool convergence in the touched topology path.
- Browser/UI canonical hydration and NIP-86 validation closeout is tracked under `bahia-ho1r`; the dirty canonical E2E defect was resolved in follow-up evidence and `bahia-ho1r` is now closed.
