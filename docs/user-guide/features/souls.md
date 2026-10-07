# Souls

A Soul is the signed identity and runtime specification for an AI agent. Soul Factory provisions, observes, and controls agents when `soul_factory.enabled` is true.

## Web and CLI

Use **Souls** (`/souls`) to browse templates and agents, provision a Soul, and run lifecycle actions.

```bash
bahia souls list --status active
bahia souls get scout
bahia souls provision scout --template "31950:<publisher-pubkey>:research-agent" --tier standard --follow
bahia souls await <request-id>
bahia souls suspend scout --reason "maintenance"
bahia souls resume scout
bahia souls redeploy scout
bahia souls regenerate scout --brief "Research and summarize operational evidence"
bahia souls revoke scout --reason "retired"
bahia souls templates list
bahia souls templates get research-agent
```

The public MCP registry does not expose Soul Factory lifecycle tools. Use the web app, `bahia souls`, or the Soul Factory Nostr contract.

## Event contract

| Kind | Purpose |
|---|---|
| `31950` | Addressable Soul template |
| `31951` | Addressable provisioned Soul |
| `31952` | Soul draft |
| `31953` | Fleet-wide OpenClaw configuration |
| `5950` | Provisioning request |
| `6950` | Provisioning progress |
| `7950` | Provisioning result |
| `1950` | Lifecycle action |

Provisioning subscribes for progress and results before publishing the request. A result must match the request and a trusted Soul Factory publisher. Lifecycle actions do not become complete merely because a relay accepted them.

## Authorization

The configured Soul Factory operator keys define who may act. The browser also reads the fleet-OCK-encrypted `operators:soul-factory` record; an operator not present in that record is not trusted to author fleet configuration.

The **OpenClaw Fleet** settings page publishes kind `31953` configuration with the active signer and reports each relay's `OK` result. New provisions use the newest trusted document; existing Souls reconcile according to the fleet rollout view.

## Provisioning and recovery

Provisioning creates the signed Soul, prepares runtime state, deploys the agent, and publishes progress. Interrupted work resumes from its durable ledger and canonical events. Inspect the latest progress/result and runtime evidence before retrying with the same request identity.

Runtime reconciliation is available to platform administrators through dry-run-first endpoints at `/api/v1/soulfactory/legacy-reconciliation/preview` and `/apply`. Preview classifications must be reviewed and bound into the apply request. These endpoints do not infer identity from container names alone.

## Safety

- Use an addressable template coordinate, not a display label.
- Keep runtime credentials in protected secrets.
- Treat revoke as destructive; use suspend for reversible maintenance.
- Verify the trusted publisher and relay catch-up before diagnosing a missing Soul.
- Use the fleet configuration page for shared runtime policy, not per-agent ad hoc edits.

## Related

- [Workers](workers.md)
- [Services](services.md)
- [Settings](settings.md)
- [Nostr Integration](../nostr-integration.md)
