/**
 * Cursor-backed, paged history for store-first read models.
 *
 * The shared pool's `filterKey` cursor only moves a retained REQ's `since`
 * forward and keeps the caller's fixed `limit`, so a relay that caps a REQ
 * silently truncates anything older than its newest N events. This helper
 * layers `until` paging on the same pool, store and cursor table.
 *
 * Per relay it opens one retained live REQ carrying every filter, then walks
 * each filter's stored history backwards one page REQ at a time (at most two
 * concurrent REQs per relay). All events are verified and ingested by the pool;
 * BahiaEventStore deduplicates the deliberate overlaps.
 *
 * Paging never trusts a page's size, because a relay may cap `limit` below the
 * requested page size: a filter is finished only when a page returns nothing.
 * `until` is inclusive, so each page repeats its oldest second to pick up
 * events the previous limit cut off, then steps below it.
 *
 * A cursor means "this relay was complete for this filter through this
 * second". It is committed only after every page reached EOSE, is derived from
 * the session start or relay-delivered timestamps, never runs ahead of the
 * local clock, and trails by `overlapSeconds` so clock skew and late-arriving
 * events are re-read rather than skipped. A second holding more events than a
 * relay returns in one page cannot be paged with NIP-01's second-resolution
 * `until`. That shows up as a page lying entirely on its boundary second that
 * is either full or has older history below it (the relay stopped short of the
 * requested limit). Older history is still read, but that filter's cursor
 * stays uncommitted and completion is reported as incomplete so a later
 * session retries the boundary. A relay that ignores `until` is treated the same
 * way and is not asked for further pages.
 *
 * @param {object} options
 * @param {{ subscribe: Function }} options.pool
 * @param {{ getCursor: Function, setCursor: Function }} options.store
 * @param {string[]} options.relays
 * @param {import('./store-interface.js').Filter[]} options.filters  Each must name trusted authors.
 * @param {string} options.key  Read-model name; cursors are keyed by it plus the exact filter.
 * @param {number} [options.pageSize]
 * @param {number} [options.overlapSeconds]
 * @param {() => number} [options.now]  Unix seconds.
 * @param {(relay: string, complete: boolean) => void} [options.onEose]  History catch-up finished for a relay.
 * @param {(error: Error, relay: string) => void} [options.onError]
 * @returns {() => void} stop
 */
export function subscribeWithPagedBackfill({
  pool,
  store,
  relays,
  filters,
  key,
  pageSize = 500,
  overlapSeconds = 300,
  now = () => Math.floor(Date.now() / 1000),
  onEose = () => {},
  onError = () => {}
}) {
  if (!pool || !store) throw new Error('Paged backfill requires a pool and event store');
  if (!Array.isArray(filters) || filters.length === 0) throw new Error('Paged backfill requires at least one filter');
  for (const filter of filters) {
    if (!Array.isArray(filter?.authors) || filter.authors.length === 0) {
      throw new Error('Paged backfill requires a non-empty trusted authors filter');
    }
  }
  if (!Number.isInteger(pageSize) || pageSize < 1) throw new RangeError('pageSize must be positive');

  const handles = new Set();
  let stopped = false;

  const subscribe = (options) => {
    const handle = pool.subscribe(options);
    handles.add(handle);
    return () => {
      handle.unsubscribe();
      handles.delete(handle);
    };
  };
  const cursorKeys = filters.map((filter) => `${key}:${JSON.stringify(filter)}`);

  for (const relay of [...new Set(relays || [])]) {
    const sessionStart = Math.max(1, Number(now()) || 1);
    const previous = cursorKeys.map((cursorKey) => store.getCursor(relay, cursorKey));
    const lossless = filters.map(() => true);
    let historyComplete = false;
    let failed = false;

    const commit = (index, second) => {
      const value = Math.min(second, Number(now()) || sessionStart) - overlapSeconds;
      if (value > (store.getCursor(relay, cursorKeys[index]) ?? 0)) store.setCursor(relay, cursorKeys[index], value);
    };

    // The live REQ starts before the session so events published while history
    // pages, or stamped by a slightly slower clock, are still delivered.
    subscribe({
      relays: [relay],
      filters: filters.map((filter) => ({ ...filter, since: Math.max(1, sessionStart - overlapSeconds) })),
      onEvent(event) {
        if (!historyComplete) return;
        const createdAt = Number(event?.created_at || 0);
        filters.forEach((_, index) => { if (lossless[index]) commit(index, createdAt); });
      },
      onClosed(reason, url, meta) {
        if (stopped || !meta?.terminal) return;
        onError(new Error(`Live subscription closed at ${url || relay}: ${reason || 'unknown reason'}`), relay);
      }
    });

    const finish = () => {
      historyComplete = true;
      filters.forEach((_, index) => { if (lossless[index]) commit(index, sessionStart); });
      onEose(relay, lossless.every(Boolean));
    };

    // `belowFullSecond` is set while probing beneath a page whose events all
    // shared one second: finding anything there proves the relay cut that
    // page short, so events may be hidden at that second.
    const pageFilter = (index, until, belowFullSecond = false) => {
      if (stopped || failed) return;
      if (index >= filters.length) { finish(); return; }
      const since = previous[index];
      if (until < 1 || (since !== null && until < since)) { pageFilter(index + 1, sessionStart); return; }

      const ids = new Set();
      let oldest = Infinity;
      let ignoresUntil = false;
      let settled = false;
      const request = { ...filters[index], until, limit: pageSize };
      if (since !== null) request.since = since;

      const close = subscribe({
        relays: [relay],
        filters: [request],
        onEvent(event) {
          if (settled || !event?.id || ids.has(event.id)) return;
          ids.add(event.id);
          const createdAt = Number(event.created_at || 0);
          oldest = Math.min(oldest, createdAt);
          if (createdAt > until) ignoresUntil = true;
        },
        onEose() {
          if (settled || stopped) return;
          settled = true;
          close();
          if (ids.size === 0) { pageFilter(index + 1, sessionStart); return; }
          if (ignoresUntil) {
            // The relay answered with events newer than `until`, so it cannot
            // be paged. Keep what it sent, never certify it, and stop asking.
            lossless[index] = false;
            pageFilter(index + 1, sessionStart);
            return;
          }
          if (belowFullSecond) lossless[index] = false;
          if (oldest < until) {
            // The limit may have cut `oldest` short; repeat that second.
            pageFilter(index, oldest);
            return;
          }
          if (ids.size >= pageSize) lossless[index] = false;
          pageFilter(index, until - 1, true);
        },
        onClosed(reason, url, meta) {
          if (settled || stopped || !meta?.terminal) return;
          settled = true;
          failed = true;
          close();
          lossless.fill(false);
          onError(new Error(`History incomplete at ${url || relay}: ${reason || 'unknown reason'}`), relay);
          onEose(relay, false);
        }
      });
    };

    pageFilter(0, sessionStart);
  }

  return () => {
    stopped = true;
    for (const handle of handles) handle.unsubscribe();
    handles.clear();
  };
}

