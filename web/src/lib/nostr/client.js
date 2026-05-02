// Nostr client for Soul Factory web UI
// Uses nostr-tools for WebSocket relay connections

import { writable, derived, get } from 'svelte/store';

// Soul Factory event kinds
export const KINDS = {
  SOUL_TEMPLATE: 31950,
  AGENT_SOUL: 31951,
  SOUL_DRAFT: 31952,
  PROVISIONING_REQUEST: 5950,
  PROVISIONING_STATUS: 6950,
  PROVISIONING_RESULT: 7950,
  SOUL_ACTION: 1950,
  REPOSITORY: 30617,

  // Bahia canonical control-plane/read-model kinds
  BAHIA_REQUEST_DEPLOY: 5961,
  BAHIA_REQUEST_ROLLBACK: 5962,
  BAHIA_REQUEST_SERVICE_ACTION: 5963,
  BAHIA_REQUEST_SERVICE_CREATE: 5964,
  BAHIA_REQUEST_ENVIRONMENT_CREATE: 5965,
  BAHIA_REQUEST_DEPLOYMENT_APPROVAL: 5966,
  BAHIA_REQUEST_OBSERVATION_SUBMIT: 5967,
  BAHIA_REQUEST_DRIFT_REMEDIATE: 5968,
  BAHIA_REQUEST_LLM_ROUTE_CREATE: 5971,
  BAHIA_REQUEST_LLM_RELEASE_REGISTER: 5972,
  BAHIA_REQUEST_LLM_DEPLOY: 5973,
  BAHIA_REQUEST_LLM_DEPLOYMENT_APPROVAL: 5974,
  BAHIA_REQUEST_LLM_ROLLBACK: 5975,
  BAHIA_DEPLOYMENT_STATUS: 6961,
  BAHIA_SERVICE_STATUS: 6962,
  BAHIA_LLM_DEPLOYMENT_STATUS: 6973,
  BAHIA_DEPLOYMENT_RESULT: 7961,
  BAHIA_ACTION_RESULT: 7962,
  BAHIA_SERVICE_CREATE_RESULT: 7963,
  BAHIA_ENVIRONMENT_CREATE_RESULT: 7964,
  BAHIA_OBSERVATION_RESULT: 7965,
  BAHIA_REMEDIATION_RESULT: 7966,
  BAHIA_LLM_ROUTE_CREATE_RESULT: 7971,
  BAHIA_LLM_RELEASE_REGISTER_RESULT: 7972,
  BAHIA_LLM_DEPLOYMENT_RESULT: 7973,
  BAHIA_SERVICE_STATE: 31961,
  BAHIA_SERVICE_REGISTRY: 31962,
  BAHIA_ENVIRONMENT_REGISTRY: 31963,
  BAHIA_LLM_ROUTE_REGISTRY: 31964,
  BAHIA_LLM_ROUTE_STATE: 31965,
  LOOM_WORKER_AD: 10100
};

