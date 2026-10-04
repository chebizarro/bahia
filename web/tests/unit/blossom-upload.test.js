import { describe, it, expect, vi } from 'vitest';
import { blossomHashFromUrl, blossomLocation, uploadSBOMToBlossom } from '../../src/lib/nostr/blossom-upload.js';

vi.mock('$lib/stores/auth.js', () => ({ signWithAuth: vi.fn() }));
vi.mock('$lib/stores/system.svelte.js', () => ({ currentSystemInfo: vi.fn(() => null) }));

const hash = 'a'.repeat(64);

describe('Blossom SBOM locations and uploads', () => {
  it('uses the URL hash as the optional SHA-256 contract', () => {
    expect(blossomHashFromUrl(`https://blossom.example/${hash}.json`)).toBe(hash);
    expect(blossomLocation(`https://blossom.example/${hash}`, `sha256:${hash}`)).toEqual({ type: 'blossom', uri: `https://blossom.example/${hash}` });
    expect(() => blossomLocation(`https://blossom.example/${hash}`, 'b'.repeat(64))).toThrow('must match');
    expect(() => blossomLocation('file:///tmp/sbom')).toThrow('valid Blossom');
  });

  it('signs a BUD-11 upload with the active signer and verifies the descriptor', async () => {
    const file = new File(['SBOM'], 'sbom.json', { type: 'application/json' });
    const digest = Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', await file.arrayBuffer())), value => value.toString(16).padStart(2, '0')).join('');
    const sign = vi.fn(async event => ({ ...event, id: 'signed', sig: 'signature', pubkey: 'f'.repeat(64) }));
    const fetchImpl = vi.fn(async () => ({ ok: true, json: async () => ({ url: `https://blossom.example/${digest}`, sha256: digest, size: 4 }) }));
    expect(await uploadSBOMToBlossom(file, { servers: ['https://blossom.example'], sign, fetchImpl })).toEqual({ type: 'blossom', uri: `https://blossom.example/${digest}`, mediaType: 'application/json' });
    expect(sign.mock.calls[0][0]).toMatchObject({ kind: 24242, tags: expect.arrayContaining([['t', 'upload'], ['x', digest]]) });
    expect(fetchImpl.mock.calls[0][0]).toBe('https://blossom.example/upload');
    expect(fetchImpl.mock.calls[0][1]).toMatchObject({ method: 'PUT', headers: { 'X-SHA-256': digest, 'Content-Type': 'application/json' } });
    expect(fetchImpl.mock.calls[0][1].headers.Authorization).toMatch(/^Nostr /);
  });

  it('rejects a descriptor with a different hash', async () => {
    const file = new File(['SBOM'], 'sbom.json', { type: 'application/json' });
    await expect(uploadSBOMToBlossom(file, { servers: ['https://blossom.example'], sign: vi.fn(async event => event),
      fetchImpl: vi.fn(async () => ({ ok: true, json: async () => ({ url: `https://blossom.example/${hash}`, sha256: hash, size: 4 }) }))
    })).rejects.toThrow('does not match');
  });
});
