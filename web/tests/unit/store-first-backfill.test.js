import { describe, it, expect, vi } from 'vitest';
import { createPagedReader, subscribeWithPagedBackfill } from '../../src/lib/nostr/store-first-backfill.js';

const RELAY = 'wss://relay.test';
const AUTHOR = 'a'.repeat(64);
const trusted = { kinds: [30351], authors: [AUTHOR] };

/**
 * A relay double that answers each paged REQ the way NIP-01 requires: newest
 * first, inclusive `since`/`until`, at most min(limit, relayCap) events, then
 * EOSE. REQs without a limit are retained live subscriptions.
 */
function harness(events, { relayCap = Infinity, cursors = new Map() } = {}) {
  const requests = [];
  const delivered = new Set();
  const live = [];
  let open = 0;
  let maxOpen = 0;
  const store = {
    getCursor: (relay, key) => cursors.get(`${relay}\t${key}`) ?? null,
    setCursor: vi.fn((relay, key, value) => cursors.set(`${relay}\t${key}`, value))
  };
  const pool = { subscribe: vi.fn(({ relays: [relay], filters, onEvent, onEose, onClosed }) => {
    open += 1;
    maxOpen = Math.max(maxOpen, open);
    requests.push({ relay, filters });
    if (filters.some((filter) => filter.limit === undefined)) {
      live.push({ relay, filters, onEvent, onClosed });
    } else {
      const [filter] = filters;
      queueMicrotask(() => {
        const page = events
          .filter((event) => filter.kinds.includes(event.kind ?? 30351))
          .filter((event) => event.created_at <= filter.until && event.created_at >= (filter.since ?? 0))
          .sort((a, b) => b.created_at - a.created_at || a.id.localeCompare(b.id))
          .slice(0, Math.min(filter.limit, relayCap));
        for (const event of page) { delivered.add(event.id); onEvent(event, relay); }
        onEose(relay);
      });
    }
    return { unsubscribe: vi.fn(() => { open -= 1; }) };
  }) };
  const pages = () => requests.filter((request) => request.filters[0].limit !== undefined).map((request) => request.filters[0]);
  const cursor = (filter = trusted, key = 'model') => cursors.get(`${RELAY}\t${key}:${JSON.stringify(filter)}`);
  return { cursors, cursor, delivered, live, pages, pool, requests, store, maxOpen: () => maxOpen };
}

const history = (count, newest = 2000) =>
  Array.from({ length: count }, (_, index) => ({ id: `e-${String(index).padStart(5, '0')}`, created_at: newest - index }));

function run(ctx, options = {}) {
  const completed = vi.fn();
  const failed = vi.fn();
  const stop = subscribeWithPagedBackfill({
    pool: ctx.pool, store: ctx.store, relays: [RELAY], filters: [trusted], key: 'model',
    pageSize: 500, overlapSeconds: 300, now: () => 2000, onEose: completed, onError: failed, ...options
  });
  return { completed, failed, stop };
}

