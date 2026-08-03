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

## Deployment SBOMs

Deployment subjects can be covered by SBOM manifests when the deployment intent has a stable desired-state hash. Bahia uses that desired hash as the deployment subject digest, then follows the same canonical SBOM flow as artifacts and packages: generate/import payload bytes, store them on Blossom, publish a `30078` SBOM reference, publish/replace the deployment-scoped `30004` availability list, and emit `30315` status plus `4903` audit observables.

```json
{
  "jsonrpc": "2.0",
  "id": "sbom-deployment-intent-123",
  "method": "sbom/generate",
  "params": {
    "idempotencyKey": "sbom-deployment-intent-123",
    "subject": { "type": "deployment", "id": "intent-123" },
    "source": { "kind": "directory", "locator": "<rendered-desired-state-dir>" },
    "formats": ["spdx"],
    "generator": "syft",
    "storage": "blossom"
  }
}
```

If the deployment intent has no desired-state hash, provide an explicit `subject.digest` or resolve the deployment state first.

## Creating Deployments

### Web UI

For a Compose service:

1. Go to **Services** and select the service, then click **Deploy**.
2. Select the environment, an explicit deployment unit when the environment is ambiguous, and a registered artifact with a full immutable `sha256` digest.
3. Enter the Compose service name, port mappings, command arguments, and literal non-secret environment values.
4. Select service secrets by opaque reference and choose the environment variable name for each. Secret values are never placed in the signed payload or desired-state preview.
5. Configure the HTTP `GET` healthcheck, restart policy, volumes, and CPU/memory limits.
6. Review the backend-canonical non-secret desired-state diff, exact JSON, SHA-256 hash, policy result, and cost estimate.
7. Click **Sign & submit idempotently**. The browser signer first persists the reviewed managed configuration with `service/update`, then signs `service/deploy` with that exact displayed hash as `expected_desired_state_hash` and its idempotency key.

For an Arcana-ready deployment, operators can enter a `8080:8080` port mapping and enable `GET /healthz` on port `8080`; these are operator-entered values, not product-specific defaults in the generic wizard.

Bahia rebuilds the desired state after the signed update and rejects the deploy if its hash differs from the reviewed hash. Passing policy continues through the existing protected-environment approval flow; policy blockers prevent submission.

For a single explicit unit, the wizard selects its durable ID automatically. For multiple units, even the environment default is not auto-selected: the operator must choose a unit explicitly. The browser shows only the unit's `endpoint_ref` alias; it never resolves or displays the Docker host, TLS certificate paths, keys, or credentials. Runtime conflicts, missing Bahia-managed ownership, missing endpoint aliases, missing durable unit IDs, and mutable or unregistered artifacts block preview and intent creation with a clear error.


### CLI and MCP

Deployment intent creation is signer-first. CLI, MCP, web, and agent flows use ContextVM JSON-RPC methods over Nostr kind `25910` (or encrypted `1059`/`21059` wrappers) and then follow canonical observables for durable progress. Transitional REST `POST /api/v1/deployments/intents` is available when a control-plane command publisher is configured; it publishes the same signed `service/deploy` command, requires relay `OK` acceptance through the publisher receipt, and returns `202` command metadata instead of a synchronous deployment-intent domain object.

### Nostr (ContextVM)

The browser obtains its authoritative review through signed `service/deploy-preview`. That method accepts the selected IDs plus `managed_runtime_config` and returns the exact sanitized `current_desired_state`, proposed `desired_state`, `desired_state_hash`, and policy evaluation. It does not persist or apply runtime state.

After signed `service/update` persists the same normalized managed configuration, publish a ContextVM `service/deploy` request as kind `25910` or inside an encrypted `1059`/`21059` wrapper:

```json
{
  "kind": 25910,
  "content": "{\"jsonrpc\":\"2.0\",\"id\":\"deploy-svc-123-env-456\",\"method\":\"service/deploy\",\"params\":{\"service_id\":\"svc-123\",\"environment_id\":\"env-456\",\"deployment_unit_id\":\"unit-max\",\"artifact_id\":\"art-789\",\"_meta\":{\"progressToken\":\"deploy-svc-123-env-456\"}}}",
  "tags": [
    ["p", "<bahia-service-pubkey>"],
    ["method", "service/deploy"],
    ["service", "svc-123"],
    ["environment", "env-456"],
    ["unit", "unit-max"],
    ["artifact", "art-789"]
  ]
}
```

Require relay `OK` with `accepted=true`. Treat the ContextVM response as receipt only; deployment completion comes from canonical observables.

## Monitoring Deployments

### Web UI

The **Deployments** page is linkable at `/deployments/<intent-id>`. One aggregate follows the signed request without leaving the deployment UI and shows:

