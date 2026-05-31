import { browser } from '$app/environment';
import { nostr } from '../../nostr/client.js';
import { loadSystemInfo } from '../system.svelte.js';
import { resetCollections, refreshCollections, setAllLoading } from '../collections/index.svelte.js';
import { applyControlplaneEvent, readModelFilters, resetEventRouting } from './events.svelte.js';
import { connectedRelaysFromSummary, controlplaneConnection, markBootstrapComplete, normalizeRelayUrl, resetConnectionState, resolveBrowserRelays, setBootstrapError } from './connection.svelte.js';

let bootstrapPromise = null;
let liveUnsubscribe = null;
let connectedUnsubscribe = null;
let lastConnected = false;
let lastBootstrapFailedAt = null;
let bootstrapExpectedRelays = [];
let bootstrapSubscriptionGeneration = 0;
const BOOTSTRAP_RETRY_INTERVAL_MS = 30_000;
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
  markBootstrapComplete();
  setAllLoading(false);
}

function startStreamingSubscription(expectedRelays) {
  if (liveUnsubscribe) liveUnsubscribe();
  liveUnsubscribe = null;

  bootstrapExpectedRelays = [...expectedRelays];
  const generation = ++bootstrapSubscriptionGeneration;
  const pendingEoseRelays = new Set(expectedRelays.map(normalizeRelayUrl));
  const markRelayEose = (relay) => {
    if (generation !== bootstrapSubscriptionGeneration) return;
    pendingEoseRelays.delete(normalizeRelayUrl(relay));
    if (pendingEoseRelays.size === 0) completeBootstrapIfCurrent(generation);
  };

  liveUnsubscribe = nostr.subscribe(readModelFilters(), {
    onEvent: (event) => applyControlplaneEvent(event),
    onEose: (relay) => markRelayEose(relay),
    onClosed: (reason, relay) => {
      controlplaneConnection.lastError = reason || `subscription closed by ${relay}`;
      if (['syncing', 'live'].includes(controlplaneConnection.status)) {
        controlplaneConnection.status = 'disconnected';
      }
    }
  });

  if (pendingEoseRelays.size === 0) completeBootstrapIfCurrent(generation);
}

export function resetControlplaneStore() {
  cleanupSubscriptions();
  bootstrapPromise = null;
  lastBootstrapFailedAt = null;
  lastConnected = false;
  bootstrapExpectedRelays = [];
  bootstrapSubscriptionGeneration = 0;
  resetEventRouting();
  resetCollections();
  resetConnectionState();
}

export async function bootstrapControlplane({ force = false } = {}) {
  if (!browser) return { ok: false, reason: 'not_browser' };
  if (bootstrapPromise && !force) return bootstrapPromise;
  if (controlplaneConnection.ready && !force) return { ok: true };

  if (!force && lastBootstrapFailedAt !== null) {
    const elapsed = Date.now() - lastBootstrapFailedAt;
    if (elapsed < BOOTSTRAP_RETRY_INTERVAL_MS) {
      return { ok: false, reason: controlplaneConnection.lastError || 'bootstrap failed recently, waiting before retry' };
    }
  }
  if (force) lastBootstrapFailedAt = null;

  bootstrapPromise = (async () => {
    controlplaneConnection.status = 'discovering';
    controlplaneConnection.lastError = null;
    setAllLoading(true);

    try {
      const systemInfo = await loadSystemInfo({ force });
      const relays = resolveBrowserRelays(systemInfo);
      controlplaneConnection.relays = relays;
      controlplaneConnection.servicePubkey = systemInfo?.nostr?.service_pubkey || '';

      if (!systemInfo?.features?.relay_read_models) throw new Error('Relay read models are not advertised by Nostr system discovery');
      if (relays.length === 0) throw new Error('No browser Nostr relays advertised by Nostr system discovery');

      controlplaneConnection.status = 'connecting';
      subscribeToConnectionState();
      nostr.setRelays(relays, false);
      const summary = await nostr.connect(relays, { force: true });

      if (Number(summary?.connected || 0) === 0) throw new Error('Unable to connect to any advertised browser relay');
      const connectedRelays = connectedRelaysFromSummary(summary);
      if (connectedRelays.length === 0) throw new Error('Unable to determine connected relay URLs for bootstrap EOSE tracking');

      controlplaneConnection.ready = true;
      controlplaneConnection.bootstrapComplete = false;
      controlplaneConnection.status = 'syncing';
      startStreamingSubscription(connectedRelays);
      refreshCollections();
      return { ok: true };
    } catch (err) {
      lastBootstrapFailedAt = Date.now();
      setBootstrapError(err?.message || String(err));
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