export const BAHIA_KINDS = {
  DEPLOY_REQUEST: KINDS.BAHIA_REQUEST_DEPLOY,
  ROLLBACK_REQUEST: KINDS.BAHIA_REQUEST_ROLLBACK,
  SERVICE_ACTION: KINDS.BAHIA_REQUEST_SERVICE_ACTION,
  SERVICE_CREATE: KINDS.BAHIA_REQUEST_SERVICE_CREATE,
  ENVIRONMENT_CREATE: KINDS.BAHIA_REQUEST_ENVIRONMENT_CREATE,
  DEPLOYMENT_APPROVAL: KINDS.BAHIA_REQUEST_DEPLOYMENT_APPROVAL,
  OBSERVATION_SUBMIT: KINDS.BAHIA_REQUEST_OBSERVATION_SUBMIT,
  DRIFT_REMEDIATE: KINDS.BAHIA_REQUEST_DRIFT_REMEDIATE,
  LLM_ROUTE_CREATE: KINDS.BAHIA_REQUEST_LLM_ROUTE_CREATE,
  LLM_RELEASE_REGISTER: KINDS.BAHIA_REQUEST_LLM_RELEASE_REGISTER,
  LLM_DEPLOY_REQUEST: KINDS.BAHIA_REQUEST_LLM_DEPLOY,
  LLM_DEPLOYMENT_APPROVAL: KINDS.BAHIA_REQUEST_LLM_DEPLOYMENT_APPROVAL,
  LLM_ROLLBACK_REQUEST: KINDS.BAHIA_REQUEST_LLM_ROLLBACK,
  DEPLOYMENT_STATUS: KINDS.BAHIA_DEPLOYMENT_STATUS,
  SERVICE_STATUS: KINDS.BAHIA_SERVICE_STATUS,
  LLM_DEPLOYMENT_STATUS: KINDS.BAHIA_LLM_DEPLOYMENT_STATUS,
  DEPLOYMENT_RESULT: KINDS.BAHIA_DEPLOYMENT_RESULT,
  ACTION_RESULT: KINDS.BAHIA_ACTION_RESULT,
  SERVICE_CREATE_RESULT: KINDS.BAHIA_SERVICE_CREATE_RESULT,
  ENVIRONMENT_CREATE_RESULT: KINDS.BAHIA_ENVIRONMENT_CREATE_RESULT,
  OBSERVATION_RESULT: KINDS.BAHIA_OBSERVATION_RESULT,
  REMEDIATION_RESULT: KINDS.BAHIA_REMEDIATION_RESULT,
  LLM_ROUTE_CREATE_RESULT: KINDS.BAHIA_LLM_ROUTE_CREATE_RESULT,
  LLM_RELEASE_REGISTER_RESULT: KINDS.BAHIA_LLM_RELEASE_REGISTER_RESULT,
  LLM_DEPLOYMENT_RESULT: KINDS.BAHIA_LLM_DEPLOYMENT_RESULT,
  SERVICE_STATE: KINDS.BAHIA_SERVICE_STATE,
  SERVICE_REGISTRY: KINDS.BAHIA_SERVICE_REGISTRY,
  ENVIRONMENT_REGISTRY: KINDS.BAHIA_ENVIRONMENT_REGISTRY,
  LLM_ROUTE_REGISTRY: KINDS.BAHIA_LLM_ROUTE_REGISTRY,
  LLM_ROUTE_STATE: KINDS.BAHIA_LLM_ROUTE_STATE,
  WORKER_ADVERTISEMENT: KINDS.LOOM_WORKER_AD,
  AUDIT_MIN: 31000,
  AUDIT_MAX: 31099
};

export const BAHIA_READ_MODEL_KINDS = [
  KINDS.BAHIA_SERVICE_REGISTRY,
  KINDS.BAHIA_ENVIRONMENT_REGISTRY,
  KINDS.BAHIA_SERVICE_STATE,
  KINDS.BAHIA_LLM_ROUTE_REGISTRY,
  KINDS.BAHIA_LLM_ROUTE_STATE,
  KINDS.LOOM_WORKER_AD
];

export const BAHIA_STATUS_KINDS = [
  KINDS.BAHIA_DEPLOYMENT_STATUS,
  KINDS.BAHIA_SERVICE_STATUS,
  KINDS.BAHIA_LLM_DEPLOYMENT_STATUS,
  KINDS.BAHIA_DEPLOYMENT_RESULT,
  KINDS.BAHIA_ACTION_RESULT,
  KINDS.BAHIA_SERVICE_CREATE_RESULT,
  KINDS.BAHIA_ENVIRONMENT_CREATE_RESULT,
  KINDS.BAHIA_OBSERVATION_RESULT,
  KINDS.BAHIA_REMEDIATION_RESULT,
  KINDS.BAHIA_LLM_ROUTE_CREATE_RESULT,
  KINDS.BAHIA_LLM_RELEASE_REGISTER_RESULT,
  KINDS.BAHIA_LLM_DEPLOYMENT_RESULT
];

