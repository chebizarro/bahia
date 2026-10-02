/**
 * Controlplane bootstrap — Phase 4 W1-S2 rewrite.
 *
 * Delegates store and pool creation to boot.js, which opens the
 * BahiaEventStore so views can render from persisted data immediately.
 * Then runs the read-model subscription and EOSE tracking through the
 * pool, feeding events to the existing event routing in events.svelte.js.
 *
 * Design reference: phase4-web-store-first.md §7, §12 W1-S2.
 */

import { browser } from '$app/environment';
import { nostr } from '../../nostr/client.js';
import { boot, getEventStore, shutdown } from '../../nostr/boot.js';
import { getBootstrapSeed } from '../discovery.svelte.js';
import { loadSystemInfo } from '../system.svelte.js';
import { clearLoadingForPopulatedCollections, resetCollections, refreshCollections, schedulePersistCachedCollections, setAllLoading } from '../collections/index.svelte.js';
import { applyControlplaneEvent, hydrateCachedControlplane, readModelFilters, resetEventRouting } from './events.svelte.js';
import { bootstrapRetryLimited, connectedRelaysFromSummary, controlplaneConnection, markBootstrapComplete, markBootstrapFailedAt, registerBootstrapControlplaneForRetry, resetConnectionState, setBootstrapError } from './connection.svelte.js';
import {
  markConnecting,
  markSyncing,
  markRelayEose,
  markEventIngested,
  markError,
  markDisconnected,
  resetSyncStatus,
  syncStatus,
} from '../sync-status.svelte.js';
import { toWebSocketUrl } from '$lib/nostr/pool-utils.js';

let bootstrapPromise = null;
let liveUnsubscribe = null;
let connectedUnsubscribe = null;
let lastConnected = false;
let bootstrapExpectedRelays = [];
let bootstrapSubscriptionGeneration = 0;
const CLEANUP_KEY = Symbol.for('bahia.controlplane.store.cleanup');

function cleanupSubscriptions() {
  if (liveUnsubscribe) liveUnsubscribe();
  if (connectedUnsubscribe) connectedUnsubscribe();
  liveUnsubscribe = null;
  connectedUnsubscribe = null;
}

if (globalThis?.[CLEANUP_KEY]) globalThis[CLEANUP_KEY]();
globalThis[CLEANUP_KEY] = cleanupSubscriptions;

function subscribeToConnectionState() {
  if (connectedUnsubscribe) return;
  connectedUnsubscribe = nostr.connected.subscribe((connected) => {
    controlplaneConnection.connected = connected;
    if (lastConnected && !connected && ['syncing', 'live'].includes(controlplaneConnection.status)) {
      if (!controlplaneConnection.bootstrapComplete) bootstrapSubscriptionGeneration += 1;
      controlplaneConnection.status = 'disconnected';
      markDisconnected();
    }
    if (!lastConnected && connected && controlplaneConnection.ready) {
      controlplaneConnection.reconnects += 1;
      controlplaneConnection.status = controlplaneConnection.bootstrapComplete ? 'live' : 'syncing';
      if (!controlplaneConnection.bootstrapComplete) startStreamingSubscription(bootstrapExpectedRelays);
    }
    lastConnected = connected;
  });
}

function completeBootstrapIfCurrent(generation) {
  if (generation !== bootstrapSubscriptionGeneration) return;
  refreshCollections();
  markBootstrapComplete();
  setAllLoading(false);
  syncStatus.phase = 'live';
}

function startStreamingSubscription(expectedRelays, { waitForEose = false } = {}) {
  if (liveUnsubscribe) liveUnsubscribe();
  liveUnsubscribe = null;

  let resolveEose;
  let rejectEose;
  let eoseSettled = !waitForEose;
  const eosePromise = waitForEose
    ? new Promise((resolve, reject) => {
      resolveEose = resolve;
      rejectEose = reject;
    })
    : Promise.resolve();
  const settleEose = (fn, value) => {
    if (eoseSettled) return;
    eoseSettled = true;
    fn?.(value);
  };

  bootstrapExpectedRelays = [...expectedRelays];
  const generation = ++bootstrapSubscriptionGeneration;
  const pendingEoseRelays = new Set(expectedRelays.map(toWebSocketUrl));
  const markRelayEoseFn = (relay) => {
    if (generation !== bootstrapSubscriptionGeneration) return;
    pendingEoseRelays.delete(toWebSocketUrl(relay));
    markRelayEose();
    if (pendingEoseRelays.size === 0) {
      completeBootstrapIfCurrent(generation);
      settleEose(resolveEose, true);
    }
  };

  liveUnsubscribe = nostr.subscribeWithRecovery(readModelFilters(), {
    onEvent: (event) => {
      // Ingest into BahiaEventStore (for store-first derived views)
      const store = getEventStore();
      if (store) store.ingest(event);
      markEventIngested();
      // Route to legacy per-domain applicators (for unmigrated collections)
      applyControlplaneEvent(event, { deferRefresh: true });
    },
    onEose: (relay) => markRelayEoseFn(relay),
    onHealth: (health) => Object.assign(controlplaneConnection, health),
    onClosed: (reason, relay, meta = {}) => {
      const message = reason || `subscription closed by ${relay}`;
      controlplaneConnection.lastError = message;
      if (['syncing', 'live', 'reconnecting'].includes(controlplaneConnection.status)) {
        controlplaneConnection.status = meta.disconnected ? 'disconnected' : 'reconnecting';
      }
      if (meta.terminal && generation === bootstrapSubscriptionGeneration && pendingEoseRelays.has(toWebSocketUrl(relay))) {
        settleEose(rejectEose, new Error(message));
      }
    }
  });

  if (pendingEoseRelays.size === 0) {
    completeBootstrapIfCurrent(generation);
    settleEose(resolveEose, true);
  }

  return eosePromise;
}