/**
 * Keeps a named read model's paged subscriptions in step with its trusted
 * filter units. A unit is restarted (with fresh cursors) only when its filters
 * change, for example when the signed-in operator or a derived author set does.
 *
 * @param {object} options
 * @param {string} options.name
 * @param {{ subscribe: Function }} options.pool
 * @param {{ getCursor: Function, setCursor: Function }} options.store
 * @param {string[]} options.relays
 * @param {number} [options.pageSize]
 * @param {(error?: Error) => void} [options.onChange]  Catch-up state changed.
 */
export function createPagedReader({ name, pool, store, relays, pageSize, onChange = () => {} }) {
  /** @type {Map<string, { unit: string, stop: () => void, states: Map<string, { status: string, reason: string }> }>} */
  const active = new Map();

  /** @param {{ name: string, filters: import('./store-interface.js').Filter[] }[]} units */
  function sync(units) {
    const wanted = new Map(units
      .filter((unit) => unit.filters.length > 0)
      .map((unit) => [`${unit.name}:${JSON.stringify(unit.filters)}`, unit]));
    let changed = false;
    for (const [id, entry] of active) {
      if (wanted.has(id)) continue;
      entry.stop();
      active.delete(id);
      changed = true;
    }
    for (const [id, unit] of wanted) {
      if (active.has(id) || relays.length === 0) continue;
      const states = new Map(relays.map((relay) => [relay, { status: 'pending', reason: '' }]));
      const entry = { unit: unit.name, states, stop: () => {} };
      active.set(id, entry);
      changed = true;
      entry.stop = subscribeWithPagedBackfill({
        pool, store, relays, filters: unit.filters, key: `${name}:${unit.name}`, ...(pageSize ? { pageSize } : {}),
        onEose: (relay, complete) => {
          if (states.get(relay)?.status !== 'closed') {
            states.set(relay, complete ? { status: 'eose', reason: '' } : { status: 'incomplete', reason: 'paged-boundary' });
          }
          onChange();
        },
        onError: (error, relay) => {
          states.set(relay, { status: 'closed', reason: error.message });
          onChange(error);
        }
      });
    }
    if (changed) onChange();
  }

  /** Read-model catch-up metadata in the shape pages already consume. */
  function metadata() {
    const relaySummary = [...active.values()].flatMap((entry) =>
      [...entry.states].map(([relay, state]) => ({ unit: entry.unit, relay, ...state })));
    const complete = relaySummary.length > 0 && relaySummary.every((state) => state.status === 'eose');
    const settled = relaySummary.length > 0 && relaySummary.every((state) => state.status !== 'pending');
    const failed = relaySummary.find((state) => state.status === 'closed' || state.status === 'incomplete');
    return {
      complete,
      settled,
      degraded: complete ? null : { incomplete: true, reason: failed?.reason || 'catching-up', relaySummary },
      relaySummary
    };
  }

  function stop() {
    for (const entry of active.values()) entry.stop();
    active.clear();
  }

  return { sync, metadata, stop };
}
