# Policies

**Policies** in Bahia define rules for deployment approval, SBOM requirements, and operational governance.

## Overview

Policy features include:
- **Deployment policies** — Approval requirements
- **SBOM policies** — Software composition rules
- **Evaluation** — Check artifacts against policies
- **Enforcement** — Block non-compliant deployments

## Key Concepts

### Policy

A **Policy** defines rules:

```yaml
name: "require-sbom"
environment_id: "<env-uuid>"   # omit for a global policy
enforcement: "block"            # block or warn
enabled: true
rules:
  - type: require_sbom
  - type: max_critical_vulns
    params: { max: 0 }
```

A policy has no separate "type"; each rule carries its own `type` and optional `params`.

### Rule Types

| Rule `type` | Purpose |
|-------------|---------|
| `require_sbom` | Artifact must have an SBOM |
| `require_signature` | Artifact must have a verified signature |
| `max_critical_vulns`, `max_high_vulns` | Vulnerability thresholds (`params.max`) |
| `require_scan_status` | Require a scan status (`params.status`) |
| `security_osv_scan` | Security OSV freshness/outcome gate |
| `block_package` | Block a package (`params.package`) |
| `sbom_subject_digest_match`, `sbom_parseability`, `sbom_ntia_min_fields`, `sbom_trusted_generator`, `sbom_format` | SBOM quality gates |
| `package_min_age`, `package_min_downloads`, `typosquat_check` | Package supply-chain gates (tool provisioning) |
| `require_approval` | Approval gate: matching deployment intents wait for manual approval (see [Approval](#approval)) |

### Evaluation Result

```yaml
policy_id: "<policy-uuid>"
policy_name: "require-sbom"
passed: true
enforcement: "block"
violations: []
requires_approval: false   # true when the policy contains a require_approval rule
```

The aggregate evaluation also carries `requires_approval: true` when any applicable policy contains a `require_approval` rule, so deploy previews show that the intent will wait for approval.

## Config Fabric operator console

Bahia retains config desired-state and `cascadia.config.status.v1` events in its Nostr event store, so drift remains available after restart or while relays are unavailable.

Open **Config Fabric** in the **Admin** navigation section. The console lists every managed service, policy, and scope with:

- latest desired event ID and version;
- latest applied event ID and effective version;
- in-sync or drifted status;
- the latest safe rejection reason.

Select a service policy to open its detail view. The detail view shows the retained desired policy or list and the policy represented by the latest applied event, status/audit history, and all retained desired versions. Event IDs and versions identify the exact signed records used for each view.

To publish a change:

1. Select **Publish Config** from the list, or **Publish New Version** from a policy detail.
2. Choose NIP-78 structured policy or NIP-51 membership list.
3. Enter the service, policy, scope, next positive version, and matching `cascadia.config.<policy-name>.v1` schema.
4. Enter a JSON policy object, or NIP-51 `p`, `a`, and `r` list items.
5. If credentials are required, enter only schema-defined references using the `signet`, `file`, or `service` provider.
6. Select **Publish Config**. The console checks field shape, schema/coordinate agreement, monotonic versioning against the current drift view, list item format, secret reference shape, and secret-looking policy content before calling the API.

Raw passwords, tokens, private keys, and credentials must never be entered. The editor rejects secret-bearing field names and recognizable secret values; use references instead.

To roll back, open a policy detail, select **Rollback** beside a prior version, and approve the confirmation dialog. Bahia republishes the selected retained payload as a new monotonically higher version; the historical event is not modified or deleted.

## Config Fabric operator API

### Publish desired state

`POST /api/v1/config-fabric/events` requires policy write permission. The server signs with the configured operator Signet/NIP-46 identity; it does not accept a raw signing key in the request.

```json
{
  "kind": 30078,
  "service_id": "khatru-relay",
  "policy_name": "rate-limits",
  "scope": "prod",
  "version": 42,
  "schema": "cascadia.config.rate-limits.v1",
  "policy": {"query": {"max_limit": 500}}
}
```

For a NIP-51 named people list, use kind `30000`, schema `cascadia.config.membership.v1`, and `items` such as `{"tag":"p","value":"<64-hex-pubkey>"}`. Policy content that looks like a private key, token, password, or other raw secret is rejected. Use schema-defined `secret_refs` with `signet`, `file`, or `service` providers.

### Reconcile managed relay configuration

Authenticated ContextVM kind-`25910` handlers expose:

- `config/reconcile` — compare the persisted desired coordinate with drift state and revalidate/reload the managed relay projection;
- `config/status` — return the managed target, discovered callable NIP-86 methods, effective schema/version, last applied event, health, reload time, rejection, and drift;
- `config/reload` — revalidate and activate only the already-persisted projection.

Each request requires `target_ref`, `service_id`, `scope`, and `policy_coordinate` such as `service:bahia-relay-sidecar:relay-sidecar`. Mutable policy values are not accepted in `config/reload`.

### View drift

`GET /api/v1/config-fabric/drift` returns the latest desired and applied event IDs and versions for each service, policy, and scope, plus `drift` and the latest safe rejection reason.

### Roll back

`POST /api/v1/config-fabric/rollback` with `{"event_id":"<prior-desired-event-id>"}` republishes that validated content and list membership at the next version. Bahia preserves every prior event for audit; rollback never deletes history.

## Creating Policies

### Web UI

1. Navigate to **Policies** in the sidebar
2. Click **Create Policy**
3. Configure:
   - **Name**: Policy identifier
   - **Rules**: Rule types and parameters (rule builder)
   - **Environment**: Optional scope (global when empty)
   - **Enforcement**: `warn` or `block`
4. Submit to publish the signed ContextVM `policy/create` request

### CLI and MCP

Policy creation is signer-first. The web UI publishes ContextVM `policy/create`/`policy/update`/`policy/delete` requests (kind `25910`). The CLI (`bahia policies create`) and MCP mutation surfaces instead publish signed public `PolicyCreate`/`PolicyUpdate`/`PolicyDelete`/`PolicyEvaluate` events (kinds `5986`-`5989`, a scoped compatibility exception to the legacy-kind policy), verify relay `OK` acceptance, and return correlation metadata. Durable truth comes from following the returned `request_event_id` in ContextVM reply events (`kind: 25910`) and policy read-model projections (`kind: 30900`). Direct REST/repository mutation fallback is not used for policy creation.

Read-only paths are distinct: listing and getting policies may read durable read models or server projections because they do not change policy semantics.

### Nostr (`PolicyCreate` 5986)

```json
{
  "kind": 5986,
  "content": {
    "name": "require-sbom",
    "rules": [{ "type": "require_sbom" }],
    "enforcement": "block",
    "enabled": true
  },
  "tags": [
    ["d", "policy-create-require-sbom"],
    ["policy_name", "require-sbom"]
  ]
}
```

## Policy Rules

### SBOM and Security OSV Rules

```json
{
  "rules": [
    { "type": "require_sbom" },
    { "type": "max_critical_vulns", "params": { "max": 0 } },
    { "type": "max_high_vulns", "params": { "max": 5 } },
    { "type": "block_package", "params": { "package": "log4j" } }
  ]
}
```

> **NOTE (2026-09-11):** earlier versions of this page showed `banned_packages`, `required_licenses`, and `banned_licenses` keys. Those are not rule types; license rules are not implemented.

Security OSV scan policy settings use the `security_osv_scan` rule. Deployment gates read the latest completed Security scan projection; they do not trigger a scan and wait for completion during deployment evaluation.

```json
{
  "rules": [
    { "type": "require_sbom" },
    {
      "type": "security_osv_scan",
      "params": {
        "enabled": true,
        "interval_seconds": 86400,
        "freshness_seconds": 604800,
        "no_scan": "block",
        "stale": "block",
        "failed": "block"
      }
    },
    { "type": "max_critical_vulns", "params": { "max": 0 } },
    { "type": "max_high_vulns", "params": { "max": 5 } }
  ],
  "enforcement": "block"
}
```

`max_critical_vulns`, `max_high_vulns`, and `require_scan_status` prefer latest successful Security OSV counts when present. If no Security scan exists, existing SBOM aggregate counts remain the compatibility fallback. Use policy `enforcement: "warn"` for warn-only deployments; use `no_scan`, `stale`, or `failed` set to `"pass"` only when that state is explicitly acceptable.

Scheduled rescans are repository-backed due records derived from enabled policy-scoped `security_osv_scan` rules. The scheduler runs once at startup and then on cadence ticks; it skips active duplicate scans and never waits on event delivery for completion.

### Approval

A deployment intent requires manual approval when **either** of these is true:

- the target environment is marked `protected`; or
- any enabled policy that applies to the deployment (a global policy, or one scoped to the target environment) contains a `require_approval` rule.

```json
{
  "name": "prod-approval",
  "environment_id": "<env-uuid>",
  "rules": [{ "type": "require_approval" }],
  "enforcement": "block"
}
```

`require_approval` is a gate, not a check: it never produces a violation or blocker, and it applies regardless of the policy's `enforcement` mode. Gated intents are created as `pending`, do not advance desired state, and proceed only after an explicit approve decision on the deployment intent (the same flow used for protected environments). Caller-supplied approval on creation is ignored. If Bahia cannot read the policy set when the intent is created, creation fails rather than skipping approval. Multi-approver counts, approver allowlists, and auto-approval conditions are not policy parameters.

### Signature Rules

```json
{ "rules": [ { "type": "require_signature" } ] }
```

Trust roots for signature verification are server configuration, not rule parameters.

## Evaluating Policies

### Manual Evaluation

Manual policy evaluation is signer-first. Publish a signed `5989` PolicyEvaluate event:

```json
{
  "kind": 5989,
  "content": {
    "artifact_id": "art-123",
    "environment_id": "env-prod"
  },
  "tags": [
    ["artifact", "art-123"],
    ["environment", "env-prod"]
  ]
}
```

### Automatic Evaluation

Policies are automatically evaluated:
1. When deployment intent is created
2. Before deployment approval
3. During deployment execution

### Evaluation Results

```yaml
evaluations:
  - policy: "require-sbom"
    passed: true
    details:
      sbom_present: true
      critical_vulns: 0
  - policy: "require-signature"
    passed: false
    violations:
      - "No valid signature found"
```

## Viewing Policies

### Web UI

Navigate to **Policies** in the sidebar to list policies with their scope and enforcement. Click a policy (`/policies/<id>`) to view and **Edit** its rules, environment scope, and enforcement.

### CLI

```bash
# List policies
bahia policies list

# Get policy details (by policy ID)
bahia policies get <policy-id>

# Show policy as YAML
bahia policies get <policy-id> -o yaml
```

## Updating Policies

### Web UI

1. Go to policy detail
2. Click **Edit**
3. Modify rules
4. Click **Save**

### Nostr

Policy updates are signer-first. Publish a signed `PolicyUpdate` event (`kind: 5987`) with `id` in content or the `policy` tag. Omitted fields are preserved by the reactor; an empty `environment_id` explicitly clears environment scoping.

```json
{
  "kind": 5987,
  "content": {
    "id": "policy-123",
    "enforcement": "warn"
  },
  "tags": [
    ["d", "policy-update-policy-123"],
    ["policy", "policy-123"]
  ]
}
```

## Deleting Policies

Policy deletion is signer-first. Publish a signed `PolicyDelete` event (`kind: 5988`) with the policy id in content or the `policy` tag. CLI/MCP return relay acceptance and follow metadata; they do not delete the repository row directly.


## Policy Enforcement

### Blocking Mode

Block deployments that violate policies:

```yaml
enforcement: "block"
```

Deployments that fail policy evaluation are rejected.

### Warning Mode

Allow deployments with warnings:

```yaml
enforcement: "warn"
```

Deployments proceed but violations are logged.

## Read Models

Policy state is published as Nostr events:

| Kind | Tags | Content |
|------|------|---------|
| `25910` | `method=policy/create|policy/update|policy/delete`, `policy`, requester `p` | ContextVM mutation request |
| `30900` | `d`, `domain=policy`, `policy` | Current policy registry/read model projection |
| `30315` | `status`, `policy`, correlation `e` | Policy mutation progress and terminal status |
| `4903` | requester `p`, `policy`, correlation `e` | Immutable audit/provenance facts |

## Best Practices

1. **Start with warnings** — Monitor before blocking
2. **Document policies** — Explain requirements
3. **Layer policies** — Different rules per environment
4. **Review evaluations** — Check for false positives
5. **Update regularly** — Keep vulnerability rules current

## Troubleshooting

### Deployment Blocked

- Check evaluation results
- Review specific violations
- Update artifact to comply

### False Positives

- Review policy rules
- Check SBOM accuracy
- Adjust thresholds

### Policy Not Applied

- Verify environment association (`environment_id`, or global when empty)
- Check policy is enabled

## Related

- [Artifacts](artifacts.md) — Policy targets
- [Deployments](deployments.md) — Policy enforcement
- [Environments](environments.md) — Policy scope
