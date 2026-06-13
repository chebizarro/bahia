# Packages

**Packages** in Bahia provide package repository management for distributing software artifacts beyond container images.

## Overview

Package features include:
- **Repository management** — Define package repositories
- **Package publishing** — Upload and version packages
- **Promotion workflow** — Move packages between stages
- **Drift detection** — Ensure repository consistency

## Key Concepts

### Package Repository

A **Repository** hosts packages:

```yaml
name: "internal-npm"
type: "npm"
url: "https://npm.example.com"
upstream: "https://registry.npmjs.org"
```

### Package

A **Package** is a versioned artifact:

```yaml
repository: "internal-npm"
name: "@company/utils"
version: "1.2.3"
checksum: "sha256:..."
```

### Package Stage

Packages can move through stages:
- **unstable** — Development/testing
- **testing** — QA/integration
- **stable** — Production ready

## Managing Repositories

### Creating Repository

**CLI:**
```bash
bahia packages repositories create \
  --name "internal-npm" \
  --type npm \
  --url "https://npm.example.com"
```

**MCP Tool:**
```json
{
  "tool": "bahia_package_repository_apply",
  "arguments": {
    "name": "internal-npm",
    "type": "npm",
    "config": {
      "url": "https://npm.example.com"
    }
  }
}
```

### Repository Types

| Type | Description |
|------|-------------|
| `npm` | Node.js packages |
| `pypi` | Python packages |
| `maven` | Java/Maven artifacts |
| `cargo` | Rust crates |
| `apt` | Debian packages |
| `rpm` | RPM packages |
| `helm` | Helm charts |

### Listing Repositories

```bash
bahia packages repositories list
```

### Deleting Repository

```bash
bahia packages repositories delete internal-npm
```

```json
{
  "tool": "bahia_package_repository_delete",
  "arguments": {
    "name": "internal-npm"
  }
}
```

## Publishing Packages

### CLI

```bash
bahia packages publish \
  --repository internal-npm \
  --name "@company/utils" \
  --version "1.2.3" \
  --file ./utils-1.2.3.tgz
```

### MCP Tool

```json
{
  "tool": "bahia_package_publish",
  "arguments": {
    "repository": "internal-npm",
    "name": "@company/utils",
    "version": "1.2.3"
  }
}
```

### Nostr Event

```json
{
  "kind": 5xxx,
  "content": {
    "repository": "internal-npm",
    "name": "@company/utils",
    "version": "1.2.3"
  },
  "tags": [
    ["repository", "internal-npm"],
    ["package", "@company/utils"],
    ["version", "1.2.3"]
  ]
}
```

## Package Promotion

Move packages between stages:

### Promoting Package

```bash
bahia packages promote \
  --repository internal-npm \
  --name "@company/utils" \
  --version "1.2.3" \
  --to stable
```

```json
{
  "tool": "bahia_package_promote",
  "arguments": {
    "repository": "internal-npm",
    "name": "@company/utils",
    "version": "1.2.3",
    "target_stage": "stable"
  }
}
```

### Yanking Package

Remove a package version:

```bash
bahia packages yank \
  --repository internal-npm \
  --name "@company/utils" \
  --version "1.2.3" \
  --reason "Security vulnerability"
```

```json
{
  "tool": "bahia_package_yank",
  "arguments": {
    "repository": "internal-npm",
    "name": "@company/utils",
    "version": "1.2.3",
    "reason": "Security vulnerability"
  }
}
```

## Package SBOMs

Package subjects can have generated or imported SBOM manifests just like artifacts. Use ContextVM `sbom/generate` or `sbom/import` with a package subject and a stable package digest:

```json
{
  "jsonrpc": "2.0",
  "id": "sbom-package-utils-1.2.3",
  "method": "sbom/generate",
  "params": {
    "idempotencyKey": "sbom-package-utils-1.2.3",
    "subject": {
      "type": "package",
      "id": "pkg:npm/@company/utils@1.2.3",
      "digest": "sha256:<package-archive-digest>"
    },
    "source": { "kind": "archive", "locator": "packages/internal-npm/@company/utils/-/utils-1.2.3.tgz" },
    "formats": ["cyclonedx"],
    "generator": "auto",
    "storage": "blossom"
  }
}
```

Bahia stores the payload on Blossom and publishes a `30078` SBOM reference plus a subject-scoped `30004` availability list. Package subject digest resolution is not inferred from package names alone; include the content digest in the request.

## Viewing Packages

### Web UI

Navigate to **Packages** in the sidebar:
- Browse repositories
- View package versions
- Check promotion status

Click a package to see:
- Version history
- Checksums
- Stage/promotion status

### CLI

```bash
# List packages in repository
bahia packages list --repository internal-npm

# Get package details
bahia packages get internal-npm/@company/utils

# List versions
bahia packages versions --repository internal-npm --name "@company/utils"
```

### MCP Tool

```json
{
  "tool": "bahia_package_list",
  "arguments": {
    "repository": "internal-npm"
  }
}
```

## Drift Detection

Detect inconsistencies in package state:

```bash
bahia packages drift-detect --repository internal-npm
```

```json
{
  "tool": "bahia_package_drift_detect",
  "arguments": {
    "repository": "internal-npm"
  }
}
```

Drift is detected when:
- Expected packages missing
- Checksum mismatches
- Stage inconsistencies

## Upstream Proxying

Repositories can proxy upstream registries:

```yaml
repository:
  name: "npm-proxy"
  type: "npm"
  upstream:
    url: "https://registry.npmjs.org"
    cache_ttl: 3600
```

Benefits:
- Cache packages locally
- Survive upstream outages
- Audit package usage

## Best Practices

1. **Use semantic versioning** — Clear version progression
2. **Promote through stages** — Test before stable
3. **Yank bad versions** — Don't delete, mark unavailable
4. **Document packages** — Include README and changelog
5. **Monitor drift** — Ensure consistency

## Troubleshooting

### Publish Failed

- Check repository credentials
- Verify package format
- Check version doesn't exist

### Promotion Failed

- Verify package exists
- Check stage requirements
- Review promotion policies

### Drift Detected

- Compare expected vs actual
- Re-sync if needed
- Investigate cause

## Related

- [Artifacts](artifacts.md) — Container artifacts
- [Policies](policies.md) — Package policies
- [Services](services.md) — Package consumers