export const BAHIA_AUDIT_KINDS = Array.from({ length: 100 }, (_, i) => 31000 + i);

export const BAHIA_CONTROLPLANE_KINDS = [
  ...BAHIA_READ_MODEL_KINDS,
  ...BAHIA_STATUS_KINDS,
  ...BAHIA_AUDIT_KINDS
];

// Default relays - can be overridden via localStorage or connect() parameter
const DEFAULT_RELAYS = [
  'wss://relay.sharegap.net',
  'wss://relay.primal.net',
  'wss://nos.lol'
];

// Storage key for user-configured relays
const RELAY_CONFIG_KEY = 'bahia_nostr_relays';

// Reconnection configuration
const MAX_RECONNECT_ATTEMPTS = 5;
const INITIAL_BACKOFF_MS = 1000;
const MAX_BACKOFF_MS = 30000;

/**
 * Get configured relays from localStorage or return defaults
 */
function getConfiguredRelays() {
  if (typeof window === 'undefined' || typeof localStorage === 'undefined' || typeof localStorage.getItem !== 'function') return DEFAULT_RELAYS;
  
  try {
    const stored = localStorage.getItem(RELAY_CONFIG_KEY);
    if (stored) {
      const relays = JSON.parse(stored);
      if (Array.isArray(relays) && relays.length > 0) {
        return relays;
      }
    }
  } catch (e) {
    console.error('[nostr] Failed to load relay config:', e);
  }
  
  return DEFAULT_RELAYS;
}

/**
 * Save relay configuration to localStorage
 */
export function saveRelayConfig(relays) {
  if (typeof window === 'undefined' || typeof localStorage === 'undefined' || typeof localStorage.setItem !== 'function') return;
  
  try {
    if (Array.isArray(relays) && relays.length > 0) {
      localStorage.setItem(RELAY_CONFIG_KEY, JSON.stringify(relays));
    } else {
      localStorage.removeItem(RELAY_CONFIG_KEY);
    }
  } catch (e) {
    console.error('[nostr] Failed to save relay config:', e);
  }
}

/**
 * Get the default relay list
 */
export function getDefaultRelays() {
  return [...DEFAULT_RELAYS];
}

export function getTagValues(event, name) {
  if (!event || !Array.isArray(event.tags)) return [];
  return event.tags
    .filter(tag => Array.isArray(tag) && tag[0] === name && tag[1])
    .map(tag => tag[1]);
}

export function getTagValue(event, name, fallback = '') {
  const values = getTagValues(event, name);
  return values.length > 0 ? values[values.length - 1] : fallback;
}

export function getDTag(event) {
  return getTagValue(event, 'd', '');
}

export function replaceableKey(event) {
  if (!event || !event.kind || !event.pubkey) return '';
  const d = getDTag(event);
  return d ? `${event.kind}:${event.pubkey}:${d}` : `${event.kind}:${event.pubkey}`;
}

export function parseJsonContent(event, fallback = {}) {
  if (!event || !event.content) return fallback;
  try {
    return JSON.parse(event.content);
  } catch {
    return fallback;
  }
}

export function isReplaceableTombstone(event) {
  const content = parseJsonContent(event, {});
  if (content?.deleted === true) return true;
  return getTagValue(event, 'deleted') === 'true';
}

export function shouldAcceptReplaceableEvent(existing, incoming) {
  if (!incoming?.id) return false;
  if (!existing) return true;
  if (existing.id === incoming.id) return false;
  const incomingCreated = Number(incoming.created_at || 0);
  const existingCreated = Number(existing.created_at || 0);
  if (incomingCreated > existingCreated) return true;
  if (incomingCreated < existingCreated) return false;
  return String(incoming.id) > String(existing.id);
}

export function upsertReplaceableEvent(map, event) {
  const key = replaceableKey(event);
  if (!key) return { accepted: false, key: '', deleted: false };
  const existing = map.get(key);
  if (!shouldAcceptReplaceableEvent(existing, event)) {
    return { accepted: false, key, deleted: false };
  }
  if (isReplaceableTombstone(event)) {
    map.set(key, event);
    return { accepted: true, key, deleted: true };
  }
  map.set(key, event);
  return { accepted: true, key, deleted: false };
}

