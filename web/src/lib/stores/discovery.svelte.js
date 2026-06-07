import { browser } from '$app/environment';
import { KINDS, getDTag, getTagValues, parseJsonContent, upsertReplaceableEvent } from '../nostr/client.js';
import { PoolBackedClient } from '../nostr/pool-client.js';

export const BOOTSTRAP_SCHEMA = 'bahia.bootstrap.v1';
export const DISCOVERY_SCHEMA = 'bahia.system-discovery.v1';
export const SYSTEM_DISCOVERY_DTAG = 'bahia-system-v1';
export const BROWSER_RELAY_SET_DTAG = 'bahia-browser-v1';
export const CONTEXTVM_RELAY_SET_DTAG = 'bahia-contextvm-v1';
export const SERVICE_RELAY_SET_DTAG = 'bahia-service-v1';
const DISCOVERY_CACHE_KEY = 'bahia_system_discovery_cache_v1';
const DISCOVERY_CACHE_TTL_MS = 15 * 60 * 1000;

export const discoveryState = $state({
  seed: null,
  events: [],
  relaySets: {},
  loading: false,
  error: null,
  loadedAt: null
});

let discoveryPromise = null;

function splitList(value) {
  if (!value || typeof value !== 'string') return [];
  return value.split(',').map((item) => item.trim()).filter(Boolean);
}

export function getBootstrapSeed() {
  if (!browser || typeof window === 'undefined') return null;
  const injected = window.__BAHIA_BOOTSTRAP__;

  const env = import.meta.env || {};
  const envRelayUrls = splitList(env.PUBLIC_BAHIA_BOOTSTRAP_RELAYS || env.VITE_BAHIA_BOOTSTRAP_RELAYS);
  const envServicePubkeys = splitList(env.PUBLIC_BAHIA_SERVICE_PUBKEYS || env.VITE_BAHIA_SERVICE_PUBKEYS || env.PUBLIC_BAHIA_SERVICE_PUBKEY || env.VITE_BAHIA_SERVICE_PUBKEY);

  const relay_urls = Array.from(new Set([
    ...(Array.isArray(injected?.relay_urls) ? injected.relay_urls : []),
    ...envRelayUrls
  ].filter(Boolean)));

  const service_pubkeys = Array.from(new Set([
    ...(Array.isArray(injected?.service_pubkeys) ? injected.service_pubkeys : []),
    ...envServicePubkeys
  ].filter(Boolean)));

  if (relay_urls.length === 0 && service_pubkeys.length === 0) return null;

  return {
    schema: BOOTSTRAP_SCHEMA,
    relay_urls,
    service_pubkeys
  };
}

function normalizeRelayUrl(url) {
  if (!url || typeof url !== 'string') return '';
  if (url.startsWith('ws://') || url.startsWith('wss://')) return url;
  if (url.startsWith('https://')) return `wss://${url.slice('https://'.length)}`;
  if (url.startsWith('http://')) return `ws://${url.slice('http://'.length)}`;
  return url;
}

function latestByReplaceableKey(events) {
  const byKey = new Map();
  for (const event of events) {
    upsertReplaceableEvent(byKey, event);
  }
  return Array.from(byKey.values()).map((entry) => entry.event || entry);
}

export function normalizeDiscoveryEvents(events, trustedPubkeys) {
  const trusted = new Set(trustedPubkeys || []);
  const filtered = latestByReplaceableKey((events || []).filter((event) => {
    if (!event || !trusted.has(event.pubkey)) return false;
    if (![KINDS.BAHIA_SYSTEM_DISCOVERY, KINDS.NIP51_RELAY_SET].includes(event.kind)) return false;
    const d = getDTag(event);
    if (event.kind === KINDS.BAHIA_SYSTEM_DISCOVERY) return d === SYSTEM_DISCOVERY_DTAG;
    return [BROWSER_RELAY_SET_DTAG, CONTEXTVM_RELAY_SET_DTAG, SERVICE_RELAY_SET_DTAG].includes(d);
  }));

  const discoveryEvent = filtered
    .filter((event) => event.kind === KINDS.BAHIA_SYSTEM_DISCOVERY)
    .sort((a, b) => (b.created_at || 0) - (a.created_at || 0))[0];

  if (!discoveryEvent) throw new Error('No trusted Bahia system discovery event received before EOSE');

  const payload = parseJsonContent(discoveryEvent, null);
  if (!payload || payload.schema !== DISCOVERY_SCHEMA) {
    throw new Error('Invalid Bahia system discovery payload');
  }

  const relaySets = {};
  for (const event of filtered.filter((item) => item.kind === KINDS.NIP51_RELAY_SET)) {
    const d = getDTag(event);
    relaySets[d] = getTagValues(event, 'relay').map(normalizeRelayUrl).filter(Boolean);
  }

  const browserRelays = relaySets[BROWSER_RELAY_SET_DTAG] || [];
  if (browserRelays.length === 0) {
    throw new Error('No trusted Bahia browser relay set received before EOSE');
  }

  const advertisedContextVMRelays = relaySets[CONTEXTVM_RELAY_SET_DTAG] || [];
  const contextVMFallback = advertisedContextVMRelays.length === 0;
  const contextVMRelays = contextVMFallback ? browserRelays : advertisedContextVMRelays;
  const contextVMRelayMetadata = contextVMFallback
    ? {
        source: BROWSER_RELAY_SET_DTAG,
        degraded: true,
        reason: relaySets[CONTEXTVM_RELAY_SET_DTAG]
          ? 'empty_contextvm_relay_set'
          : 'missing_contextvm_relay_set'
      }
    : {
        source: CONTEXTVM_RELAY_SET_DTAG,
        degraded: false,
        reason: ''
      };

  return {
    ...payload,
    nostr: {
      browser_relays: browserRelays,
      contextvm_relays: contextVMRelays,
      contextvm_relay_metadata: contextVMRelayMetadata,
      sidecar_url: browserRelays[0] || '',
      service_relays: relaySets[SERVICE_RELAY_SET_DTAG] || [],
      service_pubkey: discoveryEvent.pubkey,
      service_npub: '',
      publish_enabled: payload.features?.publish_enabled ?? true
    },
    _discovery: {
      event_id: discoveryEvent.id,
      pubkey: discoveryEvent.pubkey,
      created_at: discoveryEvent.created_at,
      relay_sets: relaySets,
      contextvm_relay_metadata: contextVMRelayMetadata
    }
  };
}

