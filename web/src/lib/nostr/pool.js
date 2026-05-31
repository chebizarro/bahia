import { writable, get } from 'svelte/store';
import { SimplePool } from 'nostr-tools';

function relaySummaryFromStates(relayStates) {
  return Array.from(relayStates.entries()).map(([relay, state]) => ({ relay, ...state }));
}

function arrayFrom(value) {
  if (!value) return [];
  return Array.isArray(value) ? value : [value];
}

function normalizeRelayUrl(url) {
  if (typeof url !== 'string' || !url.trim()) return '';
  try {
    let normalized = url.trim();
    if (!normalized.includes('://')) normalized = `wss://${normalized}`;
    const parsed = new URL(normalized);
    if (parsed.protocol === 'http:') parsed.protocol = 'ws:';
    if (parsed.protocol === 'https:') parsed.protocol = 'wss:';
    parsed.pathname = parsed.pathname.replace(/\/+/g, '/');
    if (parsed.pathname.endsWith('/')) parsed.pathname = parsed.pathname.slice(0, -1);
    if ((parsed.port === '80' && parsed.protocol === 'ws:') || (parsed.port === '443' && parsed.protocol === 'wss:')) parsed.port = '';
    parsed.searchParams.sort();
    parsed.hash = '';
    return parsed.toString();
  } catch {
    return url.trim();
  }
}

function uniqueRelays(relays = []) {
  const seen = new Set();
  const out = [];
  for (const relay of arrayFrom(relays)) {
    const url = typeof relay === 'string' ? relay.trim() : '';
    if (!url) continue;
    const key = normalizeRelayUrl(url);
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(url);
  }
  return out;
}

function summarizeRelayConnections(relays, statusMap = {}) {
  const relayStatuses = relays.map((url) => ({
    url,
    status: statusMap[url] || 'unknown'
  }));

  const connected = relayStatuses.filter((relay) => relay.status === 'connected').length;
  const failed = relayStatuses.filter((relay) => ['error', 'failed', 'disconnected'].includes(relay.status)).length;
  const connecting = relayStatuses.filter((relay) => relay.status === 'connecting').length;

  return {
    total: relays.length,
    connected,
    failed,
    connecting,
    relays: relayStatuses
  };
}

function messageFromError(error) {
  if (error instanceof Error) return error.message;
  if (typeof error === 'string') return error;
  return String(error || 'unknown relay error');
}

function publishSentBeforeFailure(message) {
  const lower = String(message || '').toLowerCase();
  return !lower.includes('connection failure') &&
    !lower.includes('closed connection') &&
    !lower.includes('connection skipped') &&
    !lower.includes('relay connection closed') &&
    !lower.includes('websocket closed');
}

export class NostrIncompleteEOSEError extends Error {
  constructor(reason, { partialEvents = [], relaySummary = [], message = '' } = {}) {
    super(message || `Nostr query did not receive complete EOSE history: ${reason}`);
    this.name = 'NostrIncompleteEOSEError';
    this.reason = reason;
    this.partialEvents = partialEvents;
    this.relaySummary = relaySummary;
  }
}

class PoolBackedClient {
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
    this.pool.onRelayConnectionSuccess = (url) => {
      this.markRelayStatus(url, 'connected');
    };
    this.pool.onRelayConnectionFailure = (url) => {
      this.markRelayStatus(url, 'error');
    };
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
    const targetRelays = uniqueRelays(relays);
    const connectKey = targetRelays.map(normalizeRelayUrl).join('\n');
    if (this.connectPromise && this.connectRelaysKey === connectKey) return this.connectPromise;

    const previousRelays = this.relays;
    this.relays = targetRelays;
    this.updateRelayAliases();

    const nextKeys = new Set(this.relays.map(normalizeRelayUrl));
    const removedRelays = previousRelays.filter((url) => !nextKeys.has(normalizeRelayUrl(url)));
    if (removedRelays.length > 0) {
      this.pool?.close?.(removedRelays);
    }