export class NostrClient {
  constructor({ relays = getConfiguredRelays() } = {}) {
    this.relays = relays;
    this.sockets = new Map();
    this.subscriptions = new Map();
    this.subIdCounter = 0;
    this.connected = writable(false);
    this.connectionStatus = writable({});
    this.reconnectAttempts = new Map();
    this.reconnectTimers = new Map();
    this.manuallyDisconnected = false;
    this.pendingPublishes = new Map();
  }

  // Calculate exponential backoff delay
  getBackoffDelay(attempts) {
    const delay = Math.min(INITIAL_BACKOFF_MS * Math.pow(2, attempts), MAX_BACKOFF_MS);
    const jitter = delay * 0.2 * (Math.random() - 0.5);
    return Math.round(delay + jitter);
  }

  // Get current relay list
  getRelays() {
    return [...this.relays];
  }

  // Set relay list (and optionally persist)
  setRelays(relays, persist = true) {
    this.relays = relays;
    if (persist) {
      saveRelayConfig(relays);
    }
  }

  // Connect to all relays
  async connect(relays = this.relays) {
    this.manuallyDisconnected = false;
    this.relays = relays;
    console.log(`[nostr] Connecting to ${relays.length} relay(s):`, relays);
    
    // Reset reconnect attempts for fresh connection
    relays.forEach(url => this.reconnectAttempts.set(url, 0));
    const promises = relays.map(url => this.connectRelay(url));
    const results = await Promise.allSettled(promises);
    
    const connected = results.filter(r => r.status === 'fulfilled').length;
    console.log(`[nostr] Connected to ${connected}/${relays.length} relays`);
    
    this.updateConnectedStatus();
  }

  // Connect to a single relay
  connectRelay(url) {
    return new Promise((resolve) => {
      // Clear any pending reconnect timer
      if (this.reconnectTimers.has(url)) {
        clearTimeout(this.reconnectTimers.get(url));
        this.reconnectTimers.delete(url);
      }

      if (this.sockets.has(url) && this.sockets.get(url).readyState === WebSocket.OPEN) {
        resolve();
        return;
      }

      let ws;
      try {
        ws = new WebSocket(url);
      } catch (err) {
        console.error(`[nostr] Failed to create WebSocket for ${url}:`, err);
        this.connectionStatus.update(s => ({ ...s, [url]: 'error' }));
        resolve();
        return;
      }
      
      ws.onopen = () => {
        console.log(`[nostr] ✓ Connected to ${url}`);
        this.sockets.set(url, ws);
        this.connectionStatus.update(s => ({ ...s, [url]: 'connected' }));
        this.reconnectAttempts.set(url, 0);
        this.updateConnectedStatus();
        this.reissueSubscriptions(url, ws);
        resolve();
      };

      ws.onclose = () => {
        console.log(`[nostr] Disconnected from ${url}`);
        this.sockets.delete(url);
        this.connectionStatus.update(s => ({ ...s, [url]: 'disconnected' }));
        this.updateConnectedStatus();
        this.rejectPendingPublishesForRelay(url, 'relay connection closed');
        this.notifyRelayClosed(url, 'relay connection closed');
        if (!this.manuallyDisconnected) {
          this.scheduleReconnect(url);
        }
      };

      ws.onerror = (err) => {
        // Don't log full error object, just note it happened
        console.warn(`[nostr] ✗ Connection failed: ${url}`);
        this.connectionStatus.update(s => ({ ...s, [url]: 'error' }));
      };

      ws.onmessage = (e) => {
        this.handleMessage(url, e.data);
      };

      // Timeout for initial connection
      setTimeout(() => {
        if (ws.readyState === WebSocket.CONNECTING) {
          console.log(`[nostr] Connection timeout: ${url}`);
          ws.close();
          resolve();
        }
      }, 10000);
    });
  }

