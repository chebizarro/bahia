import { beforeEach, describe, expect, it, vi } from 'vitest';

import { BahiaClient } from '../../src/lib/api/client.js';

function jsonResponse(data, { ok = true, status = 200, statusText = 'OK' } = {}) {
  return {
    ok,
    status,
    statusText,
    headers: new Map([['content-type', 'application/json']]),
    json: async () => data
  };
}

describe('BahiaClient HTTP-native interop contract', () => {
  let client;

  beforeEach(() => {
    global.fetch = vi.fn();
    client = new BahiaClient();
  });

  it('adds NIP-98 authorization from the configured auth provider', async () => {
    const provider = { getAuthorizationHeader: vi.fn().mockResolvedValue('Nostr signed-event') };
    client.setAuthProvider(provider);
    global.fetch.mockResolvedValueOnce(jsonResponse({ data: ['https://blossom.example'] }));

    await client.getBlossomServers();

    expect(provider.getAuthorizationHeader).toHaveBeenCalledWith({ method: 'GET', url: '/api/v1/blossom/servers' });
    expect(global.fetch).toHaveBeenCalledWith('/api/v1/blossom/servers', expect.objectContaining({
      method: 'GET',
      headers: expect.objectContaining({ Authorization: 'Nostr signed-event' })
    }));
  });

  it('keeps query serialization for surviving SBOM search endpoints', async () => {
    global.fetch.mockResolvedValueOnce(jsonResponse({ data: [{ name: 'openssl' }] }));

    const result = await client.searchSBOMPackages({ q: 'openssl', licenses: ['Apache-2.0', 'MIT'], empty: '' });

    expect(result).toEqual([{ name: 'openssl' }]);
    expect(global.fetch).toHaveBeenCalledWith('/api/v1/sbom/search?q=openssl&licenses=Apache-2.0%2CMIT', expect.any(Object));
  });

  it('exposes Blossom HTTP methods used by artifact routes', async () => {
    global.fetch
      .mockResolvedValueOnce(jsonResponse({ data: ['https://blossom.example'] }))
      .mockResolvedValueOnce(jsonResponse({ data: { 'https://blossom.example': 'ok' } }))
      .mockResolvedValueOnce(jsonResponse({ data: [{ sha256: 'abc' }] }))
      .mockResolvedValueOnce(jsonResponse({ data: { uploads: 1 } }));

    await expect(client.getBlossomServers()).resolves.toEqual(['https://blossom.example']);
    await expect(client.checkBlossomHealth()).resolves.toEqual({ 'https://blossom.example': 'ok' });
    await expect(client.listBlossomBlobs('pubkey')).resolves.toEqual([{ sha256: 'abc' }]);
    await expect(client.getBlossomStats()).resolves.toEqual({ uploads: 1 });

    expect(global.fetch).toHaveBeenNthCalledWith(3, '/api/v1/blossom/list', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ pubkey: 'pubkey' })
    }));
  });

  it('exposes SBOM and attestation HTTP methods used by artifact detail routes', async () => {
    const artifactId = 'artifact/v1';
    global.fetch
      .mockResolvedValueOnce(jsonResponse({ data: { bomFormat: 'CycloneDX' } }))
      .mockResolvedValueOnce(jsonResponse({ data: [{ name: 'pkg' }] }))
      .mockResolvedValueOnce(jsonResponse({ data: { ingested: true } }))
      .mockResolvedValueOnce(jsonResponse({ data: { predicateType: 'sbom' } }))
      .mockResolvedValueOnce(jsonResponse({ data: { compliant: true } }));

    await expect(client.getSBOM(artifactId)).resolves.toEqual({ bomFormat: 'CycloneDX' });
    await expect(client.getSBOMPackages(artifactId, { limit: 10 })).resolves.toEqual([{ name: 'pkg' }]);
    await expect(client.ingestSBOM(artifactId, { bom: {} })).resolves.toEqual({ ingested: true });
    await expect(client.getSBOMAttestation(artifactId)).resolves.toEqual({ predicateType: 'sbom' });
    await expect(client.getSBOMNTIACompliance(artifactId)).resolves.toEqual({ compliant: true });

    const encoded = encodeURIComponent(artifactId);
    expect(global.fetch).toHaveBeenNthCalledWith(1, `/api/v1/artifacts/${encoded}/sbom`, expect.any(Object));
    expect(global.fetch).toHaveBeenNthCalledWith(2, `/api/v1/artifacts/${encoded}/sbom/packages?limit=10`, expect.any(Object));
    expect(global.fetch).toHaveBeenNthCalledWith(3, `/api/v1/artifacts/${encoded}/sbom`, expect.objectContaining({ method: 'POST' }));
    expect(global.fetch).toHaveBeenNthCalledWith(4, `/api/v1/artifacts/${encoded}/sbom/attestation`, expect.any(Object));
    expect(global.fetch).toHaveBeenNthCalledWith(5, `/api/v1/artifacts/${encoded}/sbom/ntia`, expect.any(Object));
  });

  it('exposes only the current HTTP-native interop method surface', () => {
    const methods = Object.getOwnPropertyNames(BahiaClient.prototype).filter((name) => name !== 'constructor').sort();
    expect(methods).toEqual([
      'checkBlossomHealth',
      'clearInstanceMaintenance',
      'fetch',
      'fetchBlossomBlob',
      'getBlossomServers',
      'getBlossomStats',
      'getInstanceHealth',
      'getSBOM',
      'getSBOMAttestation',
      'getSBOMNTIACompliance',
      'getRouteCanary',
      'getSBOMPackages',
      'ingestSBOM',
      'listBlossomBlobs',
      'listConfigFabricDrift',
      'listInstanceHealth',
      'listInstanceHealthEvents',
      'listInstanceRecoveryAttempts',
      'listRouteCanaries',
      'listRouteCanaryEvents',
      'publishConfigFabricEvent',
      'query',
      'rollbackConfigFabricEvent',
      'searchSBOMPackages',
      'setAuthProvider',
      'setInstanceMaintenance'
    ].sort());
  });

  it('does not expose removed ML bridge calls', () => {
    const importMethod = ['import', 'ML', 'Model'].join('');
    const deployMethod = ['deploy', 'ML', 'Endpoint'].join('');
    expect(client[importMethod]).toBeUndefined();
    expect(client[deployMethod]).toBeUndefined();
  });

  it('normalizes backend and HTTP errors', async () => {
    global.fetch.mockResolvedValueOnce(jsonResponse({ error: 'SBOM not found' }, { ok: false, status: 404, statusText: 'Not Found' }));
    await expect(client.getSBOM('missing')).rejects.toThrow('SBOM not found');
  });

  it('attaches the HTTP status to thrown errors so callers can detect 404s without parsing the message', async () => {
    // 404 is not in the default retriable status set, so a single mocked response suffices.
    global.fetch.mockResolvedValueOnce(jsonResponse({}, { ok: false, status: 404, statusText: 'Not Found' }));
    await expect(client.getSBOM('missing')).rejects.toMatchObject({ status: 404 });

    // 5xx is retried once by default for GET requests, so mock both attempts.
    global.fetch
      .mockResolvedValueOnce(jsonResponse({ error: 'boom' }, { ok: false, status: 500, statusText: 'Internal Server Error' }))
      .mockResolvedValueOnce(jsonResponse({ error: 'boom' }, { ok: false, status: 500, statusText: 'Internal Server Error' }));
    await expect(client.getSBOM('missing')).rejects.toMatchObject({ status: 500 });
  });

  it('exposes route canary list, detail, and event-lineage HTTP methods', async () => {
    // Mock the real wire shapes: a list response is {data: [...], total,
    // limit, offset} (the repo's writeData convention with pagination
    // metadata), and a single-route detail response is {data: <summary>}. An
    // earlier version of this test mocked shapes that did not match the real
    // detail endpoint, which hid the +page.svelte bug of never passing
    // deployment_unit_id through to getRouteCanary/listRouteCanaryEvents.
    global.fetch
      .mockResolvedValueOnce(jsonResponse({ data: [{ hostname: 'git.example.com' }], total: 1, limit: 50, offset: 0 }))
      .mockResolvedValueOnce(jsonResponse({ data: { hostname: 'git.example.com', open: true } }))
      .mockResolvedValueOnce(jsonResponse({ data: [{ transition: 'opened' }], total: 1, limit: 50, offset: 0 }));

    await expect(client.listRouteCanaries({ service_id: 'svc-1', open: true })).resolves.toEqual([{ hostname: 'git.example.com' }]);
    await expect(client.getRouteCanary('svc-1', 'env-1', 'git.example.com', 'unit-1')).resolves.toEqual({ hostname: 'git.example.com', open: true });
    await expect(client.listRouteCanaryEvents('svc-1', 'env-1', 'git.example.com')).resolves.toEqual([{ transition: 'opened' }]);

    expect(global.fetch).toHaveBeenNthCalledWith(1, '/api/v1/route-canaries?service_id=svc-1&open=true', expect.any(Object));
    expect(global.fetch).toHaveBeenNthCalledWith(2, '/api/v1/services/svc-1/environments/env-1/routes/git.example.com/canary?deployment_unit_id=unit-1', expect.any(Object));
    expect(global.fetch).toHaveBeenNthCalledWith(3, '/api/v1/services/svc-1/environments/env-1/routes/git.example.com/canary/events?limit=50', expect.any(Object));
  });
});
