# Artifacts

`sbom/generate|import` and `artifact/signature-verify|register-build-result` are signed request intents with the [D80 content shapes](../../../web/tests/fixtures/d80-intent-content.json). Their `30315` data is a bounded acknowledgement or count, not the SBOM, signature evidence, or artifact record. Read those from the existing canonical families.

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

### From a build (recommended)

A successful trusted HiveCI result is the normal registration source. Bahia binds the signed result to its service and original build, verifies that the service repository and result tag resolve to the claimed immutable manifest digest, and creates one canonical artifact projection. Operators do not copy names, versions, digests, or CI identifiers.

Automatic registration is enabled by default:

```yaml
hiveci:
  enabled: true
  auto_register_builds: true
  allow_manual_artifact_registration: false
```

Verified HiveCI build-result artifact registration is daemon-owned; there is no operator register-result action. Repository, tag, digest, CI provenance, signatures, SBOM reference, scan state, and policy state come from verified server-side evidence.

### Advanced manual registration

The legacy `POST /api/v1/artifacts` mutation has been removed. Signed kind `5985`/`bahia_register_artifact` registration is an advanced recovery path and is rejected unless `hiveci.allow_manual_artifact_registration: true` is explicitly configured.

The web control-plane API can submit an operator-supplied artifact registration as a signed kind-30900 `artifact/register` intent at `artifact:<client-minted-id>`. It carries the build and service IDs, repository, tag, and immutable digest; the daemon publishes the canonical registry record and a bounded acceptance status. Observed-artifact import uses `artifact/import-observed` at `artifact-import:<service-id>:<environment-id>:<digest>`. The separate Builds-page recovery action still delegates verification of a trusted HiveCI result to the daemon using only its build ID; it does not substitute operator-supplied metadata for that verification.

Even when enabled, the server requires an existing service/build binding, the service's exact artifact repository, a non-empty tag, and a full `sha256:` manifest digest. It resolves the tag in the configured registry and refuses missing, mutable-only, unverifiable, or tag/digest-mismatched references. Manual registration cannot bypass canonical deduplication or verification.

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

The CLI reads build and artifact registry records from signed, service-authored `30900` events by default. Configure `--service-pubkey` and `--relay` (or their environment equivalents). Reads resume from a local cursor; if no relay reaches EOSE, cached results are returned with a stale-data warning and exit status 0. The legacy REST registry reads are no longer mounted.

```bash
bahia builds list --service <service-uuid>
bahia builds get --build <build-uuid>
bahia artifacts list --service <service-uuid>
bahia artifacts get --artifact <artifact-uuid>
```

The MCP tools below are usable only in an embedding that explicitly configures external MCP authorization.

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

Each `30078` reference contains a DSSE envelope over the exact in-toto statement. Bahia signs the DSSE pre-authentication encoding with its configured Nostr service key, rejects unsigned or tampered references before publication, and requires the DSSE signer to match the verified Nostr event publisher when reading a reference. Payload resolution also rejects unsigned attestations before fetching or accepting SBOM bytes.

### Generating or importing SBOMs

The browser publishes signed kind-`30900` `sbom/generate` or `sbom/import` request intents. The content follows the [D80 fixture](../../../web/tests/fixtures/d80-intent-content.json); the signed `intent_id` is also the `idempotencyKey`. For example, generation content is:

```json
{
  "intent_id": "<intent-uuid>",
  "idempotencyKey": "<intent-uuid>",
  "subject": { "type": "artifact", "id": "art-456", "digest": "sha256:<artifact-digest>" },
  "source": { "kind": "oci-image", "locator": "registry.example.com/my-api@sha256:<artifact-digest>" },
  "formats": ["spdx", "cyclonedx"],
  "generator": "syft",
  "storage": "blossom"
}
```

`generator: "auto"` chooses cdxgen for repository CycloneDX generation only when the operator enabled a cdxgen binary; otherwise Syft is the default generator. An explicit `generator: "cdxgen"` request fails with a clear unavailable-binary/disabled error when cdxgen is not configured or cannot be executed.

