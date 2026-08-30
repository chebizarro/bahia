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
      'getSBOMPackages',
      'ingestSBOM',
      'listBlossomBlobs',
      'listConfigFabricDrift',
      'listInstanceHealth',
      'listInstanceHealthEvents',
      'listInstanceRecoveryAttempts',
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
});
