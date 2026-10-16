import { browser } from '$app/environment';
import { KINDS, getDTag, getTagValues, parseJsonContent, upsertReplaceableEvent } from '../nostr/client.js';
import { boot, getEventStore, getPool, getServicePubkeys } from '../nostr/boot.js';
import { toWebSocketUrl } from '../nostr/pool-utils.js';

export const BOOTSTRAP_SCHEMA = 'bahia.bootstrap.v1';
export const DISCOVERY_SCHEMA = 'bahia.system-discovery.v1';
export const SYSTEM_DISCOVERY_DTAG = 'bahia-system-v1';
export const BROWSER_RELAY_SET_DTAG = 'bahia-browser-v1';
export const CONTEXTVM_RELAY_SET_DTAG = 'bahia-contextvm-v1';
export const SERVICE_RELAY_SET_DTAG = 'bahia-service-v1';

export const discoveryState = $state({
  seed: null,
  info: null,
  events: [],
  relaySets: {},
  loading: false,
  error: null,
  loadedAt: null
});

let discoveryPromise = null;
let discoveryUnsubscribe = null;
const discoverySubscribers = new Set();

export function subscribeDiscoveryInfo(fn) {
  discoverySubscribers.add(fn);
  fn(discoveryState.info);
  return () => discoverySubscribers.delete(fn);
}

function publishDiscoveryInfo(seed, normalized, events) {
  if (!normalized) return;
  discoveryState.info = normalized;
  discoveryState.events = [...events];
  discoveryState.relaySets = normalized._discovery?.relay_sets || {};
  discoveryState.loadedAt = new Date().toISOString();
  discoveryState.error = null;
  for (const subscriber of discoverySubscribers) subscriber(normalized);
}

function splitList(value) {
  if (!value || typeof value !== 'string') return [];
  return value.split(',').map((item) => item.trim()).filter(Boolean);
}