  // Schedule reconnection with exponential backoff
  scheduleReconnect(url) {
    const attempts = this.reconnectAttempts.get(url) || 0;
    
    if (attempts >= MAX_RECONNECT_ATTEMPTS) {
      console.log(`[nostr] Giving up on ${url} after ${MAX_RECONNECT_ATTEMPTS} attempts`);
      this.connectionStatus.update(s => ({ ...s, [url]: 'failed' }));
      return;
    }

    const delay = this.getBackoffDelay(attempts);
    console.log(`[nostr] Will retry ${url} in ${Math.round(delay/1000)}s (attempt ${attempts + 1}/${MAX_RECONNECT_ATTEMPTS})`);
    
    this.reconnectAttempts.set(url, attempts + 1);
    
    const timer = setTimeout(() => {
      this.reconnectTimers.delete(url);
      this.connectRelay(url);
    }, delay);
    
    this.reconnectTimers.set(url, timer);
  }

  // Reset and retry connection to a specific relay
  retryRelay(url) {
    this.reconnectAttempts.set(url, 0);
    if (this.reconnectTimers.has(url)) {
      clearTimeout(this.reconnectTimers.get(url));
      this.reconnectTimers.delete(url);
    }
    return this.connectRelay(url);
  }

  // Update connected store
  updateConnectedStatus() {
    const anyConnected = Array.from(this.sockets.values())
      .some(ws => ws.readyState === WebSocket.OPEN);
    this.connected.set(anyConnected);
  }

  // Handle incoming messages
  handleMessage(relay, data) {
    try {
      const msg = JSON.parse(data);
      const [type] = msg;

      switch (type) {
        case 'EVENT': {
          const [, subId, event] = msg;
          const sub = this.subscriptions.get(subId);
          if (sub && sub.onEvent) {
            sub.onEvent(event, relay);
          }
          break;
        }

        case 'EOSE': {
          const [, subId] = msg;
          const subEose = this.subscriptions.get(subId);
          if (subEose && subEose.onEose) {
            subEose.onEose(relay);
          }
          break;
        }

        case 'OK': {
          const [, eventId, accepted, message] = msg;
          this.handleOk(relay, eventId, accepted, message);
          break;
        }

        case 'CLOSED': {
          const [, subId, reason = ''] = msg;
          const subClosed = this.subscriptions.get(subId);
          if (subClosed && subClosed.onClosed) {
            subClosed.onClosed(reason, relay);
          }
          break;
        }

        case 'NOTICE':
          console.log(`[nostr] Notice from ${relay}:`, msg[1]);
          break;
      }
    } catch (err) {
      console.error('[nostr] Failed to parse message:', err);
    }
  }

  handleOk(relay, eventId, accepted, message = '') {
    const pendingByRelay = this.pendingPublishes.get(eventId);
    if (!pendingByRelay) return;

    const pending = pendingByRelay.get(relay);
    if (!pending) return;

    pendingByRelay.delete(relay);
    pending.resolve({
      relay,
      sent: true,
      accepted: accepted === true,
      message: typeof message === 'string' ? message : ''
    });

    if (pendingByRelay.size === 0) {
      this.pendingPublishes.delete(eventId);
    }
  }

  // Subscribe to events
  subscribe(filters, { onEvent, onEose, onClosed } = {}) {
    const subId = `sub_${++this.subIdCounter}`;
    
    this.subscriptions.set(subId, { filters, onEvent, onEose, onClosed, events: [] });

    this.sendSubscription(subId);

    return () => {
      this.subscriptions.delete(subId);
      const close = JSON.stringify(['CLOSE', subId]);
      this.sockets.forEach((ws) => {
        if (ws.readyState === WebSocket.OPEN) {
          ws.send(close);
        }
      });
    };
  }

