# Config Fabric

**Config Fabric** (`/config-fabric`) manages signed desired configuration for a service/policy/scope coordinate and compares it with the effective version reported by the service.

## State model

Each coordinate tracks:

- the newest desired version and event;
- the effective applied version;
- rejected versions and their bounded reason;
- whether desired configuration is withdrawn;
- audit and status history.

A withdrawn desired document does not erase the last effective configuration or revert an allowlist. Publish an explicit replacement to change what the service applies.

## Web workflow

1. Open **Config Fabric** and select **Publish Config**.
2. Choose the service, policy, scope, and desired document.
3. Review the next version and publish with the active signer.
4. Open the coordinate to compare desired and effective documents, rejection state, versions, and audit history.
5. Use **Rollback** on a known desired version when you need to republish it as the next version.

The browser reads signed relay state. A relay `OK` proves publication only; the effective version changes when the service publishes status.

## CLI

```bash
bahia --relay wss://relay.example --service-pubkey <service-pubkey> \
  config publish --file config-request.json

bahia --relay wss://relay.example --service-pubkey <service-pubkey> \
  config drift

bahia --relay wss://relay.example --service-pubkey <service-pubkey> \
  config rollback <desired-event-id>
```

Publish and rollback catch up every configured relay before choosing the next version. Drift can render cached state with a warning when catch-up is incomplete. `--outbox-path` selects the local outbox used for per-relay delivery tracking.

## Safety

- Keep credentials out of desired documents; publish references.
- Treat rejection as an unapplied desired version, not partial success.
- Compare exact versions and event IDs before rollback.
- Use a stable service/policy/scope coordinate.
- Preserve the signed event and outbox evidence until service status is visible.

## Related

- [Policies](policies.md)
- [Settings](settings.md)
- [Nostr Integration](../nostr-integration.md)
