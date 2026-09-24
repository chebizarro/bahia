# Packages

**Packages** in Bahia provide package repository management for distributing software artifacts beyond container images.

## Overview

Package features include:
- **Repository management** — Define package repositories
- **Package publishing** — Upload and version packages
- **Promotion workflow** — Move packages between stages
- **Drift detection** — Ensure repository consistency

## Authenticated package mutations

`package/publish`, `package/promote`, `package/yank`, and `package/drift-detect`
execute through signed plain or wrapped ContextVM requests. Every method requires
a configured, nonempty fleet-operator allowlist and an `idempotency_key` (or
`_meta.progressToken`). Reusing that key with changed parameters is rejected.
Durable request claims prevent re-execution across transport restarts, including
when a backend change succeeds but publication or completion persistence fails.
An interrupted request is not automatically retried: inspect the backend and
canonical state before submitting a new intent.

### Two-person approval

When repository policy requires publish/promotion approval, a **different**
allowlisted operator signs a `package/approve-plan` ContextVM request:

```json
{
  "jsonrpc": "2.0",
  "id": "review-package",
  "method": "package/approve-plan",
  "params": {
    "idempotency_key": "review-unique-key",
    "requester_pubkey": "<publishing operator's hex public key>",
    "method": "package/publish",
    "params": {
      "repository_id": "<repository UUID>",
      "package_name": "utils",
      "version": "1.2.3",
      "filename": "utils.tgz",
      "source_url": "https://packages.example.com/utils.tgz",
      "sha256": "<64 lowercase hex characters>",
      "size_bytes": 1234
    }
  }
}
```

The server builds the plan hash from the action and current repository/artifact
snapshots, including policy, digest, source and target identities and event
revisions. It records the authenticated approver and returns `approval_id` and
`expires_at`; this does not upload or promote anything. The requester submits the
same action with that `approval_id` and a fresh mutation `idempotency_key`.
Approvals expire after ten minutes, require the approver still to be allowlisted,
and are consumed atomically before the backend attempt. Failed attempts do not
restore approval. Changed requests or resource snapshots require a new approval.

`approved_by` is not evidence and is rejected. Use raw ContextVM for this approval
workflow; the existing CLI/MCP string approval arguments do not grant approval.
The command publisher supports `approval_id`, but the CLI/MCP wrappers do not yet
expose that field.

### Yank versus deprecation

`package/yank` with `deprecated: true` only publishes advisory deprecation metadata
for an available artifact. It preserves bytes, download URL, digest and available
status; package-manager-native deprecation messages are not claimed. With
`deprecated: false` (the default), yank deliberately removes backend bytes and
publishes a deleted artifact. Use deprecation when consumers must retain access.

### Completion and observation

Artifact and promotion registries use signed `30900` state. Drift observations use
`30315` status and are not duplicate RPC results. Terminal package results use
`30900`, `schema=bahia.result.package.v1`, `d=package:intent:<intent UUID>`, after
intent persistence; the transport emits exactly one correlated JSON-RPC response.
No relay acceptance, a rejected projection write, or a persistence error produces
an error response rather than success. Partial backend changes are not rolled back.
Migration `000069_package_authorization` adds authoritative local approval and
admission records: preserve them across relay-projection rebuilds.

Repository apply/delete are not registered by this handler group. Their existing
publishers do not establish server reachability.

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
bahia package repo apply \
  --name internal-npm \
  --format npm \
  --backend-ref nexus-main \
  --backend-type nexus
```

The equivalent MCP tool is `bahia_package_repository_apply`; its required fields are `name`, `format`, and `backend_ref`.

### Repository Types

| Type | Description |
|------|-------------|
| `npm` | Node.js packages |
| `pypi` | Python packages |
| `conan` | C/C++ packages |
| `deb` | Debian packages |
| `rpm` | RPM packages |
| `pub` | Dart/Flutter packages |
| `go_modules` | Go modules |
| `gradle` | Gradle artifacts |

Registered backend types are `nexus`, `pulp`, and `filesystem_mock`.

### Listing repositories

Use the Packages UI or `bahia_package_list`.

### Deleting a repository

```bash
bahia package repo delete --name internal-npm
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
bahia package upload \
  --repository internal-npm \
  --package "@company/utils" \
  --version "1.2.3" \
  --file ./utils-1.2.3.tgz
```

### MCP tool

In an embedding that configures external MCP authorization, use `bahia_package_upload` with destination `repository_name` (or `repository_id`) plus required `package_name`, `version`, `filename`, `source_url`, `sha256`, and `size_bytes`. It publishes the signed package intent and returns correlation metadata; follow canonical observables for completion.

## Package Promotion

Move packages between stages:

### Promoting Package

```bash
bahia package promote \
  --source-repository internal-npm \
  --target-repository stable-npm \
  --package "@company/utils" \
  --version "1.2.3" \
  --filename utils-1.2.3.tgz
```

The equivalent MCP tool is `bahia_package_promote`. Supply source `repository_name` (or `repository_id`) and required `target_repository_name`, `package_name`, `version`, and `filename`.

### Yanking Package

Remove a package version:

```bash
bahia package yank \
  --repository internal-npm \
  --package "@company/utils" \
  --version "1.2.3" \
  --filename utils-1.2.3.tgz \
  --reason "Security vulnerability"
```

The equivalent MCP tool is `bahia_package_yank`.

## Package SBOMs

Package subjects can have generated or imported SBOM manifests just like artifacts. Use ContextVM `sbom/generate` or `sbom/import` with either an explicit package subject digest or the canonical immutable package artifact locator. The locator resolves the subject digest from Bahia's package projection only when the projected artifact SHA-256 matches the request:

```json
{
  "jsonrpc": "2.0",
  "id": "sbom-package-utils-1.2.3",
  "method": "sbom/generate",
  "params": {
    "idempotencyKey": "sbom-package-utils-1.2.3",
    "subject": { "type": "package" },
    "subjectLocator": {
      "package": {
        "repository_id": "<package-repository-uuid>",
        "namespace": "@company",
        "package_name": "utils",
        "version": "1.2.3",
        "filename": "utils-1.2.3.tgz",
        "sha256": "<package-archive-sha256>"
      }
    },
    "source": { "kind": "archive", "locator": "packages/internal-npm/@company/utils/-/utils-1.2.3.tgz" },
    "formats": ["cyclonedx"],
    "generator": "auto",
    "storage": "blossom"
  }
}
```

Bahia stores the payload on Blossom and publishes a `30078` SBOM reference plus a subject-scoped `30004` availability list. Package subject digest resolution is never inferred from package names alone; include `repository_id`, `package_name`, `version`, `filename`, and `sha256` in `subjectLocator.package`, or provide `subject.digest` directly.

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

### CLI and MCP

The CLI package group publishes mutations but does not register list/get/version commands. Use the web UI or the `bahia_package_list` and `bahia_package_get` MCP tools.

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
bahia package drift --repository internal-npm
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
3. **Choose the right operation** — Deprecate to retain access; yank to remove bytes
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
