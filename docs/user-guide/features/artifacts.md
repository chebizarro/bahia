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
| `image_repo` | Container image repository | Yes |
| `image_tag` | Image tag (build handle) | Yes |
| `image_digest` | Immutable manifest digest (`sha256:`) | Yes |
| `service_id` | Associated service | Yes |
| `build_id` | Source build | Yes for registration |
| `manifest_media_type`, `size_bytes` | Manifest details | No |
| `sbom_url`, `signature_ref`, `scan_status` | Supply-chain evidence (`scan_status` defaults to `unknown`) | No |
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

The **Builds** page also provides an idempotent **Register verified build artifact** recovery action for a successful result. The signed ContextVM request contains only `build_id`; repository, tag, digest, CI provenance, signatures, SBOM reference, scan state, and policy state come from verified server-side evidence.

### Advanced manual registration

The legacy `POST /api/v1/artifacts` mutation has been removed. The signer-first ContextVM `artifact/register` path is available through `bahia artifacts register` and is rejected unless `hiveci.allow_manual_artifact_registration: true` is explicitly configured. The signed kind `5985` handler remains a narrowly scoped compatibility exception for artifact registration.

Even when enabled, the server requires an existing service/build binding, the service's exact artifact repository, a non-empty tag, and a full `sha256:` manifest digest. It resolves the tag in the configured registry and refuses missing, mutable-only, unverifiable, or tag/digest-mismatched references. Manual registration cannot bypass canonical deduplication or verification.

## Viewing Artifacts

### Web UI

1. Go to **Artifacts** in the sidebar
2. Use the **Registry** tab for registered artifacts (with **SBOM Status** and **Signature Status** badges) or the **Blossom** tab for stored blobs
3. Click an artifact to see:
   - Image details
   - Build provenance
   - SBOM packages
   - Signatures

Or from a service:
1. Go to **Services** → select service
2. Scroll to the **Artifacts** section

### CLI

The CLI registers `bahia artifacts register` for explicitly enabled manual recovery and `bahia artifacts import-observed` for observation-verified live imports. It does not register a `bahia builds` group. Use the web UI or trusted HiveCI flow for normal registration. The MCP tools below are usable only in an embedding that explicitly configures external MCP authorization and the required backing services.

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

1. Go to **Artifacts → Registry** and open the artifact.
2. On artifact detail, use the **Generate SBOM** or **Regenerate SBOM** action in the page header or on the SBOM tab.
3. The browser opens the SBOM tab, subscribes to artifact-scoped `30078` SBOM reference events and the subject `30004` availability list, then publishes a signer-backed encrypted ContextVM `sbom/generate` request. It does not call a REST generation endpoint.
4. Bahia only uses explicit image refs or configured artifact repositories plus immutable digests as generation sources. The ContextVM reply only acknowledges request handling; durable completion is shown when canonical SBOM reference or availability events arrive.
5. View attestation details, Blossom location, hashes, NTIA status, and package list directly from the canonical SBOM events and compatibility projection data.

## Signatures

Artifacts can have cryptographic signatures for provenance.

### Viewing and verifying signatures

Use `bahia_list_signatures`, `bahia_list_verified_signatures`, `bahia_has_verified_signature`, and `bahia_verify_signatures` through MCP. Verification discovers supported signatures, evaluates configured trust roots, stores results, and returns status.

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

Builds are normally registered by trusted signed HiveCI results (see [Builds](builds.md)). The `bahia_register_build` MCP tool exists for embeddings that authorize external MCP callers. The current CLI does not register a build command.

### Linking to Artifacts

Every registration path binds the artifact to its source build: automatic HiveCI registration and **Register verified build artifact** derive the binding from the trusted result, and `bahia artifacts register --build <build-id> --service <service-id> …` requires it explicitly.

## Canonical Observables

Artifact and build state is published as canonical `30900` state projections with `30315` status and `4903` audit facts. The historical `31966` (artifact registry) and `31969` (build registry) read-model kinds are legacy migration inventory only; do not subscribe to them for live state.

## OCI Registry Integration

When `oci.enabled: true`, Bahia serves an OCI Distribution API (`/v2/`) that can act as your container registry (set `oci.public_host` for the advertised host).

### Pushing Images

```bash
# Tag and push
docker tag my-api:latest bahia.example.com/my-api:v2.0.0
docker push bahia.example.com/my-api:v2.0.0
```

### Authentication

- **NIP-98**: Nostr-signed HTTP auth
- **Basic Auth**: Service account credentials
- **Anonymous**: Pull only, from `oci.allow_anonymous_pull_cidrs`

### Benefits

- Registry-verified digests for artifact registration
- Unified access control
- Blossom-backed blob storage

> **NOTE (2026-09-11):** pushing to the embedded registry does not by itself register a Bahia artifact or generate an SBOM; registration still flows through HiveCI or the explicit registration commands above.

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
