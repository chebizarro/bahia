# Bahia Web API Client Reference

The Bahia web app uses a unified API client (`web/src/lib/api/client.js`) for all backend communication.

## Overview

**Location**: `web/src/lib/api/client.js`

**Responsibilities**:
- HTTP request/response handling
- Direct NIP-98 authorization via an auth provider
- Bahia API envelope unwrapping
- Query parameter serialization
- Error normalization

**Singleton Export**:
```javascript
import { api } from '$lib/api/client.js';
```

## Bahia Response Envelope

All Bahia API responses follow this envelope structure:

```json
{
  "data": { /* actual response payload */ },
  "error": "error message if present"
}
```

The client automatically:
- Unwraps `data` for successful responses
- Throws errors for failed responses (4xx, 5xx, or when `error` field is set)

**Example**:
```javascript
// Backend returns: { "data": [{ "id": "svc-123", "name": "web-api" }] }
const services = await api.listServices();
// services = [{ "id": "svc-123", "name": "web-api" }]
```

## HTTP Authentication

The first-party browser no longer stores `bahia_token` or exchanges NIP-98 events for JWTs. Protected requests use an auth provider that signs each HTTP request with direct NIP-98:

```javascript
api.setAuthProvider({
  getAuthorizationHeader: async ({ method, url }) => {
    return signHttpRequestWithNip07({ method, url }); // returns `Nostr <base64event>`
  }
});
```

If no auth provider is configured, requests are sent without `Authorization` and protected endpoints will reject them when auth is enabled.

## Client Methods by Domain

### Services

```javascript
// List all services
await api.listServices();
// Returns: [{ id, name, artifact_repo, runtime_type, ... }]

// Get service by ID
await api.getService(serviceId);
// Returns: { id, name, artifact_repo, runtime_type, ... }

// Create service
await api.createService({
  name: 'my-service',
  artifact_repo: 'ghcr.io/myorg/my-service',
  runtime_type: 'docker',
  default_branch: 'main',
  repo_url: 'https://github.com/myorg/my-service'
});
// Returns: { id, name, ... }

// Update service
await api.updateService(serviceId, {
  name: 'new-name',
  default_branch: 'develop'
});
// Returns: { id, name, ... }

// Delete service
await api.deleteService(serviceId, force = false);
// Returns: null
```

### Environments

```javascript
// List all environments
await api.listEnvironments();
// Returns: [{ id, name, loom_worker_selector, ... }]

// Get environment by ID
await api.getEnvironment(envId);
// Returns: { id, name, loom_worker_selector, runtime_config, ... }

// Create environment
await api.createEnvironment({
  name: 'production',
  loom_worker_selector: { region: 'us-east-1' },
  runtime_config: { timeout: 300, memory: '2Gi' },
  deploy_strategy: 'rolling',
  protected: true
});
// Returns: { id, name, ... }

// Update environment
await api.updateEnvironment(envId, {
  runtime_config: { timeout: 600 }
});
// Returns: { id, name, ... }

// Delete environment
await api.deleteEnvironment(envId);
// Returns: null
```

### Deployments

```javascript
// List deployment intents for a service/environment pair
await api.listIntents(serviceId, envId);
// Returns: [{ id, status, artifact_id, requested_by, ... }]

// Get deployment intent by ID
await api.getIntent(intentId);
// Returns: { id, status, artifact_id, ... }

// Create deployment intent
await api.createIntent(serviceId, envId, artifactId);
// Returns: { id, status, ... }

// Approve deployment intent
await api.approveIntent(intentId);
// Returns: { id, status: 'approved', ... }

// Reject deployment intent
await api.rejectIntent(intentId);
// Returns: { id, status: 'rejected', ... }

// List deployment runs
await api.listRuns({ intent_id: intentId });
// Returns: [{ id, status, started_at, exit_code, ... }]

// Get deployment run by ID
await api.getRun(runId);
// Returns: { id, status, exit_code, logs, ... }

// Rollback deployment
await api.rollback({
  service_id: serviceId,
  environment_id: envId,
  requested_by: 'user@example.com'
});
// Returns: { id, status, ... }
```

### Policies

```javascript
// List all policies
await api.listPolicies();
// Returns: [{ id, name, environment_id, rules, ... }]

// Get policy by ID
await api.getPolicy(policyId);
// Returns: { id, name, rules, enforcement, ... }

// Create policy
await api.createPolicy({
  name: 'sbom-required',
  environment_id: envId,
  rules: [{ type: 'sbom_required' }],
  enforcement: 'hard',
  enabled: true
});
// Returns: { id, name, ... }

// Update policy
await api.updatePolicy(policyId, {
  enforcement: 'soft'
});
// Returns: { id, name, ... }

// Delete policy
await api.deletePolicy(policyId);
// Returns: null
```

### Secrets