  sendSubscription(subId, relayUrl = null) {
    const sub = this.subscriptions.get(subId);
    if (!sub) return;

    const req = JSON.stringify(['REQ', subId, ...sub.filters]);
    if (relayUrl) {
      const ws = this.sockets.get(relayUrl);
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(req);
      }
      return;
    }

    this.sockets.forEach((ws) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(req);
      }
    });
  }

  reissueSubscriptions(relayUrl, ws) {
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    this.subscriptions.forEach((_sub, subId) => {
      this.sendSubscription(subId, relayUrl);
    });
  }

  notifyRelayClosed(relayUrl, reason) {
    this.subscriptions.forEach((sub) => {
      if (sub.onClosed) {
        sub.onClosed(reason, relayUrl);
      }
    });
  }

  // One-shot query that resolves only when all currently connected relays send EOSE.
  async queryUntilEose(filters) {
    return new Promise((resolve) => {
      const events = [];
      const pendingRelays = new Set(
        Array.from(this.sockets.entries())
          .filter(([, ws]) => ws.readyState === WebSocket.OPEN)
          .map(([url]) => url)
      );

      if (pendingRelays.size === 0) {
        resolve([]);
        return;
      }

      const unsub = this.subscribe(filters, {
        onEvent: (event) => {
          if (!events.find(e => e.id === event.id)) {
            events.push(event);
          }
        },
        onEose: (relay) => {
          pendingRelays.delete(relay);
          if (pendingRelays.size === 0) {
            unsub();
            resolve(events);
          }
        },
        onClosed: (_reason, relay) => {
          pendingRelays.delete(relay);
          if (pendingRelays.size === 0) {
            unsub();
            resolve(events);
          }
        }
      });
    });
  }

  // One-shot query
  async query(filters, timeout = 5000) {
    return new Promise((resolve) => {
      const events = [];
      let eoseCount = 0;
      const relayCount = this.sockets.size;

      if (relayCount === 0) {
        resolve([]);
        return;
      }

      const unsub = this.subscribe(filters, {
        onEvent: (event) => {
          if (!events.find(e => e.id === event.id)) {
            events.push(event);
          }
        },
        onEose: () => {
          eoseCount++;
          if (eoseCount >= relayCount) {
            unsub();
            resolve(events);
          }
        }
      });

      setTimeout(() => {
        unsub();
        resolve(events);
      }, timeout);
    });
  }

  rejectPendingPublishesForRelay(relay, message) {
    this.pendingPublishes.forEach((pendingByRelay, eventId) => {
      const relaysToReject = relay ? [relay] : Array.from(pendingByRelay.keys());

      relaysToReject.forEach((relayUrl) => {
        const pending = pendingByRelay.get(relayUrl);
        if (!pending) return;

        pendingByRelay.delete(relayUrl);
        pending.resolve({
          relay: relayUrl,
          sent: false,
          accepted: false,
          message: message || 'relay connection closed'
        });
      });

      if (pendingByRelay.size === 0) {
        this.pendingPublishes.delete(eventId);
      }
    });
  }

  // Publish an event and wait for per-relay OK/CLOSED outcomes.
  async publish(event) {
    if (!event?.id) {
      throw new Error('Cannot publish event without id');
    }

    const msg = JSON.stringify(['EVENT', event]);
    const pendingByRelay = new Map();
    const promises = [];

    this.sockets.forEach((ws, url) => {
      if (ws.readyState !== WebSocket.OPEN) return;

      let resolvePending;
      const relayPromise = new Promise((resolve) => {
        resolvePending = resolve;
      });

      pendingByRelay.set(url, { resolve: resolvePending });
      promises.push(relayPromise);

      try {
        ws.send(msg);
      } catch (error) {
        pendingByRelay.delete(url);
        resolvePending({
          relay: url,
          sent: false,
          accepted: false,
          message: error?.message || 'failed to send event'
        });
      }
    });

    if (pendingByRelay.size === 0) {
      return [];
    }

    this.pendingPublishes.set(event.id, pendingByRelay);
    return Promise.all(promises);
  }

  // Close all connections
  disconnect() {
    this.manuallyDisconnected = true;
    this.subscriptions.clear();
    this.rejectPendingPublishesForRelay('', 'client disconnected');
    
    this.reconnectTimers.forEach(timer => clearTimeout(timer));
    this.reconnectTimers.clear();
    this.reconnectAttempts.clear();
    
    this.sockets.forEach((ws, url) => {
      ws.close();
    });
    this.sockets.clear();
    this.connected.set(false);
  }
}

