import { describe, it, expect, beforeEach, vi } from 'vitest';

vi.mock('../../src/lib/nostr/subscriptions.js', () => ({
  queryOrPartial: vi.fn(),
  readModelEvents: vi.fn((result) => Array.isArray(result) ? result : (result?.events || [])),
  attachReadModelMetadata: vi.fn((values, result) => {
    Object.defineProperty(values, 'eose', { value: result?.eose || null });
    return values;
  })
}));

describe('NIP-34 repository queries', () => {
  let subscriptions;
  let repositories;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();

    subscriptions = await import('../../src/lib/nostr/subscriptions.js');
    repositories = await import('../../src/lib/nostr/repositories.js');
  });

  it('queries repository announcements through explicit NIP-34 relay URLs', async () => {
    const event = {
      id: 'event-1',
      kind: 30617,
      pubkey: 'a'.repeat(64),
      created_at: 10,
      tags: [
        ['d', 'bahia'],
        ['name', 'Bahia'],
        ['clone', 'https://git.example/bahia.git'],
        ['relays', 'wss://repo-relay.example']
      ],
      content: ''
    };
    subscriptions.queryOrPartial.mockResolvedValue({ events: [event] });

    const result = await repositories.fetchRepositories({
      authors: ['a'.repeat(64)],
      relayUrls: ['wss://nip34.example', ' wss://nip34-backup.example ', 'wss://nip34.example']
    });

    expect(subscriptions.queryOrPartial).toHaveBeenCalledWith([
      {
        kinds: [30617],
        limit: 200,
        authors: ['a'.repeat(64)]
      }
    ], {
      scope: 'repositories',
      relays: ['wss://nip34.example', 'wss://nip34-backup.example']
    });
    expect(result).toHaveLength(1);
    expect(result[0]).toMatchObject({
      displayName: 'Bahia',
      repoCoordinate: `30617:${'a'.repeat(64)}:bahia`,
      relayUrls: ['wss://repo-relay.example']
    });
  });

  it('uses the global relay pool only when no NIP-34 relays are configured', async () => {
    subscriptions.queryOrPartial.mockResolvedValue({ events: [] });

    await repositories.fetchRepositories({ relayUrls: [] });

    expect(subscriptions.queryOrPartial).toHaveBeenCalledWith([
      { kinds: [30617], limit: 200 }
    ], { scope: 'repositories' });
  });
});