Enable cdxgen in runtime config only when the executable is installed on the Bahia server:

```yaml
sbom:
  cdxgen:
    enabled: true
    binary_path: "/usr/local/bin/cdxgen"
```

Equivalent environment variables:

```bash
BAHIA_SBOM_CDXGEN_ENABLED=true
BAHIA_SBOM_CDXGEN_BINARY_PATH=/usr/local/bin/cdxgen
```

Generated/imported payloads must use Blossom storage. Direct OCI or package-backend SBOM writes are intentionally outside this path.

The existing REST endpoint remains a compatibility import path for non-Nostr clients:

```bash
curl -X POST "$BAHIA_URL/api/v1/artifacts/$ARTIFACT_ID/sbom" \
  -H "Content-Type: application/json" \
  -d @sbom.json
```

That endpoint delegates to the SBOM import service, uploads/verifies the payload on Blossom, publishes canonical Nostr observables, and keeps the artifact SBOM read projection available.

### Viewing SBOM

The artifact SBOM view is a compatibility projection. Canonical SBOM reference and availability events are not mutated after publication; when Security OSV completes a successful scan, Bahia refreshes the projection's `vulnerability_count`, `critical_count`, and `high_count` from the latest Security scan so existing policy/UI consumers continue to see current aggregate counts. If no Security scan exists, the original SBOM aggregate counts remain visible.

Use `bahia_get_sbom` for the compatibility projection, `bahia_get_sbom_packages` for indexed packages, and `bahia_search_sbom_packages` for package searches. These are MCP tools, not CLI commands.

### Web UI

1. Go to **Artifacts → Registry**.
2. Use the per-row **Generate SBOM** or **Regenerate SBOM** action to open the artifact directly on its SBOM tab.
3. On artifact detail, the same **Generate SBOM** or **Regenerate SBOM** action is also visible in the page header and on the SBOM tab.
4. The browser opens the SBOM tab, reads artifact-scoped `30078` SBOM references and `30004` availability lists from its event store (routed by kind and `t` topic), then publishes a signed `sbom/generate` intent. It does not call a REST generation endpoint.
5. Bahia only uses explicit image refs or configured artifact repositories plus immutable digests as generation sources. The requester-scoped `30315` status acknowledges handling; durable completion is shown when canonical SBOM reference or availability events arrive.
6. View attestation details, Blossom location, hashes, NTIA status, and package list directly from the canonical SBOM events and compatibility projection data.

## Signatures

Artifacts can have cryptographic signatures for provenance.

### Viewing and verifying signatures

Use `bahia_list_signatures`, `bahia_list_verified_signatures`, `bahia_has_verified_signature`, and `bahia_verify_signatures` through MCP. Verification submits an `artifact/signature-verify` intent. Its bounded status reports counts; canonical signature records carry the durable verification result. A pending MCP response is correlation, not a completed verification.

### MCP Tool

```json
{
  "tool": "bahia_verify_signatures",
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

Query artifact projections with `bahia_list_artifacts`, then apply supported service filters in the MCP request or filter returned metadata in the client.

## Builds and Artifacts

**Builds** are CI workflow executions that produce artifacts.

```
Build (CI run) → produces → Artifact (container image)
```

### Registering Builds

Build rows and status are authored by Bahia from trusted Hive-CI workflow events and build requests. Operators cannot hand-register a build through MCP or REST; the CLI has no manual build registration command. A successful verified result can still be used to register its artifact.

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

## Canonical MCP reads

Artifact signature and parsed SBOM MCP reads use daemon-authored `30900` state. SBOM packages are indexed one per record, so large package lists do not exceed relay frame limits. The existing `30078` reference and `30004` availability records remain the public interop source for SBOM availability.

CLI `artifacts register` and `artifacts import-observed` publish signed kind-`30900` artifact intents and wait for bounded kind-`30315` status. Registration accepts a client-minted `--id` artifact UUID for retry; observed import never promotes or deploys the imported image.