```javascript
// List secrets for a service (names only, values hidden)
await api.listSecrets(serviceId);
// Returns: [{ id, name, created_at }]

// Create secret
await api.createSecret(serviceId, {
  name: 'DATABASE_URL',
  value: 'postgresql://...'
});
// Returns: { id, name }

// Update secret value
await api.updateSecret(serviceId, secretId, {
  value: 'new-value'
});
// Returns: { id, name }

// Delete secret
await api.deleteSecret(serviceId, secretId);
// Returns: null
```

### Builds & Artifacts

```javascript
// List builds for a service
await api.listBuilds(serviceId);
// Returns: [{ id, git_sha, git_ref, status, ... }]

// Get build by ID
await api.getBuild(buildId);
// Returns: { id, git_sha, ci_run_id, status, ... }

// List artifacts for a service
await api.listArtifacts(serviceId);
// Returns: [{ id, image_tag, image_digest, build_id, ... }]

// Get artifact by ID
await api.getArtifact(artifactId);
// Returns: { id, image_repo, image_tag, sbom_ref, ... }
```

### SBOM & Signatures

```javascript
// Get SBOM for an artifact
await api.getSBOM(artifactId);
// Returns: { artifact_id, sbom_ref, package_count, ... }

// Verify signatures for an artifact
await api.verifySignatures(artifactId);
// Returns: { verified: true, signatures: [...] }
```

### Workers

```javascript
// List all workers
await api.listWorkers();
// Returns: [{ pubkey, capabilities, region, ... }]

// Get worker by pubkey
await api.getWorker(pubkey);
// Returns: { pubkey, capabilities, pricing, ... }
```

### State & Drift

```javascript
// List all environment states
await api.listStates();
// Returns: [{ service_id, environment_id, desired_image_digest, ... }]

// List drifted states only
await api.listDriftedStates();
// Returns: [{ service_id, environment_id, drift_detected: true, ... }]
```

### Authentication

```javascript
api.setAuthProvider({
  getAuthorizationHeader: async ({ method, url }) => `Nostr ${signedEventBase64}`
});
```

`POST /api/v1/auth/nostr` and `api.exchangeNostrAuth()` have been removed; use direct NIP-98 request signing instead.

## Error Handling

### HTTP Errors

The client throws errors for:
- **Non-2xx status codes**: `HTTP 404: Not Found`
- **Backend error responses**: Unwraps `error` field from envelope
- **Network failures**: `fetch failed`

### Error Structure

```javascript
try {
  await api.getService('invalid-id');
} catch (error) {
  console.error(error.message);
  // "HTTP 404: Not Found"
  // or "Service not found"
}
```

### Best Practices

**Always handle errors in UI components**:
```javascript
import { toasts } from '$lib/components/toast.js';

async function deleteService(id) {
  try {
    await api.deleteService(id);
    toasts.success('Service deleted');
    // Refresh list or navigate away
  } catch (error) {
    toasts.error(`Failed to delete service: ${error.message}`);
  }
}
```

## Query Parameters

The client includes a `query()` helper for serializing URL parameters:

```javascript
api.listRuns({ intent_id: 'intent-123', status: 'running' });
// GET /api/v1/runs?intent_id=intent-123&status=running
```

**Features**:
- Omits `null`, `undefined`, and empty string values
- Handles arrays via comma-joining: `tags: ['a', 'b']` → `tags=a,b`
- URL-encodes keys and values

## Extending the Client

To add new API methods, follow this pattern:

```javascript
// In web/src/lib/api/client.js, inside BahiaClient class:

myNewMethod(param) {
  return this.fetch(`/my-endpoint/${encodeURIComponent(param)}`);
}

myNewPost(payload) {
  return this.fetch('/my-endpoint', {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}
```

**Rules**:
- Always use `this.fetch()` (not global `fetch`)
- Always `encodeURIComponent()` path parameters
- Always `JSON.stringify()` request bodies
- Return the Promise directly (don't add extra error handling)

## Testing the Client

Unit tests mock the global `fetch` function:

```javascript
// tests/unit/api-client.test.js
global.fetch = vi.fn();

beforeEach(() => {
  global.fetch.mockClear();
});

test('listServices calls correct endpoint', async () => {
  global.fetch.mockResolvedValueOnce({
    ok: true,
    headers: new Headers({ 'content-type': 'application/json' }),
    json: async () => ({ data: [{ id: 'svc-1' }] })
  });

  const result = await api.listServices();
  
  expect(global.fetch).toHaveBeenCalledWith(
    '/api/v1/services',
    expect.objectContaining({ headers: expect.any(Object) })
  );
  expect(result).toEqual([{ id: 'svc-1' }]);
});
```

## Next Steps

- **Setup Guide**: See [web-app-setup.md](./web-app-setup.md)
- **Component Library**: See [web-components.md](./web-components.md)
- **Testing Guide**: See [web-testing.md](./web-testing.md)
