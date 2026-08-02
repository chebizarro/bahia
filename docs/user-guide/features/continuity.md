# Continuity

The **Continuity** route at `/continuity` is a read-only operational view of service failover readiness and recovery progress. Its data is reconstructed from signed Nostr events rather than a mutable browser-local source of truth.

## What the page shows

The page has three tabs:

- **Status** — continuity profiles, current operating state, primary and standby placement, active recovery step, and recent run progress.
- **Topology** — event-derived failover and recovery relationships, standby count, replication configuration, and heartbeat evidence.
- **Simulation** — a local what-if assessment of a worker failure using the events already loaded by the page.

Simulation does not publish a request or change runtime state. Treat it as planning assistance, not proof that a failover has executed successfully.

The `/continuity` route is not currently included in the browser's protected-prefix list. Its relay-derived view must therefore be treated as visible to anyone who can load the app and relay data. Backend and signed-event authorization still govern mutations; route visibility does not grant failover authority.

## Nostr inputs

The view reads current continuity state from canonical events:

| Kind | Purpose |
|---|---|
| `30351` | Continuity status read model |
| `30353` | Recovery progress read model |
| `30315` | Heartbeat observation with `domain=continuity` |
| `31400`–`31404` | Continuity profile, failover policy, standby, replication, and recovery workflow definitions |
| `30900` | Canonical worker state used in the topology assessment |

The browser requests at most 1,000 events for each continuity filter and deduplicates replaceable events before projecting the page.

## Reading status safely

1. Confirm the service has a continuity profile.
2. Check the primary, active, and standby placements.
3. Confirm recent heartbeats and replication configuration.
4. If recovery is active, use the current step and the `30353` progress event to follow it.
5. Correlate the view with deployment and worker health before taking an operational action.

A displayed definition is desired configuration; status and progress events are the observable evidence of what happened.

## Related

- [Deployments](deployments.md) — Rollout and rollback behavior
- [Workers](workers.md) — Worker availability
- [Fleet Health](fleet-health.md) — Fleet pressure and health
- [Nostr Integration](../nostr-integration.md) — Replay and canonical read models
