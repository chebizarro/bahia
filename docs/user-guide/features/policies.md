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

### CLI

```bash
bahia policies create \
  --name "require-sbom" \
  --type sbom \
  --rule require_sbom=true \
  --rule max_critical_vulns=0 \
  --environment production
```

### MCP Tool

```json
{
  "tool": "bahia_policy_create",
  "arguments": {
    "name": "require-sbom",
    "type": "sbom",
    "rules": {
      "require_sbom": true,
      "max_critical_vulns": 0
    }
  }
}
```

### Nostr (Signer-First)

```json
{
  "kind": 5986,
  "content": {
    "name": "require-sbom",
    "type": "sbom",
    "rules": {
      "require_sbom": true
    }
  },
  "tags": [
    ["t", "policy-create"]
  ]
}
```

## Policy Rules

### SBOM Rules

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

```bash
bahia policies evaluate \
  --artifact-id art-123 \
  --environment production
```

```json
{
  "tool": "bahia_policy_evaluate",
  "arguments": {
    "artifact_id": "art-123",
    "environment_id": "env-prod"
  }
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

### CLI

```bash
bahia policies update require-sbom \
  --rule max_critical_vulns=0 \
  --rule max_high_vulns=3
```

### Nostr

```json
{
  "kind": 5987,
  "content": {
    "policy_id": "policy-123",
    "rules": {
      "max_high_vulns": 3
    }
  }
}
```

## Deleting Policies

```bash
bahia policies delete require-sbom
```

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

| Kind | d-tag | Content |
|------|-------|---------|
| 31970 | `policy_id` | Policy registry |

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
