import { writable, get } from 'svelte/store';
import { SimplePool } from 'nostr-tools';
import { publishFromPool } from './pool-publish.js';
import { subscribeOnRelays, subscribeWithRecoveryOnRelays } from './pool-subscriptions.js';
import { summarizeRelayConnections, uniqueRelays, normalizeRelayUrl, messageFromError } from './pool-utils.js';

export class PoolBackedClient {
  constructor({ relays = [], saveRelayConfig = () => {}, validateEvent = null, pool = null, poolFactory = null } = {}) {
    this.relays = uniqueRelays(relays);
    this.saveRelayConfig = saveRelayConfig;
    this.validateEvent = validateEvent;
    this.poolFactory = poolFactory || ((options) => new SimplePool(options));
    this.externalPool = pool;
    this.pool = pool || this.createPool();
    this.connected = writable(false);
    this.connectionStatus = writable({});
    this.subIdCounter = 0;
    this.activeSubscriptions = new Set();
    this.connectPromise = null;
    this.connectRelaysKey = '';
    this.disconnected = false;
    this.relayAliases = new Map();
    this.configurePool();
    this.updateRelayAliases();
  }

  createPool() {
    return this.poolFactory({ enableReconnect: true });
  }

  configurePool() {
    if (!this.pool) return;
    this.pool.trackRelays = true;
    this.pool.enableReconnect = true;
    this.pool.onRelayConnectionSuccess = (url) => this.markRelayStatus(url, 'connected');
    this.pool.onRelayConnectionFailure = (url) => this.markRelayStatus(url, 'error');
  }

  updateRelayAliases() {
    this.relayAliases = new Map(this.relays.map((url) => [normalizeRelayUrl(url), url]));
  }

  aliasForRelay(url) {
    return this.relayAliases.get(normalizeRelayUrl(url)) || url;
  }

  markRelayStatus(url, status) {
    const alias = this.aliasForRelay(url);
    this.connectionStatus.update((current) => ({ ...current, [alias]: status }));
    this.updateConnectedStatus();
  }

  updateConnectedStatus() {
    const status = get(this.connectionStatus);
    this.connected.set(Object.values(status).some((value) => value === 'connected'));
  }

  refreshConnectionStatus() {
    if (typeof this.pool?.listConnectionStatus !== 'function') {
      this.updateConnectedStatus();
      return get(this.connectionStatus);
    }
    const poolStatus = this.pool.listConnectionStatus();
    this.connectionStatus.update((current) => {
      const next = {};
      for (const url of this.relays) {
        const normalized = normalizeRelayUrl(url);
        if (poolStatus.has(normalized)) {
          next[url] = poolStatus.get(normalized) ? 'connected' : (current[url] === 'connecting' ? 'connecting' : 'disconnected');
        } else if (poolStatus.has(url)) {
          next[url] = poolStatus.get(url) ? 'connected' : (current[url] === 'connecting' ? 'connecting' : 'disconnected');
        } else {
          next[url] = current[url] || 'disconnected';
        }
      }
      return next;
    });
    this.updateConnectedStatus();
    return get(this.connectionStatus);
  }

  getRelays() {
    return [...this.relays];
  }

  setRelays(relays, persist = true) {
    this.relays = uniqueRelays(relays);
    this.updateRelayAliases();
    if (persist) this.saveRelayConfig(this.relays);
    this.refreshConnectionStatus();
  }

  getConnectedRelays() {
    const status = this.refreshConnectionStatus();
    return this.relays.filter((url) => status[url] === 'connected');
  }

