import { beforeEach, describe, expect, it, vi } from 'vitest';

import { BahiaClient } from '../../src/lib/api/client.js';

function json(data, extra = {}) {
  return {
    ok: true,
    headers: new Map([['content-type', 'application/json']]),
    json: async () => ({ data }),
    ...extra
  };
}

describe('BahiaClient', () => {
  let client;

  beforeEach(() => {
    vi.clearAllMocks();
    global.fetch = vi.fn();
    client = new BahiaClient();
  });

  it('initializes without bearer token state and can set an auth provider', () => {
    expect(client.authProvider).toBeNull();
    expect(client.token).toBeUndefined();
    const provider = { getAuthorizationHeader: vi.fn() };
    client.setAuthProvider(provider);
    expect(client.authProvider).toBe(provider);
    client.setAuthProvider(null);
    expect(client.authProvider).toBeNull();
  });

  it('makes authenticated Blossom HTTP requests', async () => {
    client.setAuthProvider({ getAuthorizationHeader: vi.fn().mockResolvedValue('Nostr signed-event') });
    global.fetch.mockResolvedValueOnce(json(['https://blossom.example']));

    await expect(client.getBlossomServers()).resolves.toEqual(['https://blossom.example']);

    expect(global.fetch).toHaveBeenCalledWith('/api/v1/blossom/servers', expect.objectContaining({
      method: 'GET',
      headers: expect.objectContaining({
        'Content-Type': 'application/json',
        Authorization: 'Nostr signed-event'
      })
    }));
  });

  it('consumes Config Fabric drift, publish, and rollback endpoints', async () => {
    const payload = {
      kind: 30078,
      service_id: 'khatru-relay',
      policy_name: 'rate-limits',
      scope: 'prod',
      version: 8,
      schema: 'cascadia.config.rate-limits.v1',
      policy: { query: { max_limit: 500 } }
    };
    const eventId = 'a'.repeat(64);
    global.fetch
      .mockResolvedValueOnce(json([{ service_id: 'khatru-relay', desired_version: 7 }]))
      .mockResolvedValueOnce(json({ event_id: eventId, version: 8 }))
      .mockResolvedValueOnce(json({ event_id: 'b'.repeat(64), version: 9 }));

    await expect(client.listConfigFabricDrift()).resolves.toHaveLength(1);
    await expect(client.publishConfigFabricEvent(payload)).resolves.toMatchObject({ version: 8 });
    await expect(client.rollbackConfigFabricEvent(eventId)).resolves.toMatchObject({ version: 9 });

    expect(global.fetch).toHaveBeenNthCalledWith(1, '/api/v1/config-fabric/drift', expect.objectContaining({ method: 'GET' }));
    expect(global.fetch).toHaveBeenNthCalledWith(2, '/api/v1/config-fabric/events', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify(payload)
    }));
    expect(global.fetch).toHaveBeenNthCalledWith(3, '/api/v1/config-fabric/rollback', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ event_id: eventId })
    }));
  });

  it('throws backend error envelopes', async () => {
    global.fetch.mockResolvedValueOnce({
      ok: false,
      status: 404,
      statusText: 'Not Found',
      headers: new Map([['content-type', 'application/json']]),
      json: async () => ({ error: 'SBOM not found' })
    });

    await expect(client.getSBOM('missing')).rejects.toThrow('SBOM not found');
  });

  it('throws JSON success envelopes that carry errors', async () => {
    global.fetch.mockResolvedValueOnce({
      ok: true,
      headers: new Map([['content-type', 'application/json']]),
      json: async () => ({ error: 'Invalid request' })
    });

    await expect(client.searchSBOMPackages({ q: 'bad' })).rejects.toThrow('Invalid request');
  });

  it('returns null for non-JSON responses', async () => {
    global.fetch.mockResolvedValueOnce({
      ok: true,
      headers: new Map([['content-type', 'text/plain']]),
      json: async () => { throw new Error('Not JSON'); }
    });

    await expect(client.fetch('/some-endpoint')).resolves.toBeNull();
  });

  it('encodes artifact IDs in SBOM URLs', async () => {
    const artifactId = 'artifact/with/slashes';
    global.fetch.mockResolvedValueOnce(json({ id: artifactId }));

    await client.getSBOM(artifactId);

    expect(global.fetch).toHaveBeenCalledWith(`/api/v1/artifacts/${encodeURIComponent(artifactId)}/sbom`, expect.any(Object));
  });
});
