# Notifications

**Notifications** in Bahia alert you about deployment events, drift detection, and system status.

## Overview

Bahia supports multiple notification channels:
- **Webhook** — HTTP POST to any endpoint
- **Email** — SMTP-based alerts
- **Slack** — Slack incoming webhooks
- **Nostr** — Direct messages via Nostr

## Notification Events

| Event | Description |
|-------|-------------|
| `deployment.started` | Deployment run began |
| `deployment.completed` | Deployment succeeded |
| `deployment.failed` | Deployment failed |
| `deployment.approval_required` | Intent awaiting approval |
| `drift.detected` | Observed state differs from desired |
| `drift.resolved` | Drift was corrected |
| `worker.offline` | Worker became unreachable |
| `worker.online` | Worker reconnected |

## Creating Notification Channels

### Web UI

1. Navigate to **Notifications** in the sidebar
2. Click **New Channel**
3. Select channel type:
   - Webhook, Email, Slack, or Nostr
4. Configure the channel
5. Click **Create**

### CLI

```bash
# Webhook channel
bahia notifications channels create \
  --name "Deploy Alerts" \
  --type webhook \
  --config url="https://hooks.example.com/bahia" \
  --events deployment.completed,deployment.failed

# Slack channel
bahia notifications channels create \
  --name "Slack Deploys" \
  --type slack \
  --config webhook_url="https://hooks.slack.com/services/..." \
  --events deployment.completed
```

### MCP Tool

```json
{
  "tool": "bahia_create_notification_channel",
  "arguments": {
    "name": "Deploy Alerts",
    "type": "webhook",
    "config": {
      "url": "https://hooks.example.com/bahia"
    },
    "events": ["deployment.completed", "deployment.failed"]
  }
}
```

## Channel Types

### Webhook

Send HTTP POST requests to any endpoint:

```yaml
type: webhook
config:
  url: "https://hooks.example.com/bahia"
  secret: "webhook-secret"  # HMAC signature
  headers:
    Authorization: "Bearer token"
```

Payload format:
```json
{
  "event": "deployment.completed",
  "timestamp": "2024-01-15T10:30:00Z",
  "data": {
    "service_id": "svc-123",
    "environment_id": "env-456",
    "artifact_id": "art-789",
    "status": "completed"
  }
}
```

### Email

SMTP-based email notifications:

```yaml
type: email
config:
  recipients:
    - "team@example.com"
    - "oncall@example.com"
  subject_prefix: "[Bahia]"
```

Requires SMTP configuration in Bahia server.

### Slack

Slack incoming webhooks:

```yaml
type: slack
config:
  webhook_url: "https://hooks.slack.com/services/T00/B00/xxx"
  channel: "#deployments"  # Optional override
  username: "Bahia"
```

### Nostr

Direct messages via Nostr:

```yaml
type: nostr
config:
  recipient_pubkeys:
    - "npub1..."
    - "npub2..."
```

## Filtering Notifications

### By Event Type

```yaml
events:
  - deployment.completed
  - deployment.failed
  - drift.detected
```

### By Service

```yaml
filters:
  service_ids:
    - "svc-123"
    - "svc-456"
```

### By Environment

```yaml
filters:
  environment_ids:
    - "env-prod"
```

### By Tags

```yaml
filters:
  tags:
    team: "payments"
    criticality: "high"
```

## Managing Channels

### Listing Channels

```bash
bahia notifications channels list
```

### Viewing Channel

```bash
bahia notifications channels get channel-123
```

### Updating Channel

```bash
bahia notifications channels update channel-123 \
  --events deployment.completed,deployment.failed,drift.detected
```

### Deleting Channel

```bash
bahia notifications channels delete channel-123
```

## Testing Channels

Send a test notification to verify configuration:

### Web UI

1. Go to channel detail
2. Click **Test Channel**
3. Verify notification received

### CLI

```bash
bahia notifications channels test channel-123
```

### MCP Tool

```json
{
  "tool": "bahia_test_notification_channel",
  "arguments": {
    "channel_id": "channel-123"
  }
}
```

## Notification Logs

View notification delivery history:

### Web UI

1. Go to **Notifications** → **Log**
2. Filter by channel, event, or status
3. View delivery attempts and errors

### CLI

```bash
# Recent logs
bahia notifications log --limit 50

# Logs for specific channel
bahia notifications log --channel-id channel-123

# Failed deliveries
bahia notifications log --status failed
```

## Encrypted Transport

Notification channel configuration is **sensitive** — webhook secrets, URLs, etc.

In the Nostr-native model:
- Channel CRUD uses encrypted request/result events (5980/7980)
- Secrets are never sent to public relays
- Requires NIP-44 capable signer

## Best Practices

1. **Use multiple channels** — Redundancy for critical alerts
2. **Filter appropriately** — Avoid alert fatigue
3. **Test before relying** — Verify channels work
4. **Secure webhooks** — Use secrets and HTTPS
5. **Monitor delivery** — Check logs for failures

## Troubleshooting

### Notifications Not Received

- Check channel configuration
- Verify events are enabled
- Check notification logs for errors
- Test the channel

### Webhook Failures

- Verify URL is reachable from Bahia
- Check for TLS/certificate issues
- Verify authentication if required

### Email Not Delivered

- Check SMTP configuration
- Verify recipient addresses
- Check spam folders

### Slack Not Posting

- Verify webhook URL is current
- Check Slack app permissions
- Test webhook directly with curl

## Related

- [Services](services.md) — Notification sources
- [Deployments](deployments.md) — Deployment events
- [Organizations](organizations.md) — Team alerts
