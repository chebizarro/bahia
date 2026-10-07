# Builds

**Builds** (`/builds`) shows governed build requests and signed Hive-CI results. A successful result can register a deployable artifact.

## Request a build

```bash
bahia builds request --org "$ORG" --service <service-id> --git-ref main \
  --credential-ref <repository-credential-secret-id> \
  --artifact-repo ghcr.io/acme/api \
  --idempotency-key <uuidv7> --result-timeout 120s
```

The daemon resolves the service repository metadata and protected credential, prepares the configured source provider, and submits work to Hive-CI. It returns bounded acceptance data including the build ID. Reuse the same idempotency key after a timeout.

The fleet initiator requires `hiveci.initiator.enabled`, an explicit source provider, and configured mirror/worker trust. Repository credentials are passed to the selected worker without being embedded in clone URLs or public events. The CLI rejects non-empty `--build-arg` values because the fleet dispatch contract has no build-argument field.

## Browse builds

```bash
bahia builds list --service <service-id> --limit 20 --offset 0
bahia builds get --build <build-id>
```

MCP provides `bahia_list_builds` and `bahia_get_build`. Build creation comes from the signed request and Hive-CI evidence rather than a generic MCP create tool.

## Artifact registration

Bahia accepts workflow run and result evidence only from configured trusted publishers. The result must bind the service, commit, image digest, and relevant build identifiers. A verified successful result creates or updates the build record and registers the artifact. A worker result from an untrusted key is ignored.

## Failures

If initiation is unavailable, verify the `hiveci` source provider, mirror endpoint, protected secret reference, worker relay reachability, and trusted publisher lists. If a result is visible on a relay but no artifact appears, compare its author and tags with the configured trust boundary.

## Related

- [Artifacts](artifacts.md)
- [Services](services.md)
- [Workers](workers.md)
