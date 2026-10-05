/**
 * Open one retained live REQ per relay and page backwards through the relay's
 * stored history. The live REQ starts at a captured high-water second so events
 * published while history is paging cannot fall through the gap. Historical
 * pages are inclusive and overlap at their oldest second; BahiaEventStore
 * ingestion deduplicates those overlaps.
 *
 * A cursor means "this relay/filter was complete through this second". It is
 * advanced only after every historical page reached EOSE. Once complete, later
 * live events advance it immediately. A page saturated by one timestamp cannot
 * be losslessly continued with NIP-01's second-resolution `until`; older pages
 * are still read, but the cursor remains uncommitted and completion is reported
 * as degraded so a later session retries that boundary.
 */
export function subscribeWithPagedBackfill({
  pool,
  store,
  relays,
  filter,
  key,
  pageSize = 500,
  now = () => Math.floor(Date.now() / 1000),
  onEose = () => {},
  onError = () => {}
}) {
  if (!pool || !store) throw new Error('Paged backfill requires a pool and event store');
  if (!Array.isArray(filter?.authors) || filter.authors.length === 0) {
    throw new Error('Paged backfill requires a non-empty trusted authors filter');
  }
  if (!Number.isInteger(pageSize) || pageSize < 1) throw new RangeError('pageSize must be positive');

  const handles = new Set();
  let stopped = false;

  const subscribe = (options) => {
    let handle;
    let closeAfterAssign = false;
    const close = () => {
      if (handle) {
        handle.unsubscribe();
        handles.delete(handle);
      } else {
        closeAfterAssign = true;
      }
    };
    handle = pool.subscribe(options);
    handles.add(handle);
    if (closeAfterAssign) close();
    return close;
  };

  for (const relay of [...new Set(relays || [])]) {
    const previousCursor = store.getCursor(relay, key);
    const highWater = Math.max(1, Number(now()) || 1);
    let newestSeen = previousCursor ?? 0;
    let historyComplete = false;
    let settled = false;
    let lossless = true;
    let lastUntil = highWater;
    let priorBoundaryIds = new Set();

    // Keep realtime delivery open independently of the bounded history pages.
    subscribe({
      relays: [relay],
      filters: [{ ...filter, since: highWater }],
      onEvent(event) {
        const createdAt = Number(event?.created_at || 0);
        newestSeen = Math.max(newestSeen, createdAt);
        if (historyComplete && lossless && createdAt > (store.getCursor(relay, key) ?? 0)) {
          store.setCursor(relay, key, createdAt);
        }
      },
      onClosed(reason, url, meta) {
        if (stopped || !meta?.terminal) return;
        onError(new Error(`Live subscription closed at ${url || relay}: ${reason || 'unknown reason'}`), relay);
      }
    });

    const finish = () => {
      if (settled) return;
      settled = true;
      historyComplete = true;
      if (lossless) store.setCursor(relay, key, Math.max(highWater, newestSeen));
      onEose(relay, lossless);
    };

    const openPage = (until) => {
      if (stopped) return;
      const ids = new Set();
      let oldest = Infinity;
      const pageFilter = { ...filter, until, limit: pageSize };
      if (previousCursor !== null) pageFilter.since = previousCursor;

      const close = subscribe({
        relays: [relay],
        filters: [pageFilter],
        onEvent(event) {
          if (!event?.id || ids.has(event.id)) return;
          ids.add(event.id);
          const createdAt = Number(event.created_at || 0);
          newestSeen = Math.max(newestSeen, createdAt);
          oldest = Math.min(oldest, createdAt);
        },
        onEose() {
          if (stopped) return;
          close();
          if (ids.size < pageSize || !Number.isFinite(oldest)) {
            finish();
            return;
          }

          // Inclusive `until` deliberately repeats the boundary second. If a
          // full page repeats entirely, the relay cannot expose the remaining
          // events at that second. Continue older without certifying history.
          if (oldest === lastUntil && [...ids].every(id => priorBoundaryIds.has(id))) {
            lossless = false;
            if (oldest <= 1 || (previousCursor !== null && oldest <= previousCursor)) {
              finish();
              return;
            }
            lastUntil = oldest - 1;
            priorBoundaryIds = new Set();
            openPage(lastUntil);
            return;
          }

          priorBoundaryIds = ids;
          lastUntil = oldest;
          openPage(oldest);
        },
        onClosed(reason, url, meta) {
          if (stopped || !meta?.terminal) return;
          close();
          lossless = false;
          historyComplete = true;
          settled = true;
          onError(new Error(`History incomplete at ${url || relay}: ${reason || 'unknown reason'}`), relay);
          onEose(relay, false);
        }
      });
    };

    openPage(highWater);
  }

  return () => {
    stopped = true;
    for (const handle of handles) handle.unsubscribe();
    handles.clear();
  };
}
