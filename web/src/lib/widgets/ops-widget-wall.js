import { getEventStore, onStoreRefresh } from '$lib/nostr/boot.js';
import { DASHBOARD_WIDGET } from '$lib/nostr/kinds.gen.js';
import { getOpsWidgetAllowedPubkeys, parseOpsWidgetPublisherAllowlist } from './ops-widget-config.js';
export { getOpsWidgetAllowedPubkeys, parseOpsWidgetPublisherAllowlist } from './ops-widget-config.js';

/** A store-query read model; the app layout owns its binding, not the widgets route. */
export function createOpsWidgetWall({
  allowedPubkeys = getOpsWidgetAllowedPubkeys(),
  eventStore = getEventStore,
  registerRefresh = onStoreRefresh
} = {}) {
  const authors = parseOpsWidgetPublisherAllowlist(allowedPubkeys);
  const filter = { kinds: [DASHBOARD_WIDGET], authors };
  const listeners = new Set();
  let events = [];
  let unbind = null;

  function refresh() {
    events = authors.length ? (eventStore()?.query(filter) || []) : [];
    for (const listener of listeners) listener(events);
  }

  function subscribe(listener) {
    listeners.add(listener);
    listener(events);
    return () => listeners.delete(listener);
  }

  function start() {
    if (unbind) return stop;
    refresh(); // Persisted, verified events are available before any relay connects.
    unbind = registerRefresh(refresh);
    return stop;
  }

  function stop() {
    unbind?.();
    unbind = null;
    events = [];
    for (const listener of listeners) listener(events);
  }

  return { allowedPubkeys: authors, filter, subscribe, start, stop, refresh };
}

export const opsWidgetWall = createOpsWidgetWall();
export const initOpsWidgetWallBinding = () => opsWidgetWall.start();
export const teardownOpsWidgetWallBinding = () => opsWidgetWall.stop();