- policy decision and approval/rejection status;
- explicit deployment-unit key and safe endpoint alias;
- immutable artifact digest and reviewed desired-state hash;
- persisted execution phases and links to each run's redacted stdout/stderr;
- safe failure code/message, runtime health, observed digest/hash, drift, reconciliation time, and completion; and
- the explicit previous healthy artifact used by rollback.

Relay updates may arrive late or repeat after reconnect. The dashboard merges intent, run, and service/environment projections by logical identity and domain `updated_at`, with relay timestamp and event ID as deterministic tie-breakers. Corrected service-state coordinates include both service and environment, and logical tombstone watermarks prevent stale replay from resurrecting deleted state.

### CLI

The current CLI does not register `bahia deployments list`, `bahia deployments get`, or `bahia deployments logs` commands. Use `bahia state list` / `bahia state drifted` for current state views and `bahia logs run <run-id>` for run logs.

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

Approval and rejection mutations are signer-first ContextVM intents. CLI/MCP mutation surfaces publish signed requests and return Nostr correlation receipts; if no signer-first publisher is configured, MCP fails closed instead of mutating the registry directly. Use `approval/approve` or `approval/reject` and follow canonical status/audit/state observables.

### Nostr

Publish a ContextVM approval request:

```json
{
  "kind": 25910,
  "content": "{\"jsonrpc\":\"2.0\",\"id\":\"approve-intent-123\",\"method\":\"approval/approve\",\"params\":{\"intent_id\":\"intent-123\",\"decision\":\"approve\",\"_meta\":{\"progressToken\":\"approve-intent-123\"}}}",
  "tags": [
    ["p", "<bahia-service-pubkey>"],
    ["method", "approval/approve"],
    ["intent", "intent-123"]
  ]
}
```

## Rollbacks

Roll back to a previous artifact:

### Web UI

1. Open the failed deployment's linkable detail page.
2. Review the displayed prior deployed artifact and immutable digest.
3. Click **Rollback**. Bahia creates a fresh desired-state intent for that explicit artifact, evaluates current policy, and requests approval again when the environment is protected.

### Nostr

Rollback is signer-first. The legacy `POST /api/v1/rollback` REST mutation has been removed; publish a ContextVM `service/rollback` intent:

```json
{
  "kind": 25910,
  "content": "{\"jsonrpc\":\"2.0\",\"id\":\"rollback-svc-123-env-456\",\"method\":\"service/rollback\",\"params\":{\"service_id\":\"svc-123\",\"environment_id\":\"env-456\",\"deployment_unit_id\":\"unit-max\",\"target_artifact_id\":\"art-previous\",\"supersedes_intent_id\":\"intent-failed\",\"_meta\":{\"progressToken\":\"rollback-svc-123-env-456\"}}}",
  "tags": [
    ["p", "<bahia-service-pubkey>"],
    ["method", "service/rollback"],
    ["service", "svc-123"],
    ["environment", "env-456"],
    ["unit", "unit-max"],
    ["artifact", "art-previous"],
    ["intent", "intent-failed"]
  ]
}
```

The target must be an explicit, previously successful artifact for the same service, environment, and deployment unit. This creates a new canonical desired-state intent; it never reuses the legacy force-approved rollback path.

### MCP

Use the same signer-first `service/rollback` ContextVM payload with an explicit target. Do not use registry helpers that infer history or force approval.

## Rollout safety and verified rollback

Canary and blue/green strategies require a runtime traffic controller. Bahia fails the rollout when the runtime cannot shift traffic, or when a follow-up read does not confirm the requested canary weight, blue/green primary slot, or restored rollback target. It does not treat an in-memory percentage change as production traffic movement.

Health gates evaluate consecutive unhealthy observations. Observer errors count as unhealthy samples and fail the rollout when the configured failure threshold is reached; an unavailable observer is not converted into a healthy result.

The rollout plan records the previous artifact and traffic state before it begins. If rollback is required, Bahia restores the previous artifact, restores or switches traffic to the previous primary, observes runtime health and artifact identity, performs cleanup, and persists the final state. A run is marked `rolled_back` only after that verification and persistence succeed. A failed verification publishes `rollout.rollback_failed` and leaves an explicit failure instead of claiming recovery.

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

1. Open a deployment and select **View phases & logs** for a run.
2. Select stdout or stderr after the run reaches a terminal state.
3. Bahia decrypts all retained versions of the desired state's referenced secrets and redacts them from both complete streams before applying tail limits or stream selection. If redaction dependencies or retained versions are unavailable, log retrieval fails closed.

### CLI

```bash
bahia logs run run-456 --tail 100
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

Use the service detail page and the Deployments UI for deployment history. The current CLI does not register `bahia deployments list` history commands.

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
