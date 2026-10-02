/**
 * Environments collection — Phase 4 W1-S2 store-first rewrite.
 *
 * Renders from BahiaEventStore immediately on boot (no network needed).
 * The legacy `applyEnvironmentEvent` applicator remains for backward compat
 * with the event routing in events.svelte.js.
 *
 * Design reference: phase4-web-store-first.md §8.2, §12 W1-S2.
 */

import { getEventStore, onStoreRefresh } from '../../nostr/boot.js';
import { CAS_CONTROL_STATE, CP_STATE_TOPICS } from '../../nostr/kinds.gen.js';
import { applySimpleReplaceable, getDTag, replaceArray, sortByNameOrId } from './utils.js';

export const environments = $state([]);
export const environmentMap = new Map();

// Replaceable-event dedup map (kept for applyEnvironmentEvent compat).
const replaceableEvents = new Map();

/**
 * Rebuild the environments array from the BahiaEventStore.
 * Called on boot and on every store refresh (RAF-coalesced).
 */
export function rebuildEnvironmentsFromStore() {
  const store = getEventStore();
  if (!store) return;

  const events = store.query({
    kinds: [CAS_CONTROL_STATE],
    '#t': [CP_STATE_TOPICS.ENVIRONMENT_REGISTRY],
  });

  environmentMap.clear();
  replaceableEvents.clear();
  for (const event of events) {
    applyEnvironmentEvent(event, replaceableEvents);
  }
  replaceArray(environments, Array.from(environmentMap.values()).sort(sortByNameOrId));
}

export function refreshEnvironments() {
  replaceArray(environments, Array.from(environmentMap.values()).sort(sortByNameOrId));
}

export function resetEnvironments() {
  environmentMap.clear();
  replaceableEvents.clear();
  environments.length = 0;
}

/**
 * Apply an environment event from the legacy event routing.
 * Still needed because events.svelte.js routes events here for backward compat.
 */
export function applyEnvironmentEvent(event, replaceableEventsArg = replaceableEvents) {
  return applySimpleReplaceable(
    event,
    environmentMap,
    replaceableEventsArg,
    (content, relayEvent) => content.id || getDTag(relayEvent)
  );
}

// Register for store refresh notifications
let _unsubRefresh = null;
export function initEnvironmentStoreBinding() {
  if (_unsubRefresh) return;
  rebuildEnvironmentsFromStore();
  _unsubRefresh = onStoreRefresh(rebuildEnvironmentsFromStore);
}

export function teardownEnvironmentStoreBinding() {
  if (_unsubRefresh) {
    _unsubRefresh();
    _unsubRefresh = null;
  }
}
