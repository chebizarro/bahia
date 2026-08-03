# Private repository builds

The **Builds** page starts and monitors Arcana builds through Bahia's signed ContextVM control plane.

## Requesting a build

1. Register the Arcana service with repository coordinate `chebizarro/living-library-forge` and its OCI artifact repository.
2. Store the private GitHub repository credential as a protected service secret.
3. Open **Delivery → Builds**, select the service and the secret reference, then enter a branch, tag, or full commit.
4. Optionally set public `VITE_*` compile-time values and request the build.

The browser sends only the opaque secret ID. It never reads the secret value and does not store credentials in localStorage. Bahia accepts only the Arcana Dockerfile's nine documented public Vite arguments; secret-style Docker build arguments are rejected.

## Status, logs, and artifacts

Queued, running, succeeded, and failed states come from signed canonical build projections. Evidence supplied by HiveCI (log URL and request/run/result event IDs) appears with each build. A successful build is a deployment candidate only after an artifact projection supplies a valid `sha256:` digest; the UI then displays `repository@sha256:digest`.

## Unavailable fleet boundary

Build initiation requires the fleet Gitea private-mirror and HiveCI runner adapter. If it is not configured, Bahia returns a signed fail-closed error and creates no queued build. Do not work around this by placing GitHub tokens in a ref, build argument, Nostr event, or browser storage.
