# Continuity

**Continuity** (`/continuity`) is a read-oriented view of service placement, recovery readiness, and failover evidence. Its simulation runs in the browser and does not mutate production.

## What the page shows

The page combines trusted topology, service state, worker/runtime availability, and continuity status from the relay-backed browser store. It highlights missing replicas, single failure domains, stale evidence, and operations that need an operator decision.

A simulation lets you mark targets unavailable and inspect the resulting coverage. It changes only local UI state.

## Trust and confidentiality

Continuity records are accepted from configured service publishers and from operators in the fleet-OCK-encrypted `operators:continuity` allowlist. The record is addressed by that scope; its member pubkeys are encrypted. Signatures, authors, tags, and replaceable-event ordering are checked before a record enters the view.

If the signed-in operator cannot unwrap the fleet OCK, the page cannot use the operator allowlist and reports the record as unreadable rather than widening trust.

## Reading status

- Wait for relay catch-up before treating an empty view as proof that no record exists.
- Treat stale observations as unknown.
- Use canonical service and environment state to confirm whether a failover action completed.
- Use audit and intent-status evidence to explain rejected or incomplete operations.

Continuity operations that require daemon-side planning use the registered signed request methods and return an acknowledgement plus canonical outcome records. A missing reply is not proof that nothing happened.

## Related

- [Deployments](deployments.md)
- [Workers](workers.md)
- [Fleet Health](fleet-health.md)
- [Nostr Integration](../nostr-integration.md)
