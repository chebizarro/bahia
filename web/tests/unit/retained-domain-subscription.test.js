import { describe, expect, it, vi } from 'vitest';
import {
  domainLiveFilters,
  subscribeToDomainRefresh
} from '../../src/lib/nostr/retained-domain-subscription.js';

describe('retained domain subscriptions', () => {
  it('uses canonical author- and domain-scoped observable filters', () => {
    expect(domainLiveFilters({
      domain: 'notifications',
      servicePubkey: 'b'.repeat(64)
    })).toEqual([{
      kinds: [30900, 4903, 30315, 30078],
      authors: ['b'.repeat(64)],
      '#domain': ['notifications'],
      limit: 500
    }]);
  });

  it('does not refresh during backfill and refreshes on events after EOSE', async () => {
    let handlers;
    const close = vi.fn();
    const refresh = vi.fn(async () => undefined);
    const client = {
      getConnectedRelays: () => ['wss://relay.example'],
      subscribeWithRecovery: vi.fn((_filters, nextHandlers) => {
        handlers = nextHandlers;
        return close;
      })
    };

    const unsubscribe = await subscribeToDomainRefresh({
      domain: 'orgs',
      servicePubkey: 'b'.repeat(64),
      refresh,
      client,
      connect: vi.fn(async () => undefined)
    });

    handlers.onEvent({ id: 'historical', kind: 4903 }, 'wss://relay.example');
    await Promise.resolve();
    expect(refresh).not.toHaveBeenCalled();

    handlers.onEose('wss://relay.example');
    expect(close).not.toHaveBeenCalled();

    handlers.onEvent({ id: 'live', kind: 4903 }, 'wss://relay.example');
    await Promise.resolve();
    await Promise.resolve();
    expect(refresh).toHaveBeenCalledOnce();
    expect(close).not.toHaveBeenCalled();

    unsubscribe();
    expect(close).toHaveBeenCalledOnce();
  });
});
