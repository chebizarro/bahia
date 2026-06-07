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

## Desired-State Runtime Behavior

For Compose and Docker runtimes, deploy and rollback flows build a canonical desired-state snapshot and hash before apply. Bahia persists the snapshot on the deployment intent, stores apply metadata on the deployment run, records the latest desired runtime state on the service/environment row, and compares it with normalized runtime observations for drift.

Compose desired-state deploys require a Bahia-owned Compose directory. Bahia renders the whole managed project for the environment or deployment unit into `docker-compose.yml`, generated `.bahia/env/<service-key>.env` files, and `.bahia/render-state.json`, validates the staged project, then applies with full-project `up -d --remove-orphans`. Unknown, operator-authored, or explicitly non-owned directories are blocked before file writes unless a valid Bahia render marker proves prior ownership or an operator has set `bahia_owned: true` after confirming the directory is dedicated to Bahia generation.

Docker desired-state deploys use Bahia labels and `desired_hash` to decide whether the existing managed container already matches the requested state. A matching hash is a no-op apply followed by observation; a hash mismatch triggers pull per policy, prerequisite network/volume ensure, replacement container create/start, and observation. Endpoint connection material remains server-managed through runtime endpoint aliases; public deployment events expose IDs, hashes, and target keys, not raw Docker hosts or TLS credentials.

Generated Compose env files may contain resolved secret values because Docker Compose needs them at apply time. They are written only under the Bahia-owned generated layout and are not included in Nostr events, apply metadata summaries, logs, or normalized observations. Desired-state and observation JSON store redacted secret refs or key-presence metadata only.

Kubernetes desired-state apply and Compose per-service fragments are deferred. Until those follow-up slices land, Kubernetes remains outside the Compose/Docker desired-state behavior and Compose uses the authoritative full-project output.

## Creating Deployments

### Web UI

1. Go to **Services** → select your service
2. Click **Deploy**
3. Select:
   - **Environment**: Where to deploy
   - **Artifact**: Which version to deploy
   - **Reason**: Why you're deploying (optional)
4. Click **Create Deployment**

### CLI and MCP

Deployment intent creation is signer-first. CLI, MCP, web, and agent flows use ContextVM JSON-RPC methods over Nostr kind `25910` (or encrypted `1059`/`21059` wrappers) and then follow canonical observables for durable progress. Transitional REST `POST /api/v1/deployments/intents` is available when a control-plane command publisher is configured; it publishes the same signed `service/deploy` command, requires relay `OK` acceptance through the publisher receipt, and returns `202` command metadata instead of a synchronous deployment-intent domain object.

### Nostr (ContextVM)

Publish a ContextVM `service/deploy` request as kind `25910` or inside an encrypted `1059`/`21059` wrapper:

```json
{
  "kind": 25910,
  "content": "{\"jsonrpc\":\"2.0\",\"id\":\"deploy-svc-123-env-456\",\"method\":\"service/deploy\",\"params\":{\"service_id\":\"svc-123\",\"environment_id\":\"env-456\",\"artifact_id\":\"art-789\",\"_meta\":{\"progressToken\":\"deploy-svc-123-env-456\"}}}",
  "tags": [
    ["p", "<bahia-service-pubkey>"],
    ["method", "service/deploy"],
    ["service", "svc-123"],
    ["environment", "env-456"],
    ["artifact", "art-789"]
  ]
}
```

Require relay `OK` with `accepted=true`. Treat the ContextVM response as receipt only; deployment completion comes from canonical observables.

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

Subscribe to canonical deployment observables:

```json
{
  "kinds": [30315, 4903, 30900],
  "authors": ["<bahia-service-pubkey>"],
  "#service": ["svc-123"],
  "#environment": ["env-456"]
}
```

- **30315**: NIP-38 operational status and progress, including desired-state steps such as `building_desired_state`, `locking_environment`, `rendering`, `applying`, `observing`, and `projecting`
- **4903**: immutable audit/provenance facts
- **30900**: current desired/observed deployment state, with optional sanitized `desired_hash`, renderer/target, revision/apply, and `observation_id` metadata

Add `#e=<ContextVM request event id>` when the emitted observable includes request correlation.

## Approving Deployments

When an intent requires approval:

### Web UI

1. Go to **Deployments** → **Pending**
2. Review the intent details
3. Click **Approve** or **Reject**

### CLI and MCP

Approval and rejection mutations are signer-first ContextVM intents. Use `deployment/approve` or `deployment/reject` and follow canonical status/audit/state observables.

### Nostr

Publish a ContextVM approval request:

```json
{
  "kind": 25910,
  "content": "{\"jsonrpc\":\"2.0\",\"id\":\"approve-intent-123\",\"method\":\"deployment/approve\",\"params\":{\"intent_id\":\"intent-123\",\"approved\":true,\"_meta\":{\"progressToken\":\"approve-intent-123\"}}}",
  "tags": [
    ["p", "<bahia-service-pubkey>"],
    ["method", "deployment/approve"],
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

### Nostr

Rollback is signer-first. The legacy `POST /api/v1/rollback` REST mutation has been removed; publish a ContextVM `service/rollback` intent:

```json
{
  "kind": 25910,
  "content": "{\"jsonrpc\":\"2.0\",\"id\":\"rollback-svc-123-env-456\",\"method\":\"service/rollback\",\"params\":{\"service_id\":\"svc-123\",\"environment_id\":\"env-456\",\"_meta\":{\"progressToken\":\"rollback-svc-123-env-456\"}}}",
  "tags": [
    ["p", "<bahia-service-pubkey>"],
    ["method", "service/rollback"],
    ["service", "svc-123"],
    ["environment", "env-456"]
  ]
}
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

## Canonical Observables

Deployment state is published as canonical Nostr observables:

| Kind | Tags | Content |
|------|------|---------|
| `30900` | `d`, `domain=deployment` or `domain=service`, `schema`, `service`, `environment`, optional `intent`/`run` | Current intent, run, and service/environment state projections |
| `30315` | `status`, `service`, `environment`, optional `intent`/`run`, correlation `e` | Progress and operational status |
| `4903` | requester `p`, resource tags, correlation `e` | Audit, policy, approval, and deployment facts |

Historical `31961`/`31967`/`31968`, `6961`, and `7961` events are startup migration inputs only. Desired-state metadata is additive on the canonical observable contract and does not revive those legacy live subscriptions.

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
