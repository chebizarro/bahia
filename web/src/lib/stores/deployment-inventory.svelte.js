import { browser } from '$app/environment';
import { nostr } from '$lib/nostr/client.js';
import { ensureRelayConnection } from '$lib/nostr/connection-guard.js';
import { subscribeToRetainedEvents } from '$lib/nostr/retained-domain-subscription.js';
import { getBootstrapSeed } from './discovery.svelte.js';
import {
  buildDeploymentInventoryView,
  createDeploymentInventoryState,
  deploymentInventoryFilters,
  ingestDeploymentInventoryEvent,
  markUnconfirmedCacheEntries,
  restoreDeploymentInventoryCache,
  serializeDeploymentInventoryCache
} from '$lib/deployment-inventory.js';

export const DEPLOYMENT_INVENTORY_CACHE_KEY = 'bahia_deployment_inventory_cache_v1';

export const deploymentInventory = $state({
  status: 'idle',
  error: '',
  caughtUp: false,
  reconnecting: false,
  lastClosedReason: '',
  revision: 0,
  cachedRestored: 0,
  unconfirmedCount: 0,
  rejectedCount: 0
});

let inventoryState = createDeploymentInventoryState();
let unsubscribe = null;
let starting = null;
let generation = 0;
let processing = Promise.resolve();
let startingGeneration = -1;

function bump() {
  deploymentInventory.revision += 1;
  deploymentInventory.rejectedCount = inventoryState.rejected.length;
}

/** Render model; call with the page's clock so freshness ages without new events. */
export function deploymentInventoryView(nowMs = Date.now()) {
  return buildDeploymentInventoryView(inventoryState, { nowMs });
}

function persist(storage) {
  if (!storage || typeof storage.setItem !== 'function') return;
  try {
    storage.setItem(DEPLOYMENT_INVENTORY_CACHE_KEY, serializeDeploymentInventoryCache(inventoryState));
  } catch (error) {
    console.warn('[deployment-inventory] failed to persist cache:', error?.message || error);
  }
}

/**
 * Subscribe to the signed deployment inventory. Cached events are re-verified
 * and shown as provisional until relays confirm them; the retained
 * subscription re-issues itself with backoff after CLOSED or disconnect, and
 * replayed events are idempotent under addressable ordering.
 */
export function startDeploymentInventory({
  client = nostr,
  connect = ensureRelayConnection,
  seed = browser ? getBootstrapSeed() : null,
  storage = browser && typeof localStorage !== 'undefined' ? localStorage : null,
  validate
} = {}) {
  if (unsubscribe) return Promise.resolve();
  if (starting && startingGeneration === generation) return starting;
  const trustedPubkeys = Array.isArray(seed?.service_pubkeys) ? seed.service_pubkeys : [];
  if (trustedPubkeys.length === 0) {
    deploymentInventory.status = 'error';
    deploymentInventory.error = 'No trusted Bahia service pubkeys are configured; deployment inventory cannot be verified.';
    return Promise.resolve();
  }
  const ingestOptions = { trustedPubkeys, ...(validate ? { validate } : {}) };
  if (deploymentInventory.status !== 'cached' && deploymentInventory.status !== 'ready') deploymentInventory.status = 'loading';
  deploymentInventory.error = '';
  const startedGeneration = ++generation;
  startingGeneration = startedGeneration;

  const run = (async () => {
    const raw = storage?.getItem?.(DEPLOYMENT_INVENTORY_CACHE_KEY);
    if (raw) {
      const { restored } = await restoreDeploymentInventoryCache(inventoryState, raw, ingestOptions);
      deploymentInventory.cachedRestored = restored;
      if (restored > 0) deploymentInventory.status = 'cached';
      bump();
    }

    // Relay callbacks are applied strictly in arrival order.
    const serialize = (task) => {
      processing = processing.then(task, task);
      return processing;
    };

    const stop = await subscribeToRetainedEvents({
      client,
      connect,
      filters: deploymentInventoryFilters(trustedPubkeys),
      onEvent: (event) => serialize(async () => {
        const result = await ingestDeploymentInventoryEvent(inventoryState, event, ingestOptions);
        deploymentInventory.reconnecting = false;
        if (result.accepted) persist(storage);
        bump();
      }),
      onReady: () => serialize(async () => {
        deploymentInventory.caughtUp = true;
        deploymentInventory.reconnecting = false;
        deploymentInventory.status = 'ready';
        deploymentInventory.unconfirmedCount = markUnconfirmedCacheEntries(inventoryState);
        bump();
      }),
      onClosed: (reason, _relay, _metadata, meta) => {
        deploymentInventory.lastClosedReason = String(reason || '');
        if (meta?.recovering) deploymentInventory.reconnecting = true;
        if (meta?.authRequired) deploymentInventory.error = `Relay requires AUTH for deployment inventory: ${reason}`;
      }
    });
    if (startedGeneration !== generation) {
      stop?.();
      return;
    }
    unsubscribe = stop;
  })().catch((error) => {
    deploymentInventory.status = 'error';
    deploymentInventory.error = error?.message || String(error);
  }).finally(() => {
    if (starting === run) starting = null;
  });
  starting = run;
  return run;
}

/** Resolves once every event received so far has been verified and applied. */
export function whenDeploymentInventoryIdle() {
  return processing;
}

export function stopDeploymentInventory() {
  generation += 1;
  unsubscribe?.();
  unsubscribe = null;
  deploymentInventory.caughtUp = false;
}

export function resetDeploymentInventoryStore() {
  stopDeploymentInventory();
  starting = null;
  inventoryState = createDeploymentInventoryState();
  processing = Promise.resolve();
  Object.assign(deploymentInventory, {
    status: 'idle',
    error: '',
    caughtUp: false,
    reconnecting: false,
    lastClosedReason: '',
    revision: 0,
    cachedRestored: 0,
    unconfirmedCount: 0,
    rejectedCount: 0
  });
}
