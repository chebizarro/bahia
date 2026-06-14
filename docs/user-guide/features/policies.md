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
description: "Require SBOM for production deployments"
type: "sbom"
rules:
  - require_sbom: true
  - max_critical_vulns: 0
environments:
  - "production"
```

### Policy Types

| Type | Description |
|------|-------------|
| `approval` | Manual approval requirements |
| `sbom` | SBOM and vulnerability rules |
| `signature` | Artifact signature requirements |
| `custom` | Custom rule expressions |

### Evaluation Result

```yaml
policy_id: "require-sbom"
artifact_id: "art-123"
passed: true
violations: []
evaluated_at: "2024-01-15T10:00:00Z"
```

## Creating Policies

### Web UI

1. Navigate to **Policies** in the sidebar
2. Click **New Policy**
3. Configure:
   - **Name**: Policy identifier
   - **Type**: Policy type
   - **Rules**: Specific requirements
   - **Environments**: Where to apply
4. Click **Create**

### CLI and MCP

Policy creation is signer-first. CLI and MCP mutation surfaces publish a signed public `PolicyCreate` event (`kind: 5986`), verify relay `OK` acceptance, and return correlation metadata. Durable truth comes from following the returned `request_event_id` in ContextVM reply events (`kind: 25910`) and policy read-model projections (`kind: 30900`). Direct REST/repository mutation fallback is not used for policy creation.

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

```yaml
rules:
  require_sbom: true
  max_critical_vulns: 0
  max_high_vulns: 5
  banned_packages:
    - "log4j<2.17.0"
    - "lodash<4.17.21"
  required_licenses:
    - "MIT"
    - "Apache-2.0"
  banned_licenses:
    - "GPL-3.0"
```

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

### Approval Rules

```yaml
rules:
  require_approval: true
  min_approvers: 2
  approver_pubkeys:
    - "npub1admin..."
  auto_approve_if:
    - "no_vulnerabilities"
    - "tests_passed"
```

### Signature Rules

```yaml
rules:
  require_signature: true
  trusted_keys:
    - "cosign-key-1"
  signature_types:
    - "cosign"
    - "notation"
```

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

Navigate to **Policies** in the sidebar:
- View all policies
- See which environments they apply to
- Check recent evaluations

Click a policy to see:
- **Rules**: Policy configuration
- **Environments**: Where applied
- **History**: Recent evaluations

### CLI

```bash
# List policies
bahia policies list

# Get policy details
bahia policies get require-sbom

# Show policy as YAML
bahia policies get require-sbom -o yaml
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

**Note**: Policies linked to active deployments cannot be deleted.

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

- Verify environment association
- Check policy is enabled
- Review policy priority

## Related

- [Artifacts](artifacts.md) — Policy targets
- [Deployments](deployments.md) — Policy enforcement
- [Environments](environments.md) — Policy scope
