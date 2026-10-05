import { describe, it, expect, vi } from 'vitest';
import { subscribeWithPagedBackfill } from '../../src/lib/nostr/store-first-backfill.js';

function harness(events, initialCursor = null) {
  const cursors = new Map(initialCursor === null ? [] : [['wss://relay.test:continuity-test', initialCursor]]);
  const filters = [];
  let liveHandlers = null;
  const store = {
    getCursor: (relay, key) => cursors.get(`${relay}:${key}`) ?? null,
    setCursor: (relay, key, value) => cursors.set(`${relay}:${key}`, value)
  };
  const pool = { subscribe: vi.fn(({ filters: [filter], onEvent, onEose, onClosed }) => {
    filters.push(filter);
    if (filter.limit === undefined) {
      liveHandlers = { onEvent, onEose, onClosed };
    } else {
      queueMicrotask(() => {
        const page = events
          .filter((event) => event.created_at <= filter.until && event.created_at >= (filter.since ?? 0))
          .slice(0, filter.limit);
        for (const item of page) onEvent(item);
        onEose('wss://relay.test');
      });
    }
    return { unsubscribe: vi.fn() };
  }) };
  return { cursors, filters, live: () => liveHandlers, pool, store };
}

const trustedFilter = { kinds: [30351], authors: ['a'.repeat(64)] };

describe('store-first paged backfill', () => {
  it('reads more than 1,000 events before persisting a completion cursor', async () => {
    const events = Array.from({ length: 1205 }, (_, index) => ({ id: `e-${index}`, created_at: 2000 - index }));
    const ctx = harness(events);
    const completed = vi.fn();
    const stop = subscribeWithPagedBackfill({
      pool: ctx.pool, store: ctx.store, relays: ['wss://relay.test'], filter: trustedFilter,
      key: 'continuity-test', pageSize: 500, now: () => 2000, onEose: completed
    });
    await vi.waitFor(() => expect(completed).toHaveBeenCalledWith('wss://relay.test', true));
    expect(ctx.filters.filter((filter) => filter.limit === 500)).toHaveLength(3);
    expect(ctx.cursors.get('wss://relay.test:continuity-test')).toBe(2000);
    stop();
  });

  it('pages a cursor-backed offline gap larger than one page and advances on later live events', async () => {
    const events = Array.from({ length: 1205 }, (_, index) => ({ id: `e-${index}`, created_at: 2000 - index }));
    const ctx = harness(events, 500);
    const completed = vi.fn();
    subscribeWithPagedBackfill({
      pool: ctx.pool, store: ctx.store, relays: ['wss://relay.test'], filter: trustedFilter,
      key: 'continuity-test', pageSize: 500, now: () => 2000, onEose: completed
    });
    await vi.waitFor(() => expect(completed).toHaveBeenCalledWith('wss://relay.test', true));
    expect(ctx.filters.filter((filter) => filter.limit === 500).length).toBeGreaterThan(2);
    expect(ctx.filters.find((filter) => filter.limit === 500)?.since).toBe(500);
    ctx.live().onEvent({ id: 'live', created_at: 2001 });
    expect(ctx.cursors.get('wss://relay.test:continuity-test')).toBe(2001);
  });

  it('does not certify a timestamp boundary that the relay cannot page losslessly', async () => {
    const events = Array.from({ length: 501 }, (_, index) => ({ id: `same-${index}`, created_at: 2000 }));
    const ctx = harness(events);
    const completed = vi.fn();
    subscribeWithPagedBackfill({
      pool: ctx.pool, store: ctx.store, relays: ['wss://relay.test'], filter: trustedFilter,
      key: 'continuity-test', pageSize: 500, now: () => 2000, onEose: completed
    });
    await vi.waitFor(() => expect(completed).toHaveBeenCalledWith('wss://relay.test', false));
    expect(ctx.cursors.has('wss://relay.test:continuity-test')).toBe(false);
  });

  it('refuses to open an unscoped author subscription', () => {
    expect(() => subscribeWithPagedBackfill({
      pool: { subscribe: vi.fn() }, store: { getCursor: vi.fn() }, relays: ['wss://relay.test'],
      filter: { kinds: [30351] }, key: 'continuity-test'
    })).toThrow('trusted authors');
  });
});