  async connect(relays = this.relays, { force = false } = {}) {
    this.disconnected = false;
    const targetRelays = uniqueRelays(relays);
    const connectKey = targetRelays.map(normalizeRelayUrl).join('\n');
    if (this.connectPromise && this.connectRelaysKey === connectKey) return this.connectPromise;

    // Idempotent short-circuit: if not forced and the requested relay set matches the
    // current set and is already fully connected, skip the reconnect cycle (and its
    // per-call "Connected to N/N relays" log). Compare normalized URLs order-independently,
    // and read the connected status using this.relays' own keys (as produced by
    // refreshConnectionStatus) so URL formatting differences can't defeat the check.
    if (!force && targetRelays.length > 0) {
      const targetKeys = targetRelays.map(normalizeRelayUrl);
      const currentKeys = this.relays.map(normalizeRelayUrl);
      const sameSet = targetKeys.length === currentKeys.length
        && targetKeys.every((key) => currentKeys.includes(key));
      if (sameSet) {
        const status = this.refreshConnectionStatus();
        if (this.relays.every((url) => status[url] === 'connected')) {
          return summarizeRelayConnections(this.relays, status);
        }
      }
    }

    const previousRelays = this.relays;
    this.relays = targetRelays;
    this.updateRelayAliases();
    const nextKeys = new Set(this.relays.map(normalizeRelayUrl));
    const removedRelays = previousRelays.filter((url) => !nextKeys.has(normalizeRelayUrl(url)));
    if (removedRelays.length > 0) this.pool?.close?.(removedRelays);
    this.connectionStatus.set(Object.fromEntries(this.relays.map((url) => [url, 'connecting'])));
    this.updateConnectedStatus();

    const relayConnections = this.relays.map((url) => this.connectRelay(url));
    const connection = (async () => {
      await Promise.allSettled(relayConnections);
      const summary = summarizeRelayConnections(this.relays, this.refreshConnectionStatus());
      console.log(`[nostr] Connected to ${summary.connected}/${summary.total} relays`);
      return summary;
    })();

    this.connectPromise = connection;
    this.connectRelaysKey = connectKey;

    try {
      return await connection;
    } finally {
      if (this.connectPromise === connection) {
        this.connectPromise = null;
        this.connectRelaysKey = '';
      }
    }
  }

  async connectRelay(url) {
    this.markRelayStatus(url, 'connecting');
    try {
      const relay = await this.pool.ensureRelay(url);
      const alias = this.aliasForRelay(relay?.url || url);
      this.markRelayStatus(alias, relay?.connected === false ? 'disconnected' : 'connected');
    } catch (error) {
      console.warn(`[nostr] relay connection failed for ${url}:`, messageFromError(error));
      this.markRelayStatus(url, 'error');
    }
  }

  retryRelay(url) {
    this.pool?.close?.([url]);
    return this.connectRelay(url);
  }

  enqueueRelayCallback(queues, relay, callback) {
    const previous = queues.get(relay) || Promise.resolve();
    const next = previous.catch(() => {}).then(callback);
    queues.set(relay, next);
    next.finally(() => {
      if (queues.get(relay) === next) queues.delete(relay);
    });
    return next;
  }

  subscribe(filters, handlers = {}) {
    return this.subscribeOnRelays(this.relays, filters, handlers);
  }

  subscribeOnRelays(relays, filters, handlers = {}) {
    return subscribeOnRelays(this, relays, filters, handlers);
  }

  subscribeWithRecovery(filters, handlers = {}, recoveryOptions = {}) {
    return this.subscribeWithRecoveryOnRelays(this.relays, filters, handlers, recoveryOptions);
  }

  subscribeWithRecoveryOnRelays(relays, filters, handlers = {}, recoveryOptions = {}) {
    return subscribeWithRecoveryOnRelays(this, relays, filters, handlers, recoveryOptions);
  }

  async publish(event, options = {}) {
    return publishFromPool(this, event, options);
  }

  disconnect() {
    if (this.disconnected) return;
    this.disconnected = true;
    Array.from(this.activeSubscriptions).forEach((unsubscribe) => unsubscribe());
    this.activeSubscriptions.clear();
    this.pool?.destroy?.();
    if (!this.externalPool) {
      this.pool = this.createPool();
      this.configurePool();
    }
    this.connectionStatus.set({});
    this.connected.set(false);
  }
}