function validSeed(relayUrls, servicePubkeys, trust = {}) {
  if (!Array.isArray(relayUrls) || !Array.isArray(servicePubkeys) || !relayUrls.length || !servicePubkeys.length) return null;
  if (relayUrls.some((url) => {
    if (typeof url !== 'string' || !/^wss?:\/\//.test(url) || /\s/.test(url)) return true;
    try {
      const parsed = new URL(url);
      return !parsed.hostname || !['ws:', 'wss:'].includes(parsed.protocol) || !!parsed.username || !!parsed.password || !!parsed.hash;
    } catch {
      return true;
    }
  })) return null;
  if (servicePubkeys.some((key) => typeof key !== 'string' || !/^[0-9a-f]{64}$/i.test(key))) return null;
  const serviceID = trust.service_id || '';
  const operatorRoot = trust.operator_root_pubkey || '';
  const inventoryIDs = trust.accepted_inventory_ids ?? null;
  if (Boolean(serviceID) !== Boolean(operatorRoot) ||
      (serviceID && (typeof serviceID !== 'string' || !/^[A-Za-z0-9._-]+$/.test(serviceID) ||
        !/^[0-9a-f]{64}$/i.test(operatorRoot) ||
        servicePubkeys.some(key => key.toLowerCase() === operatorRoot.toLowerCase()))) ||
      (inventoryIDs !== null && (!Array.isArray(inventoryIDs) || inventoryIDs.some(id => !/^[0-9a-f]{64}$/i.test(id)))) ||
      (Array.isArray(inventoryIDs) && inventoryIDs.length > 0 && !serviceID)) return null;
  return {
    schema: BOOTSTRAP_SCHEMA,
    relay_urls: [...new Set(relayUrls)],
    service_pubkeys: [...new Set(servicePubkeys.map((key) => key.toLowerCase()))],
    ...(serviceID ? { service_id: serviceID, operator_root_pubkey: operatorRoot.toLowerCase(),
      accepted_inventory_ids: inventoryIDs?.map(id => id.toLowerCase()) ?? null } : {})
  };
}

export function getBootstrapSeed() {
  if (!browser || typeof window === 'undefined') return null;
  // An injected deployment seed is authoritative, including when it is invalid.
  if (window.__BAHIA_BOOTSTRAP__ !== undefined) {
    const injected = window.__BAHIA_BOOTSTRAP__;
    if (injected?.schema !== BOOTSTRAP_SCHEMA) return null;
    return validSeed(injected.relay_urls, injected.service_pubkeys, injected);
  }

  // Vite variables are only a local-development/test fallback, never merged into an injected seed.
  const env = import.meta.env || {};
  return validSeed(
    splitList(env.PUBLIC_BAHIA_BOOTSTRAP_RELAYS || env.VITE_BAHIA_BOOTSTRAP_RELAYS),
    splitList(env.PUBLIC_BAHIA_SERVICE_PUBKEYS || env.VITE_BAHIA_SERVICE_PUBKEYS || env.PUBLIC_BAHIA_SERVICE_PUBKEY || env.VITE_BAHIA_SERVICE_PUBKEY),
    { service_id: env.PUBLIC_BAHIA_SERVICE_ID || env.VITE_BAHIA_SERVICE_ID,
      operator_root_pubkey: env.PUBLIC_BAHIA_OPERATOR_ROOT_PUBKEY || env.VITE_BAHIA_OPERATOR_ROOT_PUBKEY,
      accepted_inventory_ids: splitList(env.PUBLIC_BAHIA_ACCEPTED_INVENTORY_IDS || env.VITE_BAHIA_ACCEPTED_INVENTORY_IDS) }
  );
}

export function resolveBrowserRelays(systemInfo) {
  const nostrInfo = systemInfo?.nostr || {};
  const relays = [
    ...(Array.isArray(nostrInfo.browser_relays) ? nostrInfo.browser_relays : []),
    nostrInfo.sidecar_url
  ];
  return [...new Set(relays.map(toWebSocketUrl).filter(Boolean))];
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

  if (!discoveryEvent) return null;

  const payload = parseJsonContent(discoveryEvent, null);
  if (!payload || payload.schema !== DISCOVERY_SCHEMA) {
    throw new Error('Invalid Bahia system discovery payload');
  }

  const relaySets = {};
  for (const event of filtered.filter((item) => item.kind === KINDS.NIP51_RELAY_SET)) {
    const d = getDTag(event);
    relaySets[d] = getTagValues(event, 'relay').map(toWebSocketUrl).filter(Boolean);
  }

  const browserRelays = relaySets[BROWSER_RELAY_SET_DTAG] || [];
  const nip34Relays = Array.isArray(payload.nostr?.nip34_relays)
    ? Array.from(new Set(payload.nostr.nip34_relays.map(toWebSocketUrl).filter(Boolean)))
    : [];

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
      nip34_relays: nip34Relays,
      trusted_relay_monitor_pubkeys: Array.isArray(payload.nostr?.trusted_relay_monitor_pubkeys)
        ? payload.nostr.trusted_relay_monitor_pubkeys
        : [],
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
  if (discoveryUnsubscribe) discoveryUnsubscribe();
  discoveryUnsubscribe = null;
  discoveryPromise = null;
  discoveryState.seed = null;
  discoveryState.info = null;
  discoveryState.events = [];
  discoveryState.relaySets = {};
  discoveryState.loading = false;
  discoveryState.error = null;
  discoveryState.loadedAt = null;
}

export async function discoverSystemInfo({ force = false } = {}) {
  if (!browser) return null;
  if (discoveryState.info && !force) return discoveryState.info;
  if (discoveryPromise && !force) return discoveryPromise;

  discoveryState.loading = true;
  discoveryState.error = null;
  discoveryPromise = (async () => {
    const seed = getBootstrapSeed();
    if (!seed?.relay_urls?.length || !seed?.service_pubkeys?.length) {
      throw new Error('Bahia discovery requires deployment bootstrap relay URLs and trusted service pubkeys');
    }
    discoveryState.seed = seed;
    await boot();
    const store = getEventStore();
    const pool = getPool();
    if (!store || !pool) throw new Error('Bahia event store is unavailable for discovery');
    const relays = [...new Set(seed.relay_urls.map(toWebSocketUrl).filter(Boolean))];
    const filter = {
      kinds: [KINDS.BAHIA_SYSTEM_DISCOVERY, KINDS.NIP51_RELAY_SET],
      '#d': [SYSTEM_DISCOVERY_DTAG, BROWSER_RELAY_SET_DTAG, CONTEXTVM_RELAY_SET_DTAG, SERVICE_RELAY_SET_DTAG]
    };
    const snapshot = () => store.query({ ...filter, authors: getServicePubkeys() });
    const publishSnapshot = () => {
      const events = snapshot();
      const normalized = normalizeDiscoveryEvents(events, getServicePubkeys());
      if (normalized) publishDiscoveryInfo(seed, normalized, events);
      return normalized;
    };

    if (discoveryUnsubscribe) discoveryUnsubscribe();
    const cached = publishSnapshot();
    let eose = new Set();
    let closed = new Set();
    let settle;
    const caughtUp = new Promise((resolve, reject) => { settle = { resolve, reject }; });
    // A valid persisted discovery snapshot is usable even if relays close
    // before EOSE; consume the catch-up promise so its rejection is handled.
    if (cached) void caughtUp.catch(() => {});
    let activeAuthors = '';
    const subscribe = () => {
      const authors = getServicePubkeys();
      activeAuthors = authors.join(',');
      eose = new Set();
      closed = new Set();
      const filters = [{ ...filter, authors }];
      if (seed.service_id) filters.push({ kinds: [KINDS.CAS_CONTROL_STATE],
        authors: [seed.operator_root_pubkey], '#d': [`trust-epoch:${seed.service_id}`] });
      const handle = pool.subscribe({
        relays, filters,
        onEvent: () => {
          try {
            if (getServicePubkeys().join(',') !== activeAuthors) {
              discoveryUnsubscribe?.();
              subscribe();
            }
            publishSnapshot();
          } catch (error) { discoveryState.error = error?.message || String(error); }
        },
        onEose: (relay) => {
          eose.add(toWebSocketUrl(relay));
          if (eose.size + closed.size >= relays.length) settle.resolve(publishSnapshot());
        },
        onClosed: (reason, relay) => {
          closed.add(toWebSocketUrl(relay));
          if (eose.size + closed.size >= relays.length) {
            if (eose.size) settle.resolve(publishSnapshot());
            else settle.reject(new Error(`Discovery relays closed before EOSE: ${reason || 'no relay served history'}`));
          }
        }
      });
      discoveryUnsubscribe = () => handle.unsubscribe();
    };
    subscribe();
    // Persisted, signature-checked events render immediately. EOSE is only
    // required when there is no local discovery state to display.
    return cached || caughtUp;
  })();

  try {
    return await discoveryPromise;
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
}