describe('store-first paged backfill', () => {
  it('reads history beyond 1,000 events and commits the cursor only once every page reached EOSE', async () => {
    const ctx = harness(history(1205));
    const { completed } = run(ctx);
    expect(ctx.store.setCursor).not.toHaveBeenCalled();
    await vi.waitFor(() => expect(completed).toHaveBeenCalledWith(RELAY, true));
    expect(ctx.delivered.size).toBe(1205);
    expect(ctx.pages().length).toBeGreaterThan(3);
    expect(ctx.pages().every((filter) => filter.limit === 500 && filter.authors[0] === AUTHOR)).toBe(true);
    // Complete through the session start, trailing by the overlap window.
    expect(ctx.cursor()).toBe(1700);
  });

  it('keeps paging when the relay caps pages below the requested limit', async () => {
    const ctx = harness(history(1205), { relayCap: 100 });
    const { completed } = run(ctx);
    await vi.waitFor(() => expect(completed).toHaveBeenCalledWith(RELAY, true));
    expect(ctx.delivered.size).toBe(1205);
    expect(ctx.cursor()).toBe(1700);
  });

  it('pages only the gap since a stored cursor and lets live events advance it, never past the local clock', async () => {
    const cursors = new Map([[`${RELAY}\tmodel:${JSON.stringify(trusted)}`, 900]]);
    const ctx = harness(history(1205), { cursors });
    let clock = 2000;
    const { completed } = run(ctx, { now: () => clock });
    await vi.waitFor(() => expect(completed).toHaveBeenCalledWith(RELAY, true));
    expect(ctx.pages().every((filter) => filter.since === 900)).toBe(true);
    expect(ctx.delivered.size).toBe(1101); // created_at 900..2000 inclusive
    expect(ctx.cursor()).toBe(1700);

    clock = 2600;
    ctx.live[0].onEvent({ id: 'live', created_at: 2500 }, RELAY);
    expect(ctx.cursor()).toBe(2200);
    ctx.live[0].onEvent({ id: 'future-dated', created_at: 99999 }, RELAY);
    expect(ctx.cursor()).toBe(2300);
  });

  it('starts the live REQ before the session and does not move the cursor while history is still paging', async () => {
    const ctx = harness(history(600));
    const { completed } = run(ctx);
    expect(ctx.live[0].filters).toEqual([{ ...trusted, since: 1700 }]);
    ctx.live[0].onEvent({ id: 'early-live', created_at: 2000 }, RELAY);
    expect(ctx.store.setCursor).not.toHaveBeenCalled();
    await vi.waitFor(() => expect(completed).toHaveBeenCalled());
  });

  it('does not certify a second that holds more events than one page', async () => {
    const sameSecond = Array.from({ length: 501 }, (_, index) => ({ id: `same-${String(index).padStart(3, '0')}`, created_at: 1500 }));
    const ctx = harness([...sameSecond, { id: 'older', created_at: 1400 }]);
    const { completed } = run(ctx);
    await vi.waitFor(() => expect(completed).toHaveBeenCalledWith(RELAY, false));
    expect(ctx.delivered.has('older')).toBe(true); // older history is still read
    expect(ctx.store.setCursor).not.toHaveBeenCalled();
  });

  it('detects a capped relay hiding events inside one second', async () => {
    const sameSecond = Array.from({ length: 150 }, (_, index) => ({ id: `same-${String(index).padStart(3, '0')}`, created_at: 1500 }));
    const ctx = harness([...sameSecond, { id: 'older', created_at: 1400 }], { relayCap: 100 });
    const { completed } = run(ctx);
    await vi.waitFor(() => expect(completed).toHaveBeenCalledWith(RELAY, false));
    expect(ctx.store.setCursor).not.toHaveBeenCalled();
  });

  it('certifies a small history whose events share one second', async () => {
    const ctx = harness([1, 2, 3].map((index) => ({ id: `same-${index}`, created_at: 1500 })));
    const { completed } = run(ctx);
    await vi.waitFor(() => expect(completed).toHaveBeenCalledWith(RELAY, true));
    expect(ctx.cursor()).toBe(1700);
  });

  it('stops paging a relay that ignores until and does not certify it', async () => {
    const events = history(3);
    const store = { getCursor: () => null, setCursor: vi.fn() };
    // Returns every stored event for any REQ, whatever `until` says.
    const pool = { subscribe: vi.fn(({ filters, onEvent, onEose }) => {
      if (filters[0].limit !== undefined) queueMicrotask(() => { for (const event of events) onEvent(event, RELAY); onEose(RELAY); });
      return { unsubscribe: vi.fn() };
    }) };
    const completed = vi.fn();
    subscribeWithPagedBackfill({ pool, store, relays: [RELAY], filters: [trusted], key: 'model', now: () => 2000, onEose: completed });
    await vi.waitFor(() => expect(completed).toHaveBeenCalledWith(RELAY, false));
    // One live REQ, the first page, and the one page that exposed the relay.
    expect(pool.subscribe).toHaveBeenCalledTimes(3);
    expect(store.setCursor).not.toHaveBeenCalled();
  });

  it('shares one live REQ across filters and pages them one REQ at a time', async () => {
    const second = { kinds: [30353], authors: [AUTHOR] };
    const ctx = harness([...history(700), ...history(700, 1900).map((event) => ({ ...event, id: `p-${event.id}`, kind: 30353 }))]);
    const { completed, stop } = run(ctx, { filters: [trusted, second] });
    await vi.waitFor(() => expect(completed).toHaveBeenCalledWith(RELAY, true));
    expect(ctx.live).toHaveLength(1);
    expect(ctx.live[0].filters.map((filter) => filter.kinds[0])).toEqual([30351, 30353]);
    expect(ctx.maxOpen()).toBe(2);
    expect(ctx.delivered.size).toBe(1400);
    expect(ctx.cursor(trusted)).toBe(1700);
    expect(ctx.cursor(second)).toBe(1700);
    stop();
    expect(ctx.pool.subscribe.mock.results.every((result) => result.value.unsubscribe.mock.calls.length === 1)).toBe(true);
  });

  it('reports a terminal CLOSED during history as incomplete and leaves the cursor alone', () => {
    const store = { getCursor: () => null, setCursor: vi.fn() };
    const handlers = [];
    const pool = { subscribe: vi.fn((options) => { handlers.push(options); return { unsubscribe: vi.fn() }; }) };
    const completed = vi.fn();
    const failed = vi.fn();
    subscribeWithPagedBackfill({ pool, store, relays: [RELAY], filters: [trusted], key: 'model', onEose: completed, onError: failed });
    const page = handlers.find((options) => options.filters[0].limit !== undefined);
    page.onClosed('relay disconnected', RELAY, { disconnected: true });
    expect(failed).not.toHaveBeenCalled(); // the pool re-issues the REQ after a reconnect
    page.onClosed('restricted: not allowed', RELAY, { terminal: true });
    expect(failed).toHaveBeenCalledWith(expect.objectContaining({ message: expect.stringContaining('restricted') }), RELAY);
    expect(completed).toHaveBeenCalledWith(RELAY, false);
    expect(store.setCursor).not.toHaveBeenCalled();
  });

  it('refuses to open a subscription without trusted authors', () => {
    const base = { pool: { subscribe: vi.fn() }, store: { getCursor: vi.fn() }, relays: [RELAY], key: 'model' };
    expect(() => subscribeWithPagedBackfill({ ...base, filters: [{ kinds: [30351] }] })).toThrow('trusted authors');
    expect(() => subscribeWithPagedBackfill({ ...base, filters: [trusted, { kinds: [1], authors: [] }] })).toThrow('trusted authors');
    expect(() => subscribeWithPagedBackfill({ ...base, filters: [] })).toThrow('at least one filter');
    expect(base.pool.subscribe).not.toHaveBeenCalled();
  });
});