export function resetDiscoveryStore() {
  discoveryPromise = null;
  discoveryState.seed = null;
  discoveryState.events = [];
  discoveryState.relaySets = {};
  discoveryState.loading = false;
  discoveryState.error = null;
  discoveryState.loadedAt = null;
}

function loadCachedDiscovery(seed) {
  if (!browser || typeof localStorage?.getItem !== 'function') return null;

  try {
    const raw = localStorage.getItem(DISCOVERY_CACHE_KEY);
    if (!raw) return null;

    const cached = JSON.parse(raw);
    const age = Date.now() - Number(cached?.cachedAt || 0);
    const trustedPubkeys = Array.isArray(seed?.service_pubkeys) ? seed.service_pubkeys : [];
    const cachedPubkey = cached?.normalized?.nostr?.service_pubkey;
    if (cached?.schema !== DISCOVERY_CACHE_KEY || age > DISCOVERY_CACHE_TTL_MS) return null;
    if (trustedPubkeys.length > 0 && cachedPubkey && !trustedPubkeys.includes(cachedPubkey)) return null;
    if (!cached?.normalized?.nostr?.browser_relays?.length) return null;
    if (!Array.isArray(cached?.normalized?.nostr?.contextvm_relays)) return null;
    if (!cached?.normalized?.nostr?.contextvm_relay_metadata) return null;

    return cached;
  } catch (error) {
    console.warn('Failed to load cached system discovery:', error);
    return null;
  }
}

function persistDiscoveryCache(seed, normalized, events) {
  if (!browser || typeof localStorage?.setItem !== 'function') return;

  try {
    localStorage.setItem(
      DISCOVERY_CACHE_KEY,
      JSON.stringify({
        schema: DISCOVERY_CACHE_KEY,
        cachedAt: Date.now(),
        seed,
        normalized,
        events
      })
    );
  } catch (error) {
    console.warn('Failed to persist system discovery cache:', error);
  }
}

export async function discoverSystemInfo({ force = false } = {}) {
  if (!browser) return null;
  if (discoveryState.events.length > 0 && !force) return normalizeDiscoveryEvents(discoveryState.events, discoveryState.seed?.service_pubkeys || []);
  if (discoveryPromise && !force) return discoveryPromise;

  discoveryState.loading = true;
  discoveryState.error = null;

  discoveryPromise = (async () => {
    try {
      const seed = getBootstrapSeed();
      if (!seed?.relay_urls?.length) throw new Error('No relay URLs configured. Set PUBLIC_BAHIA_BOOTSTRAP_RELAYS or inject window.__BAHIA_BOOTSTRAP__ before deploying.');
      if (!seed?.service_pubkeys?.length) throw new Error('No trusted service pubkeys configured. Set PUBLIC_BAHIA_SERVICE_PUBKEYS before deploying.');

      discoveryState.seed = seed;
      const cached = !force ? loadCachedDiscovery(seed) : null;
      if (cached?.normalized) {
        discoveryState.events = Array.isArray(cached.events) ? cached.events : [];
        discoveryState.relaySets = cached.normalized?._discovery?.relay_sets || {};
        discoveryState.loadedAt = new Date(cached.cachedAt).toISOString();
        return cached.normalized;
      }

      const relays = Array.from(new Set(seed.relay_urls.map(normalizeRelayUrl).filter(Boolean)));
      const bootstrapClient = new PoolBackedClient({
        relays,
        saveRelayConfig: () => {}
      });
      let events;
      try {
        const summary = await bootstrapClient.connect(relays, { force: true });
        if (summary.connected === 0) {
          throw new Error('Unable to connect to any bootstrap relay');
        }

        events = await bootstrapClient.queryUntilEose([
          { kinds: [KINDS.BAHIA_SYSTEM_DISCOVERY, KINDS.NIP51_RELAY_SET], authors: seed.service_pubkeys }
        ]);
      } finally {
        bootstrapClient.disconnect();
      }

      const normalized = normalizeDiscoveryEvents(events, seed.service_pubkeys);
      discoveryState.events = events;
      discoveryState.relaySets = normalized._discovery.relay_sets;
      discoveryState.loadedAt = new Date().toISOString();
      persistDiscoveryCache(seed, normalized, events);
      return normalized;
    } catch (error) {
      discoveryState.error = error?.message || String(error);
      if (force) {
        discoveryState.events = [];
        discoveryState.loadedAt = null;
      }
      throw error;
    } finally {
      discoveryState.loading = false;
      discoveryPromise = null;
    }
  })();

  return discoveryPromise;
}
