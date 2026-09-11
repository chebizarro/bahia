# Environment States

The **Environment States** route at `/environment-states` compares desired and observed deployment state across services and environments.

## What the page shows

Each row includes:

- service;
- environment;
- deployed artifact;
- synchronization status;
- drift details;
- deployment information.

Filter the table by **All states**, **Drifted**, or **In sync**. Select the Drift cell to inspect the complete state payload in a modal.

## Data model

The page loads services, environments, and canonical state read models from the browser stores, then joins them for display. The state event is evidence about the service/environment pair; the service and environment definitions provide names and configuration context.

- **In sync** means the accepted observed artifact matches desired state.
- **Drifted** means the accepted observation does not match desired state.
- Missing or stale evidence should be investigated rather than interpreted as healthy.

The route is in the browser's protected-prefix list (`web/src/lib/auth/route-access.js`), so it requires a signer-first session. It is not linked from the sidebar; open `/environment-states` directly. Backend and encrypted-operation authorization remain authoritative; route visibility alone does not grant access to mutate state.

## Investigating drift

1. Filter to **Drifted**.
2. Open the full state payload and confirm service ID, environment ID, desired artifact, observed artifact, event author, and timestamp.
3. Check the related deployment run and worker observation.
4. Resolve the underlying runtime difference or initiate a verified deployment/rollback.
5. Wait for a new canonical observation before considering the drift resolved.

## Related

- [Environments](environments.md) — Deployment targets
- [Services](services.md) — Desired service configuration
- [Deployments](deployments.md) — Runs and rollback
- [Nostr Integration](../nostr-integration.md) — Canonical read-model semantics
