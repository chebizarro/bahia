# Private repository builds

The **Builds** page starts and monitors Arcana builds through Bahia's signed ContextVM control plane.

## Requesting a build

1. Register the Arcana service with repository coordinate `chebizarro/living-library-forge` and its OCI artifact repository.
2. Store the private source repository credential as a protected service secret.
3. Open **Delivery → Builds**, select the service and the secret reference, then enter a branch, tag, or full commit.
4. Optionally set public `VITE_*` compile-time values and request the build.

The browser sends only the opaque secret ID. It never reads the secret value and does not store credentials in localStorage. Bahia accepts only the Arcana Dockerfile's nine documented public Vite arguments; secret-style Docker build arguments are rejected.

## Source provider configuration

The fleet initiator requires an explicit source provider and never infers one from the clone URL. GitHub keeps token-based migration and may derive its clone URL from the request's `owner/name` coordinate:

```yaml
hiveci:
  initiator:
    enabled: true
    source_provider: github
```

A private Gitea source requires both its credential-free HTTPS clone URL and a non-secret username. Bahia resolves the protected service credential server-side and supplies it to fleet Gitea as the mirror password; it is not embedded in the URL:

```yaml
hiveci:
  initiator:
    enabled: true
    source_provider: gitea
    source_clone_url: https://git.example/organization/repository.git
    source_auth_username: bahia-mirror
```

`source_clone_url` must be an absolute HTTPS URL without user information, query parameters, or fragments. A `github` source URL must use `github.com`. Missing or unsupported provider configuration, or missing Gitea clone/username configuration, prevents startup and fails closed.

## Verified artifact registration

A trusted, signed HiveCI result drives the normal artifact flow. Bahia correlates it with the original build, resolves the result's repository and tag through the embedded OCI layout or configured registry, and requires the resolved manifest digest to exactly match the result's full `sha256:` digest. Mutable-only, repository-mismatched, tag/digest-mismatched, and otherwise unverifiable results are refused.

With `hiveci.auto_register_builds: true` (the default), a successful result is registered automatically. If automatic processing is disabled or a verified result is waiting for recovery, **Register verified build artifact** on the successful build sends only the Bahia build ID; the server derives every OCI identifier from the trusted result. Repeating either path returns the same deduplicated artifact.

The build row shows the immutable `repository@sha256:digest` reference, manifest digest, verification source/state, policy and scan state, signature count, SBOM reference, and CI provenance. Only this registered digest becomes a deployment candidate.

## Status and logs

Queued, running, succeeded, and failed states come from signed canonical build projections. Evidence supplied by HiveCI (log URL and request/run/result event IDs) appears with each build.

## Unavailable fleet boundary

Build initiation requires the fleet Gitea private-mirror and HiveCI runner adapter. If it is not configured, Bahia returns a signed fail-closed error and creates no queued build. Do not work around this by placing source credentials in a ref, clone URL, build argument, Nostr event, or browser storage.