    this.connectionStatus.set(Object.fromEntries(this.relays.map((url) => [url, 'connecting'])));
    this.updateConnectedStatus();

    const connection = Promise.resolve().then(async () => {
      await Promise.allSettled(this.relays.map((url) => this.connectRelay(url)));
      const summary = summarizeRelayConnections(this.relays, this.refreshConnectionStatus());
      console.log(`[nostr] Connected to ${summary.connected}/${summary.total} relays`);
      return summary;
    });

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

  subscribeOnRelays(relays, filters, { onEvent, onEose, onClosed, onAuth } = {}) {
    const requestedRelays = uniqueRelays(relays);
    const subId = `sub_${++this.subIdCounter}`;
    const subscriptions = new Map();
    const queues = new Map();
    const seenEvents = new Set();
    let active = true;

    const closeSubscription = (subscription) => {
      try {
        subscription?.close?.('closed by caller');
      } catch (error) {
        console.warn('[nostr] failed to close subscription:', messageFromError(error));
      }
    };

    const openRelaySubscription = async (relayUrl) => {
      try {
        const relay = await this.pool.ensureRelay(relayUrl);
        const callbackRelay = this.aliasForRelay(relay?.url || relayUrl);
        this.markRelayStatus(callbackRelay, relay?.connected === false ? 'disconnected' : 'connected');

        if (!active) return;

        if (onAuth) {
          relay.onauth = async (eventTemplate) => {
            const challenge = relay.challenge || 'auth-required';
            const authEvent = await onAuth(challenge, callbackRelay, eventTemplate);
            if (!authEvent) throw new Error('NIP-42 AUTH challenge received but no auth event was provided');
            return authEvent;
          };
        }

        const subscription = relay.subscribe(filters, {
          id: subId,
          onevent: (event) => {
            this.enqueueRelayCallback(queues, callbackRelay, async () => {
              if (!active) return;
              if (this.validateEvent) {
                try {
                  await this.validateEvent(event);
                } catch (validationError) {
                  console.warn(`[nostr] Dropping invalid EVENT from ${callbackRelay}:`, validationError?.message || validationError);
                  return;
                }
              }
              if (event?.id && seenEvents.has(event.id)) return;
              if (event?.id) seenEvents.add(event.id);
              onEvent?.(event, callbackRelay);
            });
          },
          oneose: () => {
            this.enqueueRelayCallback(queues, callbackRelay, async () => {
              if (active) onEose?.(callbackRelay);
            });
          },
          onclose: (reason = '') => {
            this.enqueueRelayCallback(queues, callbackRelay, async () => {
              if (!active) return;
              if (String(reason).startsWith('auth-required') && onAuth) {
                onAuth(reason, callbackRelay);
              }
              onClosed?.(reason, callbackRelay, { terminal: true, source: 'closed' });
            });
          },
          oninvalidevent: (event) => {
            console.warn(`[nostr] Dropping invalid EVENT from ${callbackRelay}:`, event);
          }
        });

        subscriptions.set(callbackRelay, subscription);
        if (!active) closeSubscription(subscription);
      } catch (error) {
        const reason = messageFromError(error);
        this.markRelayStatus(relayUrl, 'error');
        if (active) onClosed?.(reason, relayUrl, { terminal: true, source: 'connection' });
      }
    };

    const opening = Promise.allSettled(requestedRelays.map(openRelaySubscription));

    const unsubscribe = () => {
      if (!this.activeSubscriptions.has(unsubscribe)) return;
      this.activeSubscriptions.delete(unsubscribe);
      active = false;
      opening.finally(() => {
        subscriptions.forEach(closeSubscription);
        subscriptions.clear();
      });
    };

    this.activeSubscriptions.add(unsubscribe);
    return unsubscribe;
  }

