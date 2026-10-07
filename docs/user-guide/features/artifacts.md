# Artifacts

Artifacts are immutable, digest-pinned container images that Bahia can deploy. Each artifact records its service, build provenance, image repository and tag, digest, and optional SBOM, signature, scan, and metadata references.

## Registration paths

The normal path is a governed build:

1. `bahia builds request` publishes a signed build intent.
2. Hive-CI runs the build and publishes signed result evidence.
3. Bahia verifies the result author and registers the artifact.
4. The artifact appears in **Artifacts** and is available to deployment previews.

Manual registration is available for trusted automation:

```bash
bahia artifacts register --service <service-id> --build <build-id> \
  --image-repo ghcr.io/acme/api --image-tag v1.4.0 \
  --image-digest sha256:<digest>
```

`bahia artifacts import-observed` registers an image that Bahia already observes at a managed runtime target. The image digest and `bahia.*` labels must match the observation, and `hiveci.allow_live_artifact_import` must be enabled. Importing lineage does not change desired state.

## Browsing artifacts

Open **Artifacts** (`/artifacts`) to filter by service and inspect provenance, signatures, SBOM availability, and scan status.

```bash
bahia artifacts list --service <service-id>
bahia artifacts get --artifact <artifact-id>
```

MCP provides `bahia_list_artifacts`, `bahia_get_artifact`, and `bahia_register_artifact`.

## SBOMs

Bahia accepts SPDX and CycloneDX JSON. Use `bahia_ingest_sbom` for inline documents up to 360 KiB, or the authenticated HTTP import when a database-backed artifact repository is configured:

```bash
curl -X POST https://bahia.example/api/v1/artifacts/<artifact-id>/sbom \
  -H 'Authorization: Nostr <nip98-event>' \
  -H 'Content-Type: application/json' \
  --data-binary @sbom.json
```

The import acknowledgement is not completion. Follow the artifact's SBOM reference and availability events, then use `bahia_get_sbom`, `bahia_get_sbom_packages`, or `bahia_search_sbom_packages`.

## Signatures

Signature records bind an artifact digest to a signer and verification result. The MCP tools `bahia_list_signatures`, `bahia_get_signature`, `bahia_list_verified_signatures`, `bahia_has_verified_signature`, and `bahia_verify_signatures` expose the current state. Policies may require a verified signature before deployment.

## Operational rules

- Deploy by digest. Tags are labels, not identity.
- Treat build author trust as part of the supply-chain boundary.
- Keep registry credentials in service secrets, not artifact metadata.
- Wait for canonical artifact, SBOM, signature, and scan records before relying on an accepted request.
- A digest mismatch means the registry content changed or the wrong image was selected; do not bypass the check.

## Related

- [Builds](builds.md)
- [Deployments](deployments.md)
- [Policies](policies.md)
