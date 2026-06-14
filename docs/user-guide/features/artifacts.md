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

Artifact registration is signer-first. The legacy `POST /api/v1/artifacts` REST mutation has been removed; CI systems should publish a signed Nostr `ArtifactRegister` event or use the Hive-CI bridge.

### From CI (Recommended)

Use the Hive-CI bridge or publish the event directly from CI with your configured Nostr signer:

```json
{
  "kind": 5985,
  "content": {
    "service_id": "svc-123",
    "build_id": "build-456",
    "image_repo": "registry.example.com/my-api",
    "image_tag": "v2.0.0",
    "image_digest": "sha256:abc123...",
    "metadata": {
      "git_commit": "abc123",
      "build_timestamp": "2026-06-01T12:00:00Z"
    }
  },
  "tags": [
    ["service", "svc-123"],
    ["build", "build-456"],
    ["digest", "sha256:abc123..."]
  ]
}
```

### CLI and MCP

Artifact registration mutations are signer-first. MCP `bahia_register_artifact` publishes a signed kind `5985` ArtifactRegister event and returns the request event id, pubkey, kind, and accepted relay count. Legacy direct REST/registry registration is not a production mutation path.

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

Bahia supports real SBOM generation and import for artifact subjects. The canonical workflow stores exact SBOM payload bytes on Blossom, publishes a `30078` NIP-78 SBOM reference event, then publishes/replaces a complete `30004` NIP-51 availability list for the artifact subject. Historical `30079` SBOM index events are read-only compatibility data and are not used for new publication.

### Generating or importing SBOMs

Signer-first generation and import use ContextVM methods over kind `25910`:

```json
{
  "jsonrpc": "2.0",
  "id": "sbom-art-456-spdx",
  "method": "sbom/generate",
  "params": {
    "idempotencyKey": "sbom-art-456-spdx",
    "subject": { "type": "artifact", "id": "art-456", "digest": "sha256:<artifact-digest>" },
    "source": { "kind": "oci-image", "locator": "registry.example.com/my-api@sha256:<artifact-digest>" },
    "formats": ["spdx", "cyclonedx"],
    "generator": "syft",
    "storage": "blossom"
  }
}
```

`generator: "auto"` chooses cdxgen for repository CycloneDX generation only when the operator enabled a cdxgen binary; otherwise Syft is the default generator. Generated/imported payloads must use Blossom storage. Direct OCI or package-backend SBOM writes are intentionally outside this path.

The existing REST endpoint remains a compatibility import path for non-Nostr clients:

```bash
curl -X POST "$BAHIA_URL/api/v1/artifacts/$ARTIFACT_ID/sbom" \
  -H "Content-Type: application/json" \
  -d @sbom.json
```

That endpoint delegates to the SBOM import service, uploads/verifies the payload on Blossom, publishes canonical Nostr observables, and keeps the artifact SBOM read projection available.

### Viewing SBOM

The artifact SBOM view is a compatibility projection. Canonical SBOM reference and availability events are not mutated after publication; when Security OSV completes a successful scan, Bahia refreshes the projection's `vulnerability_count`, `critical_count`, and `high_count` from the latest Security scan so existing policy/UI consumers continue to see current aggregate counts. If no Security scan exists, the original SBOM aggregate counts remain visible.

```bash
# Get SBOM compatibility projection
bahia artifacts sbom art-456

# List indexed packages
bahia artifacts sbom art-456 --packages

# Search package projections
bahia sbom search --package "log4j" --version "<2.17.0"
```

### Web UI

1. Go to **Artifacts → Registry**.
2. Use the per-row **Generate SBOM** or **Regenerate SBOM** action to open the artifact directly on its SBOM tab.
3. On artifact detail, the same **Generate SBOM** or **Regenerate SBOM** action is also visible in the page header and on the SBOM tab.
4. The browser publishes a signer-backed encrypted ContextVM `sbom/generate` request; it does not call a REST generation endpoint.
5. Watch for `30078` SBOM reference events and the subject `30004` availability list to confirm durable completion. The ContextVM reply only confirms request handling.
6. View attestation details, Blossom location, hashes, NTIA status, and package list after the artifact read projection updates.

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

When publishing an `ArtifactRegister` event, include the source build id so the artifact is linked to build provenance:

```json
{
  "kind": 5985,
  "content": {
    "service_id": "svc-123",
    "build_id": "build-456",
    "image_repo": "registry.example.com/my-api",
    "image_tag": "v2.0.0",
    "image_digest": "sha256:abc123..."
  },
  "tags": [
    ["service", "svc-123"],
    ["build", "build-456"],
    ["digest", "sha256:abc123..."]
  ]
}
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