  async queryUntilEose(filters, options = {}) {
    const queryOptions = typeof options === 'number' ? { timeoutMs: options } : (options || {});
    const { timeoutMs = null, signal = null } = queryOptions;

    return new Promise((resolve, reject) => {
      const events = [];
      const seenEventIds = new Set();
      const connectedRelays = this.getConnectedRelays();
      const relayStates = new Map(connectedRelays.map((relay) => [relay, { status: 'pending', reason: '' }]));
      let unsub = null;
      let timer = null;
      let settled = false;

      const cleanup = () => {
        if (timer) clearTimeout(timer);
        timer = null;
        if (unsub) unsub();
        unsub = null;
        signal?.removeEventListener?.('abort', onAbort);
      };

      const incomplete = (reason, message = '') => new NostrIncompleteEOSEError(reason, {
        partialEvents: [...events],
        relaySummary: relaySummaryFromStates(relayStates),
        message
      });

      const settle = (fn, value) => {
        if (settled) return;
        settled = true;
        cleanup();
        fn(value);
      };

      const onAbort = () => {
        settle(reject, incomplete('aborted', signal?.reason?.message || 'Nostr query aborted before EOSE completion'));
      };

      const evaluateCompletion = () => {
        if (settled) return;
        const states = Array.from(relayStates.values());
        if (states.length === 0) return;
        if (states.every((state) => state.status === 'eose')) {
          settle(resolve, events);
          return;
        }
        if (states.every((state) => state.status !== 'pending')) {
          settle(reject, incomplete('all_relays_closed', 'Nostr query relays closed before all EOSE messages were received'));
        }
      };

      if (relayStates.size === 0) {
        settle(reject, incomplete('all_relays_closed', 'No connected Nostr relays available for EOSE query'));
        return;
      }

      if (signal?.aborted) {
        onAbort();
        return;
      }
      signal?.addEventListener?.('abort', onAbort, { once: true });

      if (Number.isFinite(timeoutMs) && timeoutMs > 0) {
        timer = setTimeout(() => {
          settle(reject, incomplete('timeout', `Timed out waiting for Nostr EOSE after ${timeoutMs}ms`));
        }, timeoutMs);
      }

      unsub = this.subscribeOnRelays(connectedRelays, filters, {
        onEvent: (event) => {
          if (event?.id && seenEventIds.has(event.id)) return;
          if (event?.id) seenEventIds.add(event.id);
          events.push(event);
        },
        onEose: (relay) => {
          if (relayStates.has(relay)) relayStates.set(relay, { status: 'eose', reason: '' });
          evaluateCompletion();
        },
        onClosed: (reason = '', relay, meta = {}) => {
          if (relayStates.has(relay)) {
            const current = relayStates.get(relay);
            if (current?.status !== 'eose') {
              relayStates.set(relay, meta?.terminal === false
                ? { status: 'pending', reason: String(reason || '') }
                : { status: 'closed', reason: String(reason || '') });
            }
          }
          if (meta?.terminal !== false) evaluateCompletion();
        }
      });
    });
  }

  async query(filters, timeout = 5000) {
    const options = typeof timeout === 'number' ? { timeoutMs: timeout } : (timeout || {});
    return this.queryUntilEose(filters, options);
  }

  async publish(event, options = {}) {
    if (!event?.id) {
      throw new Error('Cannot publish event without id');
    }

    const relays = this.getConnectedRelays();
    if (relays.length === 0) return [];

    const publishPromises = this.pool.publish(relays, event, options);
    return Promise.all(publishPromises.map((promise, index) => {
      const relay = relays[index];
      return Promise.resolve(promise)
        .then((message = '') => ({ relay, sent: true, accepted: true, message: String(message || '') }))
        .catch((error) => {
          const message = messageFromError(error);
          return {
            relay,
            sent: publishSentBeforeFailure(message),
            accepted: false,
            message
          };
        });
    }));
  }

  disconnect() {
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

export function createNostrPoolClient(options = {}) {
  return new PoolBackedClient(options);
}
