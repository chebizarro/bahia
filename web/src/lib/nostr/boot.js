/**
 * Store-first boot sequence.
 *
 * 1. Read deploy seed (service_pubkeys, relay_urls).
 * 2. Open the IndexedDB event store for this service pubkey namespace.
 * 3. Views can render from the store IMMEDIATELY — no network needed.
 * 4. The store-first subscription connects the pool in background.
 * 5. EOSE transitions sync-status from "syncing" to "live" (badge, not gate).
 * 6. Events ingested by the pool go into the BahiaEventStore; derived stores
 *    consume only that store.
 *
 * Design reference: phase4-web-store-first.md §7, §12 W1-S2.
 *
 * @module lib/nostr/boot
 */

import { browser } from '$app/environment';
import { createBahiaEventStore } from './store.js';
import { createBahiaPool } from './pool-welshman.js';
import { getBootstrapSeed } from '../stores/discovery.svelte.js';

// ---------------------------------------------------------------------------
// Module-level singletons
// ---------------------------------------------------------------------------

/** @type {ReturnType<typeof createBahiaEventStore> | null} */
let _store = null;

/** @type {ReturnType<typeof createBahiaPool> | null} */
let _pool = null;

/** Service pubkey from the deploy seed. */
let _servicePubkey = '';

/** Relay URLs from the deploy seed. */
let _relayUrls = /** @type {string[]} */ ([]);

// ---------------------------------------------------------------------------
// Animation-frame batched collection refresh (§7 step 7)
// ---------------------------------------------------------------------------

/** @type {Set<() => void>} */
const _refreshCallbacks = new Set();

/** @type {number | null} */
let _rafId = null;
let _dirty = false;
let _storeUnsubscribe = null;

/**
 * Register a callback to be called when the store has new events.
 * Coalesced per animation frame — fires at most once per frame.
 *
 * @param {() => void} cb
 * @returns {() => void} Unsubscribe function.
 */
export function onStoreRefresh(cb) {
  _refreshCallbacks.add(cb);
  return () => _refreshCallbacks.delete(cb);
}

function scheduleRefresh() {
  if (_dirty) return;
  _dirty = true;
  if (typeof requestAnimationFrame === 'function') {
    _rafId = requestAnimationFrame(_flushRefresh);
  } else {
    queueMicrotask(_flushRefresh);
  }
}

function _flushRefresh() {
  _rafId = null;
  _dirty = false;
  for (const cb of _refreshCallbacks) {
    try { cb(); } catch (err) { console.error('[boot] refresh callback error:', err); }
  }
}

/** Synchronously flush any scheduled refresh (for tests). */
export function flushBatch() {
  if (_rafId !== null && typeof cancelAnimationFrame === 'function') {
    cancelAnimationFrame(_rafId);
    _rafId = null;
  }
  if (_dirty) _flushRefresh();
}

// ---------------------------------------------------------------------------
// Public accessors
// ---------------------------------------------------------------------------

/** @returns {ReturnType<typeof createBahiaEventStore> | null} */
export function getEventStore() { return _store; }

/** @returns {ReturnType<typeof createBahiaPool> | null} */
export function getPool() { return _pool; }

export function getServicePubkey() { return _servicePubkey; }
export function getRelayUrls() { return [..._relayUrls]; }

// ---------------------------------------------------------------------------
// Boot
// ---------------------------------------------------------------------------

/** @type {Promise<void> | null} */
let _bootPromise = null;

/**
 * Open the event store so views can render immediately from persisted data.
 * Idempotent — subsequent calls return the same promise.
 *
 * Does NOT connect to relays or start subscriptions. The store-first
 * `bootstrapControlplane()` handles that, using getEventStore()/getPool().
 *
 * @param {object} [options]
 * @param {ReturnType<typeof createBahiaEventStore>} [options.store] - DI
 * @param {ReturnType<typeof createBahiaPool>} [options.pool] - DI
 * @param {object} [options.seed] - DI
 * @returns {Promise<void>}
 */
export async function boot(options = {}) {
  if (!browser) return;
  if (_bootPromise) return _bootPromise;

  _bootPromise = _bootInternal(options);
  try {
    await _bootPromise;
  } catch (err) {
    _bootPromise = null;
    throw err;
  }
}

async function _bootInternal({ store: injectedStore, pool: injectedPool, seed: injectedSeed } = {}) {
  const seed = injectedSeed || getBootstrapSeed();
  if (!seed?.service_pubkeys?.length) return;

  _servicePubkey = seed.service_pubkeys[0];
  _relayUrls = seed.relay_urls ? [...seed.relay_urls] : [];

  // Open the IndexedDB event store (step 2).
  // After this, all persisted events are in memory and queryable.
  const prefix = _servicePubkey.slice(0, 8);
  _store = injectedStore || createBahiaEventStore({ servicePubkeyPrefix: prefix });
  if (!injectedStore) {
    await _store.open();
  }
  _storeUnsubscribe = _store.subscribe?.({}, scheduleRefresh) || null;

  // Create the pool (but don't connect yet — bootstrap does that).
  _pool = injectedPool || createBahiaPool({ store: _store });

  // Trigger initial refresh so derived stores see persisted data.
  _flushRefresh();
}

// ---------------------------------------------------------------------------
// Shutdown
// ---------------------------------------------------------------------------

export async function shutdown() {
  _storeUnsubscribe?.();
  _storeUnsubscribe = null;
  if (_rafId !== null && typeof cancelAnimationFrame === 'function') {
    cancelAnimationFrame(_rafId);
    _rafId = null;
  }
  _dirty = false;

  if (_pool) {
    _pool.destroy();
    _pool = null;
  }

  if (_store) {
    await _store.close();
    _store = null;
  }

  _servicePubkey = '';
  _relayUrls = [];
  _bootPromise = null;
}

export async function ensureRelayConnection() {
  await boot();
  if (!_pool || _relayUrls.length === 0) throw new Error('No browser relays configured by deployment bootstrap');
  const summary = await _pool.connect(_relayUrls);
  if (summary.connected === 0) throw new Error('Unable to connect to any configured browser relay');
  return summary;
}
