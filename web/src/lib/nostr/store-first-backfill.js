/**
 * Retained live REQ plus bounded backwards history pages. A cursor is persisted
 * only after the complete historical range has reached an empty/short page.
 * NIP-01 `until` has second precision: a full page at its oldest second may
 * hide more events at that second. We continue older, but never certify or
 * persist a completion cursor across that ambiguous boundary.
 */
export function subscribeWithPagedBackfill({ pool, store, relays, filter, key, pageSize = 500, onEose = () => {}, onError = () => {} }) {
  const handles = new Set();
  let stopped = false;
  for (const relay of relays) {
    const cursor = store.getCursor(relay, key);
    let complete = true;
    let newest = cursor || 0;
    let lastUntil = Infinity;
    let boundaryIds = new Set();
    const open = (until, live = false) => {
      if (stopped) return;
      const ids = new Set();
      let oldest = Infinity;
      let handle;
      let closeAfterAssign = false;
      const close = () => {
        if (handle) { handle.unsubscribe(); handles.delete(handle); }
        else closeAfterAssign = true;
      };
      const pageFilter = { ...filter, limit: pageSize };
      if (until !== null) pageFilter.until = until;
      if (live && cursor !== null) pageFilter.since = cursor;
      handle = pool.subscribe({
        relays: [relay], filters: [pageFilter],
        onEvent(event) {
          if (!event?.id || ids.has(event.id)) return;
          ids.add(event.id);
          newest = Math.max(newest, event.created_at || 0);
          const time = event.created_at || 0;
          if (time < oldest) oldest = time;
        },
        onEose() {
          if (stopped) return;
          if (!live) close();
          if (cursor !== null) { onEose?.(relay, true); return; }
          if (ids.size < pageSize) {
            if (complete && newest) store.setCursor(relay, key, newest);
            onEose?.(relay, complete);
            return;
          }
          // Requery the boundary second on the next page. If that page is
          // saturated by already-seen ids, mark incomplete rather than loop.
          const nextUntil = oldest;
          if (oldest <= 1) { onEose?.(relay, false); return; }
          if (nextUntil > lastUntil || (nextUntil === lastUntil && [...ids].every(id => boundaryIds.has(id)))) {
            complete = false;
            // Relay cannot page within this timestamp. Skip it to continue
            // older history, but never persist a completion cursor.
            lastUntil = oldest - 1;
            open(lastUntil);
            return;
          }
          boundaryIds = ids;
          lastUntil = nextUntil;
          open(nextUntil);
        },
        onClosed(reason, url, meta) {
          if (!meta?.terminal || stopped) return;
          if (!live) close();
          onError?.(new Error(`History incomplete at ${url}: ${reason}`));
        }
      });
      handles.add(handle);
      if (closeAfterAssign) close();
    };
    // The first REQ stays live. Historical pages use separate bounded REQs.
    open(null, true);
  }
  return () => {
    stopped = true;
    for (const handle of handles) handle.unsubscribe();
    handles.clear();
  };
}
