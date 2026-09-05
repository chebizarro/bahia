# Notifications

**Notifications** alert an organization about operational events and retain delivery results for investigation.

## Supported channels

Bahia currently implements two channel types:

| Type | Value | Configuration |
|---|---|---|
| Webhook | `webhook` | An HTTPS endpoint, with optional headers or signing secret |
| Nostr direct message | `nostr_dm` | The recipient's Nostr pubkey |

Email and Slack are not registered channel types. A Slack incoming-webhook URL can be targeted through a generic webhook channel, but Bahia does not provide a separate Slack sender.

The Nostr DM sender is available only when the server has a Nostr private key. Invalid or unsupported channel types fail validation instead of being accepted silently.

## Organization scope and authorization

Encrypted browser channel and log operations belong to an organization. The authenticated caller must be a member of the selected organization; those repository queries are qualified by that organization so another tenant's channel or log is not visible or mutable.

The direct MCP notification handlers do not accept an `org_id` and currently use the server's unqualified notification repository. The standard app also leaves external MCP authorization fail-closed. Do not use these direct tools as a cross-tenant operator surface; prefer the tenant-scoped browser/encrypted operations. See [MCP Tools Reference](../mcp-tools.md#authorization).

## Creating a channel

### Web UI

1. Open **Notifications**.
2. Select the current organization.
3. Choose **New Channel**.
4. Select **Webhook** or **Nostr DM**, enter its configuration and event filter, and save.
5. Use **Test** before relying on the channel.

The browser performs channel and log operations with encrypted control-plane methods:

- `notifications.channels.list`, `get`, `create`, `update`, `delete`, and `test`
- `notifications.logs.list`

### MCP

The CLI does not register a `bahia notifications` command. Use the tenant-scoped web UI. The registered direct MCP tools are available only to explicitly authorized embeddings and are not organization-qualified.

```json
{
  "tool": "bahia_create_notification_channel",
  "arguments": {
    "name": "deployment-alerts",
    "channel_type": "webhook",
    "config": {
      "url": "https://hooks.example.com/bahia"
    },
    "event_filter": {
      "type": "deployment.failed"
    }
  }
}
```

For a Nostr DM channel, use `"channel_type": "nostr_dm"` and provide the recipient pubkey required by the channel configuration.

## Managing and testing channels

The registered channel tools are:

| Tool | Purpose |
|---|---|
| `bahia_list_notification_channels` | List channels in the caller's organization |
| `bahia_get_notification_channel` | Read one organization-scoped channel |
| `bahia_create_notification_channel` | Create a channel |
| `bahia_update_notification_channel` | Replace channel fields such as config, filter, or enabled state |
| `bahia_delete_notification_channel` | Delete a channel |
| `bahia_test_notification_channel` | Deliver a test through that exact channel |

Testing a disabled channel or a delivery that cannot be accepted returns an error. The test path does not report success merely because the request was queued.

## Event filters

A channel's `event_filter` controls which application events it receives. For example:

```json
{
  "type": "security.policy_breached"
}
```

Security OSV notifications are breach-only: a new or materially changed breach fingerprint dispatches, while unchanged recurring breaches and clean scans do not.

## Delivery behavior and logs

The dispatcher creates an organization-scoped log record for each attempted notification and then calls the selected sender. Send and log-update failures are returned to the caller rather than converted into success.

For Nostr DMs, zero relay acceptances count as a delivery failure. For webhooks, connection, TLS, authentication, and non-success response failures remain visible in the log's status and error fields.

Use the Notifications log view or these MCP tools:

| Tool | Purpose |
|---|---|
| `bahia_list_notifications` | List recent logs; supports status and event-type filters |
| `bahia_get_notification` | Registered compatibility operation; direct get-by-ID is unsupported, so use list |
| `bahia_mark_notification_read` | Compatibility mutation that finds a recent log and overwrites its status to `sent` |
| `bahia_dismiss_notification` | Registered compatibility operation; dismissal is unsupported |

```json
{
  "tool": "bahia_list_notifications",
  "arguments": {
    "status": "unread",
    "event_type": "deployment.failed",
    "limit": 50
  }
}
```

The MCP status filter maps `read` to sent records and `unread` to pending or retrying records. This is a delivery-status compatibility mapping, not a separate user-read receipt. Although dismissal treats logs as immutable audit records, the current mark-read compatibility handler does mutate the stored delivery status to `sent`.

## Sensitive configuration

Channel URLs, headers, and recipient details are sensitive. Browser channel CRUD uses encrypted request/result events and requires a NIP-44-capable signer. Do not publish channel configuration in public Nostr events or logs.

## Troubleshooting

### A channel test fails

- Confirm the channel is enabled.
- For webhooks, verify outbound connectivity, TLS, credentials, and the endpoint response.
- For Nostr DMs, verify the server signing key, recipient pubkey, and relay acceptance.
- Inspect the organization-scoped delivery log for the stored error.

### Expected events do not dispatch

- Confirm the channel's event filter matches the exact event type.
- Confirm the channel belongs to the active organization.
- For `security.policy_breached`, confirm the breach is new or materially changed.

## Related

- [Services](services.md) — Notification sources
- [Deployments](deployments.md) — Deployment outcomes
- [Security](security.md) — OSV breach notifications
- [Organizations](organizations.md) — Tenant membership and access

## Managed-instance alerts

Notification channel filters may include `runtime.instance_health_changed`, `runtime.recovery_requested`, `runtime.recovery_completed`, `runtime.recovery_failed`, `runtime.recovery_budget_exhausted`, and `runtime.maintenance_changed`. Recovery/error/budget alerts are immediate. Warning delivery follows the instance policy's minimum interval.
