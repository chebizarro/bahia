import { describe, it, expect, beforeEach, vi } from 'vitest';

const subscriptionsMock = vi.hoisted(() => ({
  relayEvents: [],
  nostr: {
    subscribe: vi.fn((_filters, handlers = {}) => {
      for (const event of subscriptionsMock.relayEvents) handlers.onEvent?.(event, 'wss://global.example');
      handlers.onEose?.('wss://global.example');
      return vi.fn();
    }),
    subscribeOnRelays: vi.fn((relays, _filters, handlers = {}) => {
      for (const event of subscriptionsMock.relayEvents) handlers.onEvent?.(event, relays[0]);
      for (const relay of relays) handlers.onEose?.(relay);
      return vi.fn();
    })
  }
}));

vi.mock('../../src/lib/nostr/subscriptions.js', () => subscriptionsMock);

describe('NIP-34 repository queries', () => {
  let subscriptions;
  let repositories;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    subscriptionsMock.relayEvents = [];

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
    subscriptionsMock.relayEvents = [event];

    const result = await repositories.fetchRepositories({
      authors: ['a'.repeat(64)],
      relayUrls: ['wss://nip34.example', ' wss://nip34-backup.example ', 'wss://nip34.example']
    });

    expect(subscriptions.nostr.subscribeOnRelays).toHaveBeenCalledWith(
      ['wss://nip34.example', 'wss://nip34-backup.example'],
      [
        {
          kinds: [30617],
          limit: 200,
          authors: ['a'.repeat(64)]
        }
      ],
      expect.objectContaining({
        onEvent: expect.any(Function),
        onEose: expect.any(Function),
        onClosed: expect.any(Function)
      })
    );
    expect(result).toHaveLength(1);
    expect(result[0]).toMatchObject({
      displayName: 'Bahia',
      repoCoordinate: `30617:${'a'.repeat(64)}:bahia`,
      relayUrls: ['wss://repo-relay.example']
    });
  });

  it('uses the global relay pool only when no NIP-34 relays are configured', async () => {
    await repositories.fetchRepositories({ relayUrls: [] });

    expect(subscriptions.nostr.subscribe).toHaveBeenCalledWith(
      [{ kinds: [30617], limit: 200 }],
      expect.objectContaining({
        onEvent: expect.any(Function),
        onEose: expect.any(Function),
        onClosed: expect.any(Function)
      })
    );
    expect(subscriptions.nostr.subscribeOnRelays).not.toHaveBeenCalled();
  });
});
