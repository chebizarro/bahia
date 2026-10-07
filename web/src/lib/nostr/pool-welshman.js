/**
 * Single shared Nostr pool backed by welshman.
 *
 * Provides ref-counted long-lived REQs with `since=cursor`, per-relay
 * cursors committed on EOSE/live events, NIP-42 AUTH, and per-relay OK
 * tracking for publishes.
 *
 * Design reference: docs/architecture/web-store-first.md.
 *
 * @module lib/nostr/pool-welshman
 */

import {
  Pool,
  SocketStatus,
  request as welshmanRequest,
  publish as welshmanPublish,
  PublishStatus,
  isTerminalReason,
} from '@welshman/net';
import { isExpired, validateForIngestion } from './ingestion.js';

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
 * @property {Set<object>} handlers - Per-consumer callbacks
 * @property {string} key - Canonical REQ identity
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
  const subsByKey = new Map();
  const relayReadyListeners = new Set();
  const socketListeners = new Map();
  const connectionListeners = new Set();
  const relayStatuses = new Map();
  let configuredRelays = [];

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
    const emitReady = (auth) => {
      for (const { listener, relays } of relayReadyListeners) {
        if (!relays.size || relays.has(socket.url)) listener({ relay: socket.url, auth });
      }
    };
    const onStatus = (status) => {
      relayStatuses.set(socket.url, status);
      for (const listener of connectionListeners) listener(socket.url, status);
      if (status === SocketStatus.Open) emitReady(false);
    };
    const onAuthStatus = (status) => { if (status === 'ok') emitReady(true); };
    socket.on('status', onStatus);
    socket.auth?.on('status', onAuthStatus);
    socketListeners.set(socket, () => {
      socket.off('status', onStatus);
      socket.auth?.off('status', onAuthStatus);
    });
    if (!socket.auth) return;
    const onAuthRequest = (status) => {
      if (status === 'requested' && signFn) {
        socket.auth.attemptAuth(signFn).catch(err => {
          console.warn('[BahiaPool] AUTH failed for', socket.url, err);
        });
      }
      if (status === 'requested') {
        for (const sub of subs.values()) {
          if (sub.relays.includes(socket.url)) for (const handler of sub.handlers) handler.onAuth?.(socket.auth?.challenge, socket.url);
        }
      }
    };
    socket.auth.on('status', onAuthRequest);
    const previousCleanup = socketListeners.get(socket);
    socketListeners.set(socket, () => { previousCleanup(); socket.auth.off('status', onAuthRequest); });
  });

  /**
   * Update the signer function (e.g. after login).
   *
   * A relay that enforces NIP-42 reads (the Bahia sidecar default) answers a
   * protected REQ from an unauthenticated socket with an AUTH challenge and
   * `CLOSED auth-required:`. welshman's auth-buffer socket policy withholds
   * that refusal and replays the REQ once AUTH succeeds, but only a signer
   * can complete the flow. So when a signer arrives after the challenge
   * (login after the bootstrap REQ went out) every socket that is still
   * waiting on its challenge is authenticated now; the buffered REQs then
   * replay by themselves. A denied or forbidden AUTH is terminal for that
   * socket's buffered REQs (welshman releases the refusals), so those are
   * not retried here; a reconnect starts the flow over.
   *
   * @param {typeof signFn} fn
   */
  function setSign(fn) {
    signFn = typeof fn === 'function' ? fn : null;
    if (!signFn) return;
    for (const socket of socketListeners.keys()) {
      if (socket.auth?.status !== 'requested') continue;
      socket.auth.attemptAuth(signFn).catch(err => console.warn('[BahiaPool] AUTH failed for', socket.url, err));
    }
  }

  function getConnectedRelays(relays = configuredRelays) {
    return relays.filter(url => pool.has(url) && pool.get(url).status === SocketStatus.Open);
  }

  function setRelays(relays) {
    configuredRelays = [...new Set(relays.filter(Boolean))];
  }

  function getRelays() { return [...configuredRelays]; }

  function onConnectionStatus(listener) {
    connectionListeners.add(listener);
    for (const [url, status] of relayStatuses) listener(url, status);
    return () => connectionListeners.delete(listener);
  }

  async function connect(relays = configuredRelays) {
    setRelays(relays);
    await Promise.all(configuredRelays.map((url) => new Promise((resolve) => {
      const socket = pool.get(url);
      if (socket.status === SocketStatus.Open) return resolve();
      const onStatus = (status) => {
        if (status !== SocketStatus.Open && status !== SocketStatus.Error && status !== SocketStatus.Closed) return;
        socket.off('status', onStatus);
        resolve();
      };
      socket.on('status', onStatus);
      socket.attemptToOpen();
    })));
    return {
      total: configuredRelays.length,
      connected: getConnectedRelays().length,
      relays: configuredRelays.map((url) => ({ url, status: pool.get(url).status === SocketStatus.Open ? 'connected' : 'error' }))
    };
  }

  function onRelayReady(listener, relays = []) {
    const registration = { listener, relays: new Set(relays) };
    relayReadyListeners.add(registration);
    for (const relay of relays) {
      if (pool.has(relay) && pool.get(relay).status === SocketStatus.Open) {
        listener({ relay, auth: false });
      }
    }
    return () => relayReadyListeners.delete(registration);
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
  function subscribe({ relays, filters, filterKey, onEvent, onEose, onClosed, onAuth, onHealth }) {
    const key = JSON.stringify([[...relays].sort(), filters, filterKey || '']);
    const handler = { onEvent, onEose, onClosed, onAuth, onHealth };
    const existing = subsByKey.get(key);
    if (existing) {
      existing.refCount++;
      existing.handlers.add(handler);
      return reference(existing, handler);
    }
    const id = `bahia-sub-${++subIdCounter}`;
    const controller = new AbortController();

    /** @type {ManagedSubscription} */
    const sub = {
      id,
      relays,
      filters,
      refCount: 1,
      controller,
      handlers: new Set([handler]),
      key,
    };

    subs.set(id, sub);
    subsByKey.set(key, sub);

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
        // A rejected signature must never reach compatibility subscribers,
        // even when they consume the callback without querying the store.
        if (!validateForIngestion(event).valid || isExpired(event)) return;
        const accepted = store.ingest(event);
        if (accepted && filterKey) {
          // Update cursor to the latest event timestamp
          const current = store.getCursor(url, filterKey) ?? 0;
          if (event.created_at > current) {
            store.setCursor(url, filterKey, event.created_at);
          }
        }
        for (const listener of sub.handlers) listener.onEvent?.(event, url);
      },
      onEose: (url) => {
        for (const listener of sub.handlers) listener.onEose?.(url);
        // Commit cursor on EOSE
        if (filterKey) {
          const cursor = store.getCursor(url, filterKey);
          if (cursor !== null) {
            store.setCursor(url, filterKey, cursor);
          }
        }
      },
      onClosed: (reason, url) => {
        const terminal = isTerminalReason(reason);
        const authRequired = /^auth-required:/i.test(reason || '');
        for (const listener of sub.handlers) listener.onClosed?.(reason, url, { terminal, authRequired });
      },
      onDisconnect: (url) => {
        for (const listener of sub.handlers) {
          listener.onClosed?.('relay disconnected', url, { disconnected: true });
          listener.onHealth?.({ status: 'disconnected', lastClosedReason: 'relay disconnected' });
        }
      },
      resubscribeAttempts: 3,
    }).catch(err => {
      if (err?.name !== 'AbortError') {
        console.error('[BahiaPool] subscription error:', err);
      }
    });

    return reference(sub, handler);
  }

  function reference(sub, handler = null) {
    let released = false;
    return {
      id: sub.id,
      unsubscribe() {
        if (released) return;
        released = true;
        release(sub, handler);
      }
    };
  }

  function release(sub, handler) {
    if (!subs.has(sub.id)) return;
    if (handler) sub.handlers.delete(handler);
    sub.refCount--;
    if (sub.refCount <= 0) {
      sub.controller.abort();
      subs.delete(sub.id);
      subsByKey.delete(sub.key);
    }
  }

  /**
   * Add a reference to an existing subscription.
   *
   * The boot sequence uses this so that multiple views (services,
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
    return reference(sub);
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
    if (!event?.id || !event?.sig) throw new Error('Cannot publish an unsigned Nostr event');
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
   * Used by boot to check connection state and by the outbox
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
    subsByKey.clear();
    for (const cleanup of socketListeners.values()) cleanup();
    socketListeners.clear();
    relayReadyListeners.clear();
    connectionListeners.clear();
    relayStatuses.clear();
    pool.clear();
  }

  return {
    subscribe,
    addRef,
    publishEvent,
    getConnectedRelays,
    getRelays,
    setRelays,
    connect,
    onConnectionStatus,
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
