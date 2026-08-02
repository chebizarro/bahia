# Bahia Web HTTP Client Reference

The browser HTTP compatibility client is `web/src/lib/api/client.js`. It is deliberately small: most shared state and control-plane mutations use Nostr stores and transport helpers, not methods on this class.

## Construction and export

```javascript
import { BahiaClient, api } from '$lib/api/client.js';
```

`BahiaClient` can be constructed in tests. `api` is a browser-only singleton and is `null` during server-side rendering. The fixed base path is `/api/v1`.

## Request behavior

`api.fetch(path, options)`:

1. builds the `/api/v1` URL;
2. defaults to `GET`;
3. adds `Content-Type: application/json`;
4. asks the configured auth provider for a direct NIP-98 header unless the caller supplied Authorization;
5. retries GET network failures and 5xx responses once by default;
6. throws for non-2xx responses or a JSON envelope containing `error`;
7. returns the envelope's `data`, or `null` for non-JSON success.

```javascript
api.setAuthProvider({
  getAuthorizationHeader: async ({ method, url }) => {
    return `Nostr ${signedEventBase64}`;
  }
});
```

No JWT exchange or `bahia_token` storage is performed.

### Retry overrides

```javascript
await api.fetch('/example', {
  retries: 3,
  retryDelayMs: 250,
  retryStatuses: [429, 503]
});
```

These transport-only options are removed before global `fetch`. Mutating methods default to zero retries; opt in only when replay is safe.

## Query serialization

`query(params)` omits `null`, `undefined`, and empty strings; comma-joins arrays; stringifies other values; and URL-encodes keys/values.

```javascript
api.query({ status: 'running', tags: ['gpu', 'us-west'], empty: '' });
// ?status=running&tags=gpu%2Cus-west
```

## Implemented domain methods

The current client exposes only SBOM and Blossom compatibility methods.

### SBOM

```javascript
await api.getSBOM(artifactId);
await api.getSBOMPackages(artifactId, { limit: 50, offset: 0 });
await api.searchSBOMPackages({ name: 'openssl' });
await api.ingestSBOM(artifactId, payload);
await api.getSBOMAttestation(artifactId);
await api.getSBOMNTIACompliance(artifactId);
```

Paths:

- `GET /api/v1/artifacts/{artifactId}/sbom`
- `GET /api/v1/artifacts/{artifactId}/sbom/packages`
- `GET /api/v1/sbom/search`
- `POST /api/v1/artifacts/{artifactId}/sbom`
- `GET /api/v1/artifacts/{artifactId}/sbom/attestation`
- `GET /api/v1/artifacts/{artifactId}/sbom/ntia`

### Blossom

```javascript
await api.listBlossomBlobs();
await api.listBlossomBlobs(pubkey);
await api.getBlossomServers();
await api.checkBlossomHealth();
await api.getBlossomStats();

const response = await api.fetchBlossomBlob(sha256);
const blob = await response.blob();
```

`listBlossomBlobs` uses `POST /api/v1/blossom/list` and normalizes empty data to `[]`. Server/health/stats methods normalize empty data to `[]` or `{}`.

`fetchBlossomBlob` calls global `fetch` and returns the raw `Response` so callers choose `text()`, `json()`, or `blob()`.

## What is not on this client

There are no `listServices`, `createService`, `listEnvironments`, deployment, policy, secret, worker, state, payment, notification, LLM, or SoulFactory methods on `BahiaClient`.

Current browser paths use:

- `web/src/lib/stores/collections/**` for relay-projected shared read models;
- `web/src/lib/stores/public-controlplane.svelte.js` and domain helpers for public signed mutations;
- `web/src/lib/nostr/encrypted-controlplane.js` plus sensitive-domain stores for encrypted ContextVM flows;
- `web/src/lib/stores/souls.svelte.js` for SoulFactory;
- `web/src/lib/nostr/pool-client.js` for relay subscriptions and publishing.

Do not add a convenient REST method for a domain whose authoritative mutation path is ContextVM/Nostr.

## Error handling

```javascript
try {
  const sbom = await api.getSBOM(artifactId);
} catch (error) {
  console.error(error.message);
}
```

For `{"error":"artifact not found"}`, the thrown message is `artifact not found`. If the body is not JSON, the fallback is `HTTP <status>: <statusText>`.

## Extending the client

Only add a method when the endpoint is intentionally part of the HTTP surface.

```javascript
getExample(id) {
  return this.fetch(`/examples/${encodeURIComponent(id)}`);
}

postExample(payload) {
  return this.fetch('/examples', {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}
```

Requirements:

- encode path parameters;
- stringify JSON bodies;
- use `this.fetch()` unless raw response access is required;
- document retry/idempotency;
- add tests under `web/tests/unit/api-client*.test.js`;
- do not bypass signer, capability, or transport gates.

## Tests

```bash
cd web
pnpm exec vitest run \
  tests/unit/api-client.test.js \
  tests/unit/api-client-core.test.js \
  tests/unit/api-client-extended.test.js \
  tests/unit/api-client-retry-and-edges.test.js
```

## Related documents

- [Web app setup](web-app-setup.md)
- [Web components](web-components.md)
- [Web testing](web-testing.md)
