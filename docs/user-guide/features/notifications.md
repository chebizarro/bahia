# Notifications

**Notifications** (`/notifications`) manages organization-scoped delivery channels. The delivery log is available at `/notifications/log`.

## Channels

Supported channel types include webhook and Nostr DM delivery. A channel defines its organization, event filters, destination configuration, enabled state, and retry policy.

Create and edit channels in the web app or with the CLI:

```bash
bahia notifications channels list
bahia notifications channels get <channel-id>
bahia notifications channels create --file channel.json
bahia notifications channels update --file channel.json
bahia notifications channels delete <channel-id>
```

MCP provides list/get/create/update/delete/test tools under `bahia_*_notification_channel` names. Channel responses redact webhook URLs, tokens, and other credentials.

## Authorization and encryption

Channel records are encrypted with the organization's OCK. The signer must be able to unwrap that key and hold the organization permission required by the action. Service-only delivery credentials remain inside the encrypted service payload and are never returned to a browser, CLI, or MCP caller.

External MCP calls pass through the platform authorization gate in addition to organization checks. Treat an externally exposed MCP endpoint as an administrative surface.

## Filters and delivery

Filters select normalized event types such as deployment, policy, security, backup, route, or runtime alerts. A channel test uses the stored configuration without revealing it.

The delivery log records bounded outcomes and error summaries. MCP exposes `bahia_list_notifications`, `bahia_get_notification`, and `bahia_mark_notification_read`. The log is immutable: `bahia_dismiss_notification` reports that dismissal is unsupported.

## Troubleshooting

- Confirm the signer can read the organization and channel record.
- Verify the event type matches the channel filter.
- Use **Test** to separate destination credentials from event matching.
- Inspect the delivery log and daemon readiness rather than assuming relay acceptance means delivery.

## Related

- [Organizations](organizations.md)
- [Deployments](deployments.md)
- [Security](security.md)
- [Route Canaries](route-canaries.md)
