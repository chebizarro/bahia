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

## Operator commands and backend hydration

Failover and recovery mutation intent uses ContextVM, not legacy kinds `38430`
and `38431`. Send a signed kind `25910` JSON-RPC request addressed (`p`) to the
Bahia service, optionally wrapped in `1059` or `21059`:

```json
{
  "jsonrpc": "2.0",
  "id": "continuity-run-1",
  "method": "continuity/failover",
  "params": {
    "service_key": "api",
    "target_worker_pubkey": "<32-byte lowercase hex worker public key>",
    "recipe_name": "switch",
    "target_profile": "degraded",
    "idempotency_key": "<stable unique run key>"
  }
}
```

Use `continuity/recovery` with `target_profile: "full"` for recovery. The profile
and recipe name are optional; the target worker and service key are required.
`_meta.progressToken` can supply the idempotency key instead. Requester identity
comes from the authenticated inner event, never `requested_by` in the payload.
Both methods require `nostr.authorized_pubkeys`; an empty list denies everyone.

The backend loads profiles (`31400`), failover policies (`31401`), replication
policies (`31403`), and recovery workflows (`31404`) from a long-lived REQ scoped
to those kinds and the configured operator authors. It discovers the inventory
from their definitions, so `d` coordinates are not known before backfill. The
cold rebuild starts from history without a truncating limit or persisted cursor.
Definitions are applied synchronously; command execution waits for actual EOSE
from every initially subscribed stream. CLOSED, disconnect, cancellation and
elapsed time do not count as successful catch-up. The REQ stays open for live
updates, and replay/older replacements do not mutate the current projection.
Equal timestamps prefer the lexicographically lower event ID.

Commands execute the selected stored recipe rather than republishing a legacy
command. Immediate errors include missing recipes and unavailable runtime
adapters. The JSON-RPC result reports the executor's outcome; durable operational
truth still comes from signed continuity status/progress observables. Retry with
the same idempotency key and unchanged params to replay a retained result rather
than repeat recipe actions. The transport's configured response retention applies;
this is not a transactional exactly-once guarantee across execution/process crashes.

Standby definitions (`31402`) are deliberately **not ingested by this backend
runner**. The existing standby handler has no downstream inventory consumer.
Replication policies and explicit command targets remain the runtime's sources
of standby selection; adding a second inventory requires an explicit integration
and authority decision. The browser may still display standby definitions.
The browser's legacy Requests view is historical, not a command submission path.

## Related

- [Deployments](deployments.md) — Rollout and rollback behavior
- [Workers](workers.md) — Worker availability
- [Fleet Health](fleet-health.md) — Fleet pressure and health
- [Nostr Integration](../nostr-integration.md) — Replay and canonical read models