export function resetControlplaneStore() {
  cleanupSubscriptions();
  bootstrapPromise = null;
  lastConnected = false;
  bootstrapExpectedRelays = [];
  bootstrapSubscriptionGeneration = 0;
  resetEventRouting();
  resetCollections();
  resetConnectionState();
  resetSyncStatus();
}

export async function bootstrapControlplane({ force = false } = {}) {
  if (!browser) return { ok: false, reason: 'not_browser' };
  if (bootstrapPromise && !force) return bootstrapPromise;
  if (controlplaneConnection.ready && !force) return { ok: true };

  if (!force && bootstrapRetryLimited()) {
    return { ok: false, reason: controlplaneConnection.lastError || 'bootstrap failed recently, waiting before retry' };
  }

  bootstrapPromise = (async () => {
    controlplaneConnection.status = 'discovering';
    controlplaneConnection.lastError = null;
    setAllLoading(true);

    // Step 1: Open BahiaEventStore via boot.js so derived stores can render
    // from persisted data immediately, before any network connection.
    try {
      await boot();
    } catch (err) {
      console.warn('[bootstrap] boot() failed:', err);
    }

    // Resolve the trusted service pubkey so cached canonical events pass
    // the same author filter as live relay events.
    const seed = getBootstrapSeed();
    const relays = Array.from(new Set((seed?.relay_urls || []).map(toWebSocketUrl).filter(Boolean)));
    controlplaneConnection.relays = relays;
    controlplaneConnection.servicePubkey = seed?.service_pubkeys?.[0] || '';

    // Step 2: Render cached state immediately. Hydration replays cached
    // events into the backing Maps. Loading flags stay set only for
    // collections that are still empty. EOSE only moves the connection
    // status from syncing to live — it never gates rendering.
    const hydratedFromCache = await hydrateCachedControlplane();
    if (hydratedFromCache) {
      controlplaneConnection.lastEventAt = controlplaneConnection.lastEventAt || new Date().toISOString();
    }
    clearLoadingForPopulatedCollections();

    try {
      if (relays.length === 0) throw new Error('No browser Nostr relays configured by deployment bootstrap');
      if (!controlplaneConnection.servicePubkey) throw new Error('No trusted Bahia service pubkey configured by deployment bootstrap');

      // Step 3: Connect pool and subscribe
      controlplaneConnection.status = 'connecting';
      markConnecting(relays);
      subscribeToConnectionState();
      nostr.setRelays(relays, false);
      const summary = await nostr.connect(relays, { force: true });

      if (Number(summary?.connected || 0) === 0) throw new Error('Unable to connect to any advertised browser relay');
      const connectedRelays = connectedRelaysFromSummary(summary);
      if (connectedRelays.length === 0) throw new Error('Unable to determine connected relay URLs for bootstrap EOSE tracking');

      controlplaneConnection.ready = true;
      controlplaneConnection.bootstrapComplete = false;
      controlplaneConnection.status = 'syncing';
      markSyncing();

      await startStreamingSubscription(connectedRelays, { waitForEose: true });
      schedulePersistCachedCollections();

      void loadSystemInfo({ force }).catch((error) => {
        console.warn('Optional Bahia discovery metadata unavailable:', error?.message || error);
      });
      return { ok: true };
    } catch (err) {
      markBootstrapFailedAt();
      setBootstrapError(err?.message || String(err));
      markError(err?.message || String(err));
      return { ok: false, reason: controlplaneConnection.lastError };
    } finally {
      setAllLoading(false);
      bootstrapPromise = null;
    }
  })();

  return bootstrapPromise;
}

export function disconnectControlplane() {
  if (liveUnsubscribe) liveUnsubscribe();
  liveUnsubscribe = null;
  controlplaneConnection.status = controlplaneConnection.ready ? 'disconnected' : 'idle';
}

registerBootstrapControlplaneForRetry(bootstrapControlplane);
