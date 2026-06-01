# Deployments

**Deployments** in Bahia follow an intent-based workflow: you declare what you want deployed, policies are evaluated, and workers execute the deployment.

## Deployment Workflow

```
Intent Created → Policy Evaluation → Approval (if needed) → Run Execution → Observation
```

### 1. Deployment Intent

A **Deployment Intent** is a request to deploy an artifact:

```yaml
service_id: "svc-123"
environment_id: "env-456"
artifact_id: "art-789"
requested_by: "npub1user..."
reason: "Deploy new feature X"
```

### 2. Policy Evaluation

Bahia evaluates policies:
- SBOM requirements
- Test coverage
- Security scans
- Custom rules

### 3. Approval

If policies or environment require approval:
- Manual approval by authorized pubkey
- Or automated approval if all policies pass

### 4. Deployment Run

A **Deployment Run** executes the intent:
- Worker picks up the job
- Pulls container image
- Applies to runtime
- Reports progress

### 5. Runtime Observation

After deployment:
- Workers observe what's running
- State is compared to desired
- Drift is detected if mismatch

## Creating Deployments

### Web UI

1. Go to **Services** → select your service
2. Click **Deploy**
3. Select:
   - **Environment**: Where to deploy
   - **Artifact**: Which version to deploy
   - **Reason**: Why you're deploying (optional)
4. Click **Create Deployment**

### CLI

```bash
bahia deploy \
  --service payment-api \
  --environment production \
  --artifact art-789 \
  --reason "Hotfix for payment timeout"
```

Or using signer-first Nostr:

```bash
bahia services actions deploy payment-api \
  --environment production \
  --artifact art-789
```

### MCP Tool

```json
{
  "tool": "bahia_deploy",
  "arguments": {
    "service_id": "svc-123",
    "environment_id": "env-456",
    "artifact_id": "art-789"
  }
}
```

Returns correlation metadata for Nostr follow-up:
```json
{
  "request_event_id": "abc123...",
  "request_kind": 5961,
  "status_kind": 6961,
  "result_kind": 7961,
  "service_id": "svc-123"
}
```

### Nostr (Signer-First)

Publish a `5961` DeployRequest event:

```json
{
  "kind": 5961,
  "content": {
    "service_id": "svc-123",
    "environment_id": "env-456",
    "artifact_id": "art-789"
  },
  "tags": [
    ["service", "svc-123"],
    ["environment", "env-456"],
    ["artifact", "art-789"]
  ]
}
```

## Monitoring Deployments

### Web UI

The **Deployments** page shows:
- **Pending**: Awaiting approval
- **Running**: Currently executing
- **Completed**: Successfully finished
- **Failed**: Encountered errors

Click a deployment to see:
- Intent details
- Run progress and logs
- Final status

### CLI

```bash
# List recent deployments
bahia deployments list

# Get deployment details
bahia deployments get intent-123

# View run logs
bahia deployments logs run-456
```

### Nostr Subscriptions

Subscribe to deployment events:

```json
{
  "kinds": [6961, 7961],
  "#e": ["<request-event-id>"]
}
```

- **6961 DeploymentStatus**: Progress updates
- **7961 DeploymentResult**: Terminal result

## Approving Deployments

When an intent requires approval:

### Web UI

1. Go to **Deployments** → **Pending**
2. Review the intent details
3. Click **Approve** or **Reject**

### CLI

```bash
# Approve
bahia deployments approve intent-123

# Reject
bahia deployments reject intent-123 --reason "Missing tests"
```

### Nostr

Publish a `5966` DeploymentApproval event:

```json
{
  "kind": 5966,
  "content": {
    "intent_id": "intent-123",
    "approved": true
  },
  "tags": [
    ["e", "<intent-event-id>"],
    ["intent", "intent-123"]
  ]
}
```

## Rollbacks

Roll back to a previous artifact:

### Web UI

1. Go to service detail
2. Find the deployment to roll back to
3. Click **Rollback to this version**

### CLI

```bash
bahia rollback \
  --service payment-api \
  --environment production
```

This creates a new intent to deploy the previously successful artifact.

### MCP Tool

```json
{
  "tool": "bahia_rollback",
  "arguments": {
    "service_id": "svc-123",
    "environment_id": "env-456"
  }
}
```

## Deployment States

| State | Description |
|-------|-------------|
| `pending` | Intent created, awaiting policy/approval |
| `approved` | Ready to execute |
| `rejected` | Rejected by policy or approver |
| `queued` | Waiting for available worker |
| `running` | Execution in progress |
| `completed` | Successfully deployed |
| `failed` | Deployment failed |
| `cancelled` | Manually cancelled |

## Run Logs

Deployment runs capture logs:

### Web UI

1. Go to the deployment run
2. Click **Logs** tab
3. View stdout/stderr

### CLI

```bash
bahia deployments logs run-456 --tail 100
```

### MCP Tool

Logs are accessed via encrypted request (sensitive data):

```json
{
  "tool": "bahia_get_run_logs",
  "arguments": {
    "run_id": "run-456"
  }
}
```

## Deployment History

View deployment history for a service:

```bash
bahia deployments list --service payment-api --limit 20
```

Or in the web UI on the service detail page.

## Read Models

Deployment state is published as Nostr events:

| Kind | d-tag | Content |
|------|-------|---------|
| 31967 | `intent_id` | Deployment intent |
| 31968 | `run_id` | Deployment run |
| 31961 | `service_id:environment_id` | Current state |

## Best Practices

1. **Always provide a reason** — Helps with auditing and debugging
2. **Review before approving** — Check artifact changes
3. **Monitor after deployment** — Watch for drift or errors
4. **Use policies** — Automate safety checks
5. **Keep artifacts immutable** — Never modify deployed images

## Troubleshooting

### Deployment Stuck in Pending

- Check if approval is required
- Verify policies are passing
- Ensure authorized approvers are available

### Deployment Failed

- Check run logs for errors
- Verify artifact exists and is pullable
- Check worker connectivity

### Drift After Deployment

- Observation may be delayed
- Check runtime target health
- Verify container is running

## Related

- [Services](services.md) — What you deploy
- [Artifacts](artifacts.md) — What you deploy
- [Policies](policies.md) — Approval rules
- [Workers](workers.md) — Execution agents