// Singleton instance
export const nostr = new NostrClient();

// --- Soul Factory specific helpers ---

export async function fetchTemplates(authorPubkey = null) {
  const filter = { kinds: [KINDS.SOUL_TEMPLATE] };
  if (authorPubkey) {
    filter.authors = [authorPubkey];
  }
  return nostr.query([filter]);
}

export async function fetchSouls(authorPubkey = null) {
  const filter = { kinds: [KINDS.AGENT_SOUL] };
  if (authorPubkey) {
    filter.authors = [authorPubkey];
  }
  return nostr.query([filter]);
}

export async function fetchSoul(agentId, authorPubkey) {
  const events = await nostr.query([{
    kinds: [KINDS.AGENT_SOUL],
    '#d': [agentId],
    authors: authorPubkey ? [authorPubkey] : undefined
  }]);
  return events[0] || null;
}

export function subscribeToProvisioningProgress(requestEventId, onStatus, onResult) {
  return nostr.subscribe([
    { kinds: [KINDS.PROVISIONING_STATUS], '#e': [requestEventId] },
    { kinds: [KINDS.PROVISIONING_RESULT], '#e': [requestEventId] }
  ], {
    onEvent: (event) => {
      if (event.kind === KINDS.PROVISIONING_STATUS) {
        onStatus(parseProvisioningStatus(event));
      } else if (event.kind === KINDS.PROVISIONING_RESULT) {
        onResult(parseProvisioningResult(event));
      }
    }
  });
}

export function parseSoulEvent(event) {
  const soul = {
    id: event.id,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    content: event.content,
    agentId: '',
    name: '',
    purpose: '',
    tier: 'standard',
    status: 'active',
    deployStatus: '',
    npub: '',
    agentPubkey: '',
    avatarUrl: '',
    nip05: '',
    workspace: '',
    qdrant: '',
    bahiaServiceId: '',
    allowedKinds: [],
    tools: []
  };

  for (const tag of event.tags) {
    switch (tag[0]) {
      case 'd': soul.agentId = tag[1]; break;
      case 'name': soul.name = tag[1]; break;
      case 'purpose': soul.purpose = tag[1]; break;
      case 'tier': soul.tier = tag[1]; break;
      case 'status': soul.status = tag[1]; break;
      case 'deploy-status': soul.deployStatus = tag[1]; break;
      case 'npub': soul.npub = tag[1]; break;
      case 'p': if (tag[2] === 'agent') soul.agentPubkey = tag[1]; break;
      case 'avatar': soul.avatarUrl = tag[1]; break;
      case 'nip05': soul.nip05 = tag[1]; break;
      case 'workspace': soul.workspace = tag[1]; break;
      case 'qdrant': soul.qdrant = tag[1]; break;
      case 'service': soul.bahiaServiceId = tag[1]; break;
      case 'allowed-kind': soul.allowedKinds.push(parseInt(tag[1])); break;
      case 'tool': soul.tools.push({ server: tag[1], scopes: tag.slice(2) }); break;
    }
  }

  return soul;
}

export function parseTemplateEvent(event) {
  const template = {
    id: event.id,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    identifier: '',
    name: '',
    description: '',
    tier: 'standard',
    basePrompt: event.content,
    defaultKinds: [],
    defaultTools: [],
    tags: []
  };

  for (const tag of event.tags) {
    switch (tag[0]) {
      case 'd': template.identifier = tag[1]; break;
      case 'name': template.name = tag[1]; break;
      case 'description': template.description = tag[1]; break;
      case 'tier': template.tier = tag[1]; break;
      case 't': template.tags.push(tag[1]); break;
      case 'default-kind': template.defaultKinds.push(parseInt(tag[1])); break;
    }
  }

  return template;
}

