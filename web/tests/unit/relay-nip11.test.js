import { describe, it, expect, vi } from 'vitest';
import { createRelayLimitResolver, fetchRelayMetadata, inlineSBOMLimitBytes, limitsFromMetadata } from '../../src/lib/nostr/relay-nip11.js';

describe('NIP-11 relay limits', () => {
  it('resolves both advertised fields and caches one fetch per relay', async () => {
    const url = 'wss://limits.example.test';
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ limitation: { max_message_length: 262144, max_content_length: 32768 } }) });
    const resolver = createRelayLimitResolver(relay => fetchRelayMetadata(relay, fetchMock));
    expect(await resolver.resolve([url, url])).toEqual({ maxMessageBytes: 262144, maxContentBytes: 32768 });
    expect(await resolver.resolve([url])).toEqual({ maxMessageBytes: 262144, maxContentBytes: 32768 });
    expect(fetchMock).toHaveBeenCalledOnce();
    expect(fetchMock.mock.calls[0][0]).toBe('https://limits.example.test');
    expect(fetchMock.mock.calls[0][1].headers.Accept).toBe('application/nostr+json');
    expect(inlineSBOMLimitBytes(resolver.cached([url]))).toBe(181248);
  });

  it('falls back for missing or invalid fields and malformed documents', () => {
    expect(limitsFromMetadata({ limitation: { max_message_length: 120000 } })).toEqual({ maxMessageBytes: 120000, maxContentBytes: 65535 });
    expect(limitsFromMetadata({ limitation: { max_message_length: '123', max_content_length: -1 } })).toEqual({ maxMessageBytes: 512000, maxContentBytes: 65535 });
    expect(limitsFromMetadata(null)).toEqual({ maxMessageBytes: 512000, maxContentBytes: 65535 });
    expect(inlineSBOMLimitBytes(limitsFromMetadata(null))).toBe(360 * 1024);
  });

  it('uses fallback limits when fetch fails or returns malformed JSON', async () => {
    const failed = createRelayLimitResolver(relay => fetchRelayMetadata(relay, vi.fn().mockRejectedValue(new Error('offline'))));
    expect(await failed.resolve(['ws://offline.example.test'])).toEqual({ maxMessageBytes: 512000, maxContentBytes: 65535 });
    const malformed = createRelayLimitResolver(relay => fetchRelayMetadata(relay, vi.fn().mockResolvedValue({ ok: true, json: async () => [] })));
    expect(await malformed.resolve(['wss://malformed.example.test'])).toEqual({ maxMessageBytes: 512000, maxContentBytes: 65535 });
    const rejected = createRelayLimitResolver(async () => { throw new Error('aborted'); });
    await expect(rejected.resolve(['wss://aborted.example.test'])).resolves.toEqual({ maxMessageBytes: 512000, maxContentBytes: 65535 });
  });
});
