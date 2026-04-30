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
  REPOSITORY: 30617
};

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
  if (typeof localStorage === 'undefined') return DEFAULT_RELAYS;
  
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
  if (typeof localStorage === 'undefined') return;
  
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

class NostrClient {
  constructor() {
    this.relays = getConfiguredRelays();
    this.sockets = new Map();
    this.subscriptions = new Map();
    this.subIdCounter = 0;
    this.connected = writable(false);
    this.connectionStatus = writable({});
    this.reconnectAttempts = new Map();
    this.reconnectTimers = new Map();
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
        resolve();
      };

      ws.onclose = () => {
        console.log(`[nostr] Disconnected from ${url}`);
        this.sockets.delete(url);
        this.connectionStatus.update(s => ({ ...s, [url]: 'disconnected' }));
        this.updateConnectedStatus();
        this.scheduleReconnect(url);
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
      const [type, subId, ...rest] = msg;

      switch (type) {
        case 'EVENT':
          const event = rest[0];
          const sub = this.subscriptions.get(subId);
          if (sub && sub.onEvent) {
            sub.onEvent(event, relay);
          }
          break;

        case 'EOSE':
          const subEose = this.subscriptions.get(subId);
          if (subEose && subEose.onEose) {
            subEose.onEose(relay);
          }
          break;

        case 'OK':
          const [eventId, success, message] = rest;
          break;

        case 'NOTICE':
          console.log(`[nostr] Notice from ${relay}:`, rest[0]);
          break;
      }
    } catch (err) {
      console.error('[nostr] Failed to parse message:', err);
    }
  }

  // Subscribe to events
  subscribe(filters, { onEvent, onEose } = {}) {
    const subId = `sub_${++this.subIdCounter}`;
    
    this.subscriptions.set(subId, { filters, onEvent, onEose, events: [] });

    const req = JSON.stringify(['REQ', subId, ...filters]);
    this.sockets.forEach((ws, url) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(req);
      }
    });

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

  // Publish an event
  async publish(event) {
    const msg = JSON.stringify(['EVENT', event]);
    const results = [];

    this.sockets.forEach((ws, url) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(msg);
        results.push({ relay: url, sent: true });
      }
    });

    return results;
  }

  // Close all connections
  disconnect() {
    this.subscriptions.clear();
    
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