describe('paged reader', () => {
  const reader = (ctx, onChange = vi.fn()) =>
    ({ onChange, reader: createPagedReader({ name: 'model', pool: ctx.pool, store: ctx.store, relays: [RELAY], onChange }) });

  it('reports catch-up as metadata and keeps a unit running until its filters change', async () => {
    const ctx = harness(history(3));
    const { reader: model } = reader(ctx);
    const units = [{ name: 'service', filters: [trusted] }, { name: 'operator', filters: [] }];
    model.sync(units);
    expect(model.metadata()).toMatchObject({ complete: false, settled: false, degraded: { reason: 'catching-up' } });
    await vi.waitFor(() => expect(model.metadata().complete).toBe(true));
    expect(model.metadata().relaySummary).toEqual([{ unit: 'service', relay: RELAY, status: 'eose', reason: '' }]);

    const opened = ctx.pool.subscribe.mock.calls.length;
    model.sync(units);
    expect(ctx.pool.subscribe).toHaveBeenCalledTimes(opened);

    // A newly trusted author starts that unit; the unchanged one keeps its REQs.
    const operator = { kinds: [31400], authors: ['b'.repeat(64)] };
    model.sync([units[0], { name: 'operator', filters: [operator] }]);
    expect(ctx.requests.slice(opened).every((request) => request.filters[0].authors[0] === 'b'.repeat(64))).toBe(true);
    expect(model.metadata().settled).toBe(false);

    // Signing out withdraws the operator unit and closes only its REQs.
    model.sync(units);
    expect(model.metadata().relaySummary.map((state) => state.unit)).toEqual(['service']);
    model.stop();
    expect(model.metadata().relaySummary).toEqual([]);
  });

  it('surfaces a terminal relay closure without marking catch-up complete', () => {
    const handlers = [];
    const pool = { subscribe: vi.fn((options) => { handlers.push(options); return { unsubscribe: vi.fn() }; }) };
    const onChange = vi.fn();
    const model = createPagedReader({ name: 'model', pool, store: { getCursor: () => null, setCursor: vi.fn() }, relays: [RELAY], onChange });
    model.sync([{ name: 'service', filters: [trusted] }]);
    handlers.find((options) => options.filters[0].limit !== undefined).onClosed('blocked: nope', RELAY, { terminal: true });
    expect(model.metadata()).toMatchObject({ complete: false, settled: true });
    expect(model.metadata().degraded.reason).toContain('blocked');
    expect(onChange).toHaveBeenCalledWith(expect.any(Error));
  });
});
