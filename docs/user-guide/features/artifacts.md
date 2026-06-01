# Artifacts

An **Artifact** is an immutable container image registered with Bahia — the unit of deployment.

## Overview

Artifacts represent:
- Container images with digests
- Build provenance (which CI run produced it)
- Metadata (git commit, timestamp, labels)
- SBOM (Software Bill of Materials)
- Signatures (provenance attestations)

## Artifact Properties

| Property | Description | Required |
|----------|-------------|----------|
| `image` | Container image reference | Yes |
| `digest` | Image digest (sha256) | Yes |
| `service_id` | Associated service | Yes |
| `build_id` | Source build | No |
| `metadata` | Custom key-value pairs | No |

## Registering Artifacts

### From CI (Recommended)

Use the Hive-CI bridge or webhooks to auto-register:

```yaml
# In CI workflow
- name: Register artifact
  run: |
    curl -X POST "$BAHIA_URL/api/v1/artifacts" \
      -H "Authorization: Nostr $NIP98_TOKEN" \
      -d '{
        "service_id": "'"$SERVICE_ID"'",
        "image": "registry.example.com/my-api:'"$TAG"'",
        "digest": "'"$DIGEST"'",
        "metadata": {
          "git_commit": "'"$GITHUB_SHA"'",
          "build_timestamp": "'"$(date -Iseconds)"'"
        }
      }'
```

### CLI

```bash
bahia artifacts register \
  --service-id svc-123 \
  --image "registry.example.com/my-api:v2.0.0" \
  --digest "sha256:abc123..." \
  --metadata git_commit=abc123
```

### MCP Tool

```json
{
  "tool": "bahia_artifact_register",
  "arguments": {
    "service_id": "svc-123",
    "image": "registry.example.com/my-api:v2.0.0",
    "digest": "sha256:abc123..."
  }
}
```

### Nostr (Signer-First)

Publish a `5985` ArtifactRegister event:

```json
{
  "kind": 5985,
  "content": {
    "service_id": "svc-123",
    "image": "registry.example.com/my-api:v2.0.0",
    "digest": "sha256:abc123..."
  },
  "tags": [
    ["service", "svc-123"],
    ["digest", "sha256:abc123..."]
  ]
}
```

## Viewing Artifacts

### Web UI

1. Go to **Artifacts** in the sidebar
2. Browse all artifacts or filter by service
3. Click an artifact to see:
   - Image details
   - Build provenance
   - SBOM packages
   - Signatures

Or from a service:
1. Go to **Services** → select service
2. Click **Artifacts** tab

### CLI

```bash
# List artifacts for a service
bahia artifacts list --service-id svc-123

# Get artifact details
bahia artifacts get art-456

# Output as JSON
bahia artifacts get art-456 -o json
```

### MCP Tool

```json
{
  "tool": "bahia_list_artifacts",
  "arguments": {
    "service_id": "svc-123"
  }
}
```

## SBOM (Software Bill of Materials)

Bahia can store and query SBOMs for artifacts.

### Ingesting SBOM

```bash
# Generate SBOM with Syft
syft registry.example.com/my-api:v2.0.0 -o spdx-json > sbom.json

# Upload to Bahia
curl -X POST "$BAHIA_URL/api/v1/artifacts/$ARTIFACT_ID/sbom" \
  -H "Content-Type: application/json" \
  -d @sbom.json
```

### Viewing SBOM

```bash
# Get SBOM
bahia artifacts sbom art-456

# List packages
bahia artifacts sbom art-456 --packages

# Search for vulnerable packages
bahia sbom search --package "log4j" --version "<2.17.0"
```

### Web UI

1. Go to artifact detail
2. Click **SBOM** tab
3. View package list and search

## Signatures

Artifacts can have cryptographic signatures for provenance.

### Viewing Signatures

```bash
# List signatures
bahia artifacts signatures art-456

# Check verification status
bahia artifacts signatures art-456 --verified
```

### Verifying Signatures

```bash
bahia artifacts verify art-456
```

This:
1. Discovers signatures from registries (Cosign, Notation)
2. Verifies against configured trust roots
3. Stores verification results
4. Returns verification status

### MCP Tool (Encrypted)

```json
{
  "tool": "bahia_verify_artifact_signatures",
  "arguments": {
    "artifact_id": "art-456"
  }
}
```

## Artifact Metadata

Store custom metadata with artifacts:

```yaml
metadata:
  git_commit: "abc123def"
  git_branch: "main"
  build_timestamp: "2024-01-15T10:30:00Z"
  ci_job_url: "https://ci.example.com/job/123"
  tested: "true"
  coverage: "85%"
```

Query by metadata:

```bash
bahia artifacts list --service-id svc-123 --metadata tested=true
```

## Builds and Artifacts

**Builds** are CI workflow executions that produce artifacts.

```
Build (CI run) → produces → Artifact (container image)
```

### Registering Builds

Builds are typically auto-registered via CI integration:

```bash
bahia builds register \
  --service-id svc-123 \
  --workflow-id "ci-run-456" \
  --commit-sha "abc123" \
  --branch "main" \
  --status "completed"
```

### Linking to Artifacts

When registering an artifact, link to its build:

```bash
bahia artifacts register \
  --service-id svc-123 \
  --build-id build-456 \
  --image "registry.example.com/my-api:v2.0.0" \
  --digest "sha256:abc123..."
```

## Read Models

Artifact state is published as Nostr events:

| Kind | d-tag | Content |
|------|-------|---------|
| 31966 | `artifact_id` | Artifact registry entry |
| 31969 | `build_id` | Build registry entry |

## OCI Registry Integration

Bahia includes an OCI Distribution API (`/v2/`) that can serve as your container registry.

### Pushing Images

```bash
# Tag and push
docker tag my-api:latest bahia.example.com/my-api:v2.0.0
docker push bahia.example.com/my-api:v2.0.0
```

### Authentication

- **NIP-98**: Nostr-signed HTTP auth
- **Basic Auth**: Service account credentials
- **Anonymous**: For allowed CIDRs (pull only)

### Benefits

- Integrated artifact registration
- Automatic SBOM generation
- Unified access control
- Blossom-backed blob storage

## Best Practices

1. **Use digests, not just tags** — Tags can change, digests cannot
2. **Include build metadata** — Git commit, timestamp, CI job URL
3. **Generate SBOMs** — Enable vulnerability scanning
4. **Sign artifacts** — Prove provenance
5. **Prune old artifacts** — Manage storage costs

## Troubleshooting

### "Artifact not found"

- Verify the artifact ID is correct
- Check the artifact was successfully registered
- Ensure you have access to the service

### "Digest mismatch"

- The image may have been modified
- Re-push with a new tag
- Never modify existing images

### "SBOM ingestion failed"

- Check SBOM format (SPDX or CycloneDX)
- Verify JSON is valid
- Check artifact exists

## Related

- [Services](services.md) — Artifact owners
- [Deployments](deployments.md) — Deploying artifacts
- [Policies](policies.md) — SBOM requirements
