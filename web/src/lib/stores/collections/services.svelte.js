/**
 * Services collection — Phase 4 W1-S2 store-first rewrite.
 *
 * Renders from BahiaEventStore immediately on boot (no network needed).
 * The legacy `applyServiceEvent` applicator remains for backward compat
 * with the event routing in events.svelte.js (unmigrated domains still
 * flow through there).
 *
 * Design reference: phase4-web-store-first.md §8.2, §12 W1-S2.
 */

import { getEventStore, onStoreRefresh } from '../../nostr/boot.js';
import { CAS_CONTROL_STATE, CP_STATE_TOPICS } from '../../nostr/kinds.gen.js';
import { applySimpleReplaceable, getDTag, replaceArray, sortByNameOrId } from './utils.js';

export const services = $state([]);
export const serviceMap = new Map();

// Replaceable-event dedup map (kept for applyServiceEvent compat).
const replaceableEvents = new Map();

/**
 * Rebuild the services array from the BahiaEventStore.
 * Called on boot and on every store refresh (RAF-coalesced).
 */
export function rebuildServicesFromStore() {
  const store = getEventStore();
  if (!store) return;

  const events = store.query({
    kinds: [CAS_CONTROL_STATE],
    '#t': [CP_STATE_TOPICS.SERVICE_REGISTRY],
  });

  // Replay events through the same dedup pipeline the applicator uses
  // so we get identical serviceMap state.
  serviceMap.clear();
  replaceableEvents.clear();
  for (const event of events) {
    applyServiceEvent(event, replaceableEvents);
  }
  replaceArray(services, Array.from(serviceMap.values()).sort(sortByNameOrId));
}

export function refreshServices() {
  replaceArray(services, Array.from(serviceMap.values()).sort(sortByNameOrId));
}

export function resetServices() {
  serviceMap.clear();
  replaceableEvents.clear();
  services.length = 0;
}

/**
 * Apply a service event from the legacy event routing.
 * Still needed because events.svelte.js routes events here for backward compat.
 */
export function applyServiceEvent(event, replaceableEventsArg = replaceableEvents) {
  return applySimpleReplaceable(
    event,
    serviceMap,
    replaceableEventsArg,
    (content, relayEvent) => content.id || getDTag(relayEvent)
  );
}

export function upsertServiceProjection(service) {
  const id = service?.id;
  if (!id) return;

  if (service.deleted) {
    serviceMap.delete(id);
  } else {
    serviceMap.set(id, { ...(serviceMap.get(id) || {}), ...service, id });
  }

  refreshServices();
}

// Register for store refresh notifications so services auto-update
// when new events are ingested from the relay.
let _unsubRefresh = null;
export function initServiceStoreBinding() {
  if (_unsubRefresh) return;
  rebuildServicesFromStore();
  _unsubRefresh = onStoreRefresh(rebuildServicesFromStore);
}

export function teardownServiceStoreBinding() {
  if (_unsubRefresh) {
    _unsubRefresh();
    _unsubRefresh = null;
  }
}
