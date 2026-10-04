import { writable } from 'svelte/store';
import { PublishStatus, SocketStatus } from '@welshman/net';
import { boot, getPool, getRelayUrls } from './boot.js';
import { KINDS, isLifecycleResultKind } from './kinds.js';

// Compatibility surface for consumers of individual relay events. The only
// network owner is the ref-counted pool in pool-welshman.js.
const connected = writable(false);
const connectionStatus = writable({});
let relays = null;
let observedPool = null;
let stopObserving = null;

function configuredRelays() {
  return relays === null ? getRelayUrls() : [...relays];
}

function observePool(pool) {
  if (!pool || observedPool === pool) return;
  stopObserving?.();
  observedPool = pool;
  stopObserving = pool.onConnectionStatus((url, status) => {
    connectionStatus.update((current) => {
      const next = { ...current, [url]: status === SocketStatus.Open ? 'connected' : status === SocketStatus.Opening ? 'connecting' : 'disconnected' };
      connected.set(Object.values(next).includes('connected'));
      return next;
    });
  });
}

function subscribeOnRelays(targetRelays, filters, handlers = {}) {
  let closed = false;
  let handle = null;
  const start = (pool) => {
    if (closed || !pool) return;
    observePool(pool);
    handle = pool.subscribe({ relays: targetRelays, filters, ...handlers });
  };
  const pool = getPool();
  if (pool) start(pool);
  else void boot().then(() => start(getPool())).catch((error) => handlers.onClosed?.(error?.message || String(error), '', { terminal: true }));
  return () => {
    closed = true;
    handle?.unsubscribe();
  };
}

export const nostr = {
  connected,
  connectionStatus,
  getRelays: configuredRelays,
  getConnectedRelays: (targetRelays = configuredRelays()) => getPool()?.getConnectedRelays(targetRelays) || [],
  setRelays(values) {
    relays = [...new Set((values || []).filter(Boolean))];
    getPool()?.setRelays(relays);
  },
  async connect(values = configuredRelays()) {
    await boot();
    const pool = getPool();
    if (!pool) throw new Error('Bahia event pool is unavailable: deployment bootstrap has no service pubkey');
    observePool(pool);
    return pool.connect(values);
  },
  subscribe(filters, handlers) { return subscribeOnRelays(configuredRelays(), filters, handlers); },
  subscribeOnRelays,
  subscribeWithRecovery(filters, handlers) { return subscribeOnRelays(configuredRelays(), filters, handlers); },
  subscribeWithRecoveryOnRelays: subscribeOnRelays,
  async publish(event, options = {}) {
    await boot();
    const pool = getPool();
    if (!pool) throw new Error('Bahia event pool is unavailable');
    const targetRelays = options.relays || configuredRelays();
    if (targetRelays.length === 0) throw new Error('No relays configured for publish');
    const results = await pool.publishEvent({ event, relays: targetRelays });
    return Object.entries(results).map(([relay, result]) => ({
      relay,
      sent: result.status !== PublishStatus.Aborted,
      accepted: result.status === PublishStatus.Success,
      message: result.detail || result.status,
    }));
  },
};

export function subscribeToProvisioningProgress(requestEventId, onStatus, onResult) {
  return nostr.subscribe([
    { kinds: [KINDS.PROVISIONING_STATUS], '#e': [requestEventId] },
    { kinds: [KINDS.PROVISIONING_RESULT, KINDS.SOUL_ACTION_LEGACY_RESULT], '#e': [requestEventId] }
  ], {
    onEvent: (event) => {
      if (event.kind === KINDS.PROVISIONING_STATUS) onStatus(parseProvisioningStatus(event));
      else if (isLifecycleResultKind(event.kind)) onResult(parseProvisioningResult(event));
    }
  });
}

function parseProvisioningStatus(event) {
  const status = { id: event.id, step: '', progress: { current: 0, total: 0 }, message: event.content };
  for (const tag of event.tags) {
    if (tag[0] === 'step') status.step = tag[1];
    if (tag[0] === 'progress') status.progress = { current: parseInt(tag[1]), total: parseInt(tag[2]) };
  }
  return status;
}

function parseProvisioningResult(event) {
  const result = { id: event.id, success: false, error: '', soulRef: '', action: '', requestKind: '', specHash: '', data: {}, legacyKind: event.kind === KINDS.SOUL_ACTION_LEGACY_RESULT };
  for (const tag of event.tags) {
    switch (tag[0]) {
      case 'status': result.success = tag[1] === 'success'; if (tag[1] === 'error') result.error = event.content; break;
      case 'soul': result.soulRef = tag[1]; break;
      case 'action': result.action = tag[1]; break;
      case 'request-kind': result.requestKind = tag[1]; break;
      case 'spec-hash': result.specHash = tag[1]; break;
    }
  }
  if (event.content) {
    try { result.data = JSON.parse(event.content); } catch { /* Nostr content may be plain text. */ }
  }
  return result;
}

export default nostr;