export function parseRepositoryEvent(event) {
  if (!event || !event.id || !event.pubkey || !Array.isArray(event.tags)) {
    return null;
  }

  const repo = {
    id: event.id,
    pubkey: event.pubkey,
    created_at: event.created_at,
    identifier: '',
    name: '',
    description: '',
    webUrls: [],
    cloneUrls: [],
    relayUrls: [],
    earliestUniqueCommitId: '',
    maintainers: []
  };

  for (const tag of event.tags) {
    if (!Array.isArray(tag) || tag.length < 2) continue;

    switch (tag[0]) {
      case 'd':
        repo.identifier = tag[1] || '';
        break;
      case 'name':
        repo.name = tag[1] || '';
        break;
      case 'description':
        repo.description = tag[1] || '';
        break;
      case 'web':
        repo.webUrls.push(...tag.slice(1).filter(Boolean));
        break;
      case 'clone':
        repo.cloneUrls.push(...tag.slice(1).filter(Boolean));
        break;
      case 'relays':
        repo.relayUrls.push(...tag.slice(1).filter(Boolean));
        break;
      case 'r':
        repo.earliestUniqueCommitId = tag[1] || '';
        break;
      case 'maintainers':
        repo.maintainers.push(...tag.slice(1).filter(Boolean));
        break;
    }
  }

  if (!repo.identifier) {
    return null;
  }

  repo.repoCoordinate = `${KINDS.REPOSITORY}:${repo.pubkey}:${repo.identifier}`;
  repo.primaryUrl = repo.cloneUrls[0] || repo.webUrls[0] || '';
  repo.displayName = repo.name || repo.identifier || repo.primaryUrl;
  repo.searchText = [
    repo.identifier,
    repo.name,
    repo.description,
    repo.primaryUrl,
    repo.repoCoordinate,
    ...repo.cloneUrls,
    ...repo.webUrls,
    ...repo.relayUrls,
    ...repo.maintainers
  ].join(' ').toLowerCase();

  return repo;
}

export async function fetchRepositories({ authors = null, limit = 200, since = null } = {}) {
  const filter = {
    kinds: [KINDS.REPOSITORY],
    limit
  };

  if (Array.isArray(authors) && authors.length > 0) {
    filter.authors = authors;
  }

  if (typeof since === 'number') {
    filter.since = since;
  }

  const events = await nostr.query([filter]);
  const deduped = new Map();

  for (const event of events) {
    const parsed = parseRepositoryEvent(event);
    if (!parsed) continue;

    const existing = deduped.get(parsed.repoCoordinate);
    if (!existing || parsed.created_at >= existing.created_at) {
      deduped.set(parsed.repoCoordinate, parsed);
    }
  }

  return Array.from(deduped.values());
}

function parseProvisioningStatus(event) {
  const status = {
    id: event.id,
    step: '',
    progress: { current: 0, total: 0 },
    message: event.content
  };

  for (const tag of event.tags) {
    switch (tag[0]) {
      case 'step': status.step = tag[1]; break;
      case 'progress':
        status.progress = { current: parseInt(tag[1]), total: parseInt(tag[2]) };
        break;
    }
  }

  return status;
}

function parseProvisioningResult(event) {
  const result = {
    id: event.id,
    success: false,
    error: '',
    soulRef: '',
    data: {}
  };

  for (const tag of event.tags) {
    switch (tag[0]) {
      case 'status':
        result.success = tag[1] === 'success';
        if (tag[1] === 'error') result.error = event.content;
        break;
      case 'soul': result.soulRef = tag[1]; break;
    }
  }

  if (result.success && event.content) {
    try {
      result.data = JSON.parse(event.content);
    } catch (e) {
      // Content might not be JSON
    }
  }

  return result;
}

export default nostr;
