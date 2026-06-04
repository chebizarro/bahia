import { beforeEach, describe, expect, it, vi } from 'vitest';

import { BahiaClient } from '../../src/lib/api/client.js';

function response({ data, error, ok = true, status = 200, statusText = 'OK', contentType = 'application/json' } = {}) {
  return {
    ok,
    status,
    statusText,
    headers: new Map(contentType ? [['content-type', contentType]] : []),
    json: async () => ({ data, error })
  };
}

describe('BahiaClient core HTTP behavior', () => {
  let client;

  beforeEach(() => {
    global.fetch = vi.fn();
    client = new BahiaClient();
  });

  it('starts without bearer-token state', () => {
    expect(client.authProvider).toBeNull();
    expect(client.token).toBeUndefined();
  });

  it('serializes query parameters consistently', () => {
    expect(client.query({ q: 'openssl', tags: ['a', 'b'], enabled: true, empty: '', none: null })).toBe('?q=openssl&tags=a%2Cb&enabled=true');
    expect(client.query({})).toBe('');
    expect(client.query(null)).toBe('');
  });

  it('injects auth provider headers without storing bearer credentials', async () => {
    client.setAuthProvider({ getAuthorizationHeader: vi.fn().mockResolvedValue('Nostr signed-event') });
    global.fetch.mockResolvedValueOnce(response({ data: {} }));

    await client.fetch('/blossom/servers');

    expect(global.fetch).toHaveBeenCalledWith('/api/v1/blossom/servers', expect.objectContaining({
      method: 'GET',
      headers: expect.objectContaining({ Authorization: 'Nostr signed-event' })
    }));
  });

  it('does not inject authorization when no provider is configured', async () => {
    global.fetch.mockResolvedValueOnce(response({ data: {} }));

    await client.fetch('/blossom/servers');

    expect(global.fetch.mock.calls[0][1].headers).not.toHaveProperty('Authorization');
  });

  it('unwraps Bahia data envelopes and returns null for non-JSON responses', async () => {
    global.fetch
      .mockResolvedValueOnce(response({ data: { ok: true } }))
      .mockResolvedValueOnce(response({ contentType: 'text/plain' }));

    await expect(client.fetch('/sbom/search')).resolves.toEqual({ ok: true });
    await expect(client.fetch('/metrics')).resolves.toBeNull();
  });

  it('normalizes backend error envelopes and HTTP status failures', async () => {
    global.fetch
      .mockResolvedValueOnce(response({ ok: false, status: 404, statusText: 'Not Found', error: 'missing artifact' }))
      .mockResolvedValueOnce({
        ok: false,
        status: 500,
        statusText: 'Internal Server Error',
        headers: new Map([['content-type', 'text/plain']]),
        json: async () => { throw new Error('not json'); }
      });

    await expect(client.fetch('/artifacts/missing/sbom')).rejects.toThrow('missing artifact');
    await expect(client.fetch('/blossom/health', { retries: 0 })).rejects.toThrow('HTTP 500: Internal Server Error');
  });

  it('supports the live Blossom and SBOM route calls', async () => {
    global.fetch
      .mockResolvedValueOnce(response({ data: ['https://blossom.example'] }))
      .mockResolvedValueOnce(response({ data: { 'https://blossom.example': 'ok' } }))
      .mockResolvedValueOnce(response({ data: [{ sha256: 'abc' }] }))
      .mockResolvedValueOnce(response({ data: { bomFormat: 'CycloneDX' } }))
      .mockResolvedValueOnce(response({ data: { statement: 'attested' } }));

    await expect(client.getBlossomServers()).resolves.toEqual(['https://blossom.example']);
    await expect(client.checkBlossomHealth()).resolves.toEqual({ 'https://blossom.example': 'ok' });
    await expect(client.listBlossomBlobs()).resolves.toEqual([{ sha256: 'abc' }]);
    await expect(client.getSBOM('artifact-1')).resolves.toEqual({ bomFormat: 'CycloneDX' });
    await expect(client.getSBOMAttestation('artifact-1')).resolves.toEqual({ statement: 'attested' });
  });
});
