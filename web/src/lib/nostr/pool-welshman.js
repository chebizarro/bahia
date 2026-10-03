/**
 * Single shared Nostr pool backed by welshman.
 *
 * Provides ref-counted long-lived REQs with `since=cursor`, per-relay
 * cursors committed on EOSE/live events, NIP-42 AUTH, and per-relay OK
 * tracking for publishes.
 *
 * Does NOT delete or rewire the existing pools (that's W4).  This module
 * adds the new pool layer in parallel.
 *
 * Design reference: phase4-web-store-first.md §7 step 5, §12 W1-S1.
 *
 * @module lib/nostr/pool-welshman
 */

import {
  Pool,
  SocketStatus,
  request as welshmanRequest,
  publish as welshmanPublish,
  PublishStatus,
} from '@welshman/net';

// ---------------------------------------------------------------------------
// Subscription ref-counting
// ---------------------------------------------------------------------------

/**
 * @typedef {object} ManagedSubscription
 * @property {string} id - Unique subscription id
 * @property {string[]} relays - Relay URLs
 * @property {import('./store-interface.js').Filter[]} filters - Filters
 * @property {number} refCount - Number of active consumers
 * @property {AbortController} controller - Abort controller for the subscription
 * @property {((url: string) => void) | null} onEose
 */

let subIdCounter = 0;

/**
 * Adapter factory signature: given a relay URL and a net context,
 * return an adapter for that relay.  Production uses the welshman
 * default (WebSocket-backed `SocketAdapter`); alternative environments
 * (unit tests, service workers) can inject a custom factory.
 *
 * @typedef {(url: string, context: import('@welshman/net').AdapterContext) => import('@welshman/net').AbstractAdapter} AdapterFactory
 */

/**
 * Create the shared Bahia pool and its management API.
 *
 * @param {object} options
 * @param {import('./store-interface.js').BahiaEventStore & { ingest: (e: any) => boolean, getCursor: (r: string, f: string) => number | null, setCursor: (r: string, f: string, s: number) => void }} options.store
 *   The BahiaEventStore to ingest events into.
 * @param {((event: any) => Promise<any>) | null} [options.sign]
 *   Signer function for NIP-42 AUTH.  May be set later via `setSign()`.
 * @param {AdapterFactory} [options.getAdapter]
 *   Custom adapter factory for dependency injection.  When omitted,
 *   welshman's built-in adapter resolution is used (real WebSocket
 *   connections via the pool).  Pass a factory that returns a
 *   `MockAdapter` for unit tests, or a custom adapter for service
 *   workers or other non-browser environments.
 * @returns {BahiaPool}
 */
