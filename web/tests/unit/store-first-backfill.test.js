import { describe, it, expect, vi } from 'vitest';
import { subscribeWithPagedBackfill } from '../../src/lib/nostr/store-first-backfill.js';

describe('store-first paged backfill', () => {
  it('reads more than 1,000 events before persisting a completion cursor', async () => {
    const events = Array.from({ length: 1205 }, (_, index) => ({ id: `e-${index}`, created_at: 2000 - index }));
    const cursors = new Map();
    const store = {
      getCursor: (relay, key) => cursors.get(`${relay}:${key}`) ?? null,
      setCursor: (relay, key, value) => cursors.set(`${relay}:${key}`, value)
    };
    const filters = [];
    const pool = { subscribe: vi.fn(({ filters: [filter], onEvent, onEose }) => {
      filters.push(filter);
      queueMicrotask(() => {
        for (const item of events.filter((event) => event.created_at <= (filter.until ?? Infinity)).slice(0, filter.limit)) onEvent(item);
        onEose('wss://relay.test');
      });
      return { unsubscribe: vi.fn() };
    }) };
    const completed = vi.fn();
    const stop = subscribeWithPagedBackfill({ pool, store, relays: ['wss://relay.test'], filter: { kinds: [30351], authors: ['a'.repeat(64)] }, key: 'continuity-test', pageSize: 500, onEose: completed });
    await vi.waitFor(() => expect(completed).toHaveBeenCalledWith('wss://relay.test', true));
    expect(filters.length).toBeGreaterThan(2);
    expect(cursors.get('wss://relay.test:continuity-test')).toBe(2000);
    stop();
  });
});