export function createBahiaPool({ store, sign = null, getAdapter }) {
  const pool = new Pool();

  /** @type {Map<string, ManagedSubscription>} */
  const subs = new Map();

  /** Outbox delivery is driven by socket readiness, never by a polling timer. */
  function getConnectedRelays(relays) {
    return relays.filter(url => pool.has(url) && pool.get(url).status === SocketStatus.Open);
  }

  function onRelayReady(callback, relays = []) {
    const removers = [];
    const attach = socket => {
      const status = value => { if (value === SocketStatus.Open) callback({ relay: socket.url, auth: false }); };
      const auth = value => { if (value === 'ok') callback({ relay: socket.url, auth: true }); };
      socket.on('status', status);
      socket.auth.on('status', auth);
      removers.push(() => { socket.off('status', status); socket.auth.off('status', auth); });
      if (socket.status === SocketStatus.Open) callback({ relay: socket.url, auth: false });
    };
    const unsubscribe = pool.subscribe(attach);
    for (const relay of relays) if (pool.has(relay)) attach(pool.get(relay));
    return () => { unsubscribe(); removers.forEach(remove => remove()); };
  }

  /** @type {((event: any) => Promise<any>) | null} */
  let signFn = sign;

  /**
   * Build the adapter context for welshman request/publish calls.
   * @returns {import('@welshman/net').AdapterContext}
   */
  function buildContext() {
    const ctx = { pool };
    if (getAdapter) ctx.getAdapter = getAdapter;
    return ctx;
  }

  // Wire NIP-42 AUTH handling: when a socket requests auth, sign and respond
  pool.subscribe((socket) => {
    if (!socket.auth) return;
    socket.auth.on('status', (status) => {
      if (status === 'requested' && signFn) {
        socket.auth.attemptAuth(signFn).catch(err => {
          console.warn('[BahiaPool] AUTH failed for', socket.url, err);
        });
      }
    });
  });

  /**
   * Update the signer function (e.g. after login).
   * @param {typeof signFn} fn
   */
  function setSign(fn) {
    signFn = fn;
  }

  /**
   * Create a ref-counted subscription.
   *
   * Each call returns an independent handle with its own unsubscribe.
   * The underlying welshman request is aborted only when the last
   * handle unsubscribes.
   *
   * @param {object} opts
   * @param {string[]} opts.relays
   * @param {import('./store-interface.js').Filter[]} opts.filters
   * @param {string} [opts.filterKey] - Key for cursor tracking
   * @param {(url: string) => void} [opts.onEose]
   * @returns {{ unsubscribe: () => void, id: string }}
   */
  function subscribe({ relays, filters, filterKey, onEose }) {
    const id = `bahia-sub-${++subIdCounter}`;
    const controller = new AbortController();

    /** @type {ManagedSubscription} */
    const sub = {
      id,
      relays,
      filters,
      refCount: 1,
      controller,
      onEose: onEose || null,
    };

    subs.set(id, sub);

    // Apply cursor: if filterKey is set and we have a stored cursor,
    // inject `since` into filters
    const appliedFilters = filters.map(f => {
      if (!filterKey) return f;
      for (const relay of relays) {
        const cursor = store.getCursor(relay, filterKey);
        if (cursor !== null) {
          return { ...f, since: Math.max(f.since ?? 0, cursor) };
        }
      }
      return f;
    });

    // Fire the welshman request
    welshmanRequest({
      relays,
      filters: appliedFilters,
      signal: controller.signal,
      autoClose: false,
      context: buildContext(),
      onEvent: (event, url) => {
        // Ingest into the store (signature verification happens there)
        const accepted = store.ingest(event);
        if (accepted && filterKey) {
          // Update cursor to the latest event timestamp
          const current = store.getCursor(url, filterKey) ?? 0;
          if (event.created_at > current) {
            store.setCursor(url, filterKey, event.created_at);
          }
        }
      },
      onEose: (url) => {
        if (sub.onEose) {
          sub.onEose(url);
        }
        // Commit cursor on EOSE
        if (filterKey) {
          const cursor = store.getCursor(url, filterKey);
          if (cursor !== null) {
            store.setCursor(url, filterKey, cursor);
          }
        }
      },
    }).catch(err => {
      if (err?.name !== 'AbortError') {
        console.error('[BahiaPool] subscription error:', err);
      }
    });

    return {
      id,
      unsubscribe() {
        const s = subs.get(id);
        if (!s) return;
        s.refCount--;
        if (s.refCount <= 0) {
          s.controller.abort();
          subs.delete(id);
        }
      },
    };
  }

  /**
   * Add a reference to an existing subscription.
   *
   * W1-S2's boot sequence uses this so that multiple views (services,
   * environments, workers, etc.) can share a single underlying REQ for
   * the read-model subscription without duplicating relay traffic.
   * Each view holds its own unsubscribe handle; the REQ is only
   * closed when the last handle unsubscribes.
   *
   * @param {string} id - The subscription id to add a reference to.
   * @returns {{ unsubscribe: () => void } | null} null if not found.
   */
  function addRef(id) {
    const sub = subs.get(id);
    if (!sub) return null;
    sub.refCount++;
    return {
      unsubscribe() {
        sub.refCount--;
        if (sub.refCount <= 0) {
          sub.controller.abort();
          subs.delete(id);
        }
      },
    };
  }

  /**
   * Publish an event to relays with per-relay OK tracking.
   *
   * @param {object} opts
   * @param {import('./store-interface.js').NostrEvent} opts.event
   * @param {string[]} opts.relays
   * @param {number} [opts.timeout] - Per-relay timeout in ms (default 10s)
   * @returns {Promise<Record<string, import('@welshman/net').PublishResult>>}
   */
  async function publishEvent({ event, relays, timeout = 10000 }) {
    const results = await welshmanPublish({
      event,
      relays,
      timeout,
      context: buildContext(),
    });
    return results;
  }

  /**
   * Get the underlying welshman Pool.
   *
   * Used by W1-S2 boot to check connection state and by W3 outbox
   * for reconnect-driven retry.
   */
  function getPool() {
    return pool;
  }

  /**
   * Clean up all subscriptions and the pool.
   */
  function destroy() {
    for (const [, sub] of subs) {
      sub.controller.abort();
    }
    subs.clear();
    pool.clear();
  }

  return {
    subscribe,
    addRef,
    publishEvent,
    getConnectedRelays,
    onRelayReady,
    setSign,
    getPool,
    destroy,
    PublishStatus,
  };
}

/**
 * @typedef {ReturnType<typeof createBahiaPool>} BahiaPool
 */
