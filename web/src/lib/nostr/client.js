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
  SOUL_ACTION: 1950
};

// Default relays
const DEFAULT_RELAYS = [
  'wss://relay.sharegap.net',
  'wss://armada.sharegap.net'
];

class NostrClient {
  constructor() {
    this.relays = DEFAULT_RELAYS;
    this.sockets = new Map();
    this.subscriptions = new Map();
    this.subIdCounter = 0;
    this.connected = writable(false);
    this.connectionStatus = writable({});
  }

  // Connect to all relays
  async connect(relays = this.relays) {
    this.relays = relays;
    const promises = relays.map(url => this.connectRelay(url));
    await Promise.allSettled(promises);
    this.updateConnectedStatus();
  }

  // Connect to a single relay
  connectRelay(url) {
    return new Promise((resolve, reject) => {
      if (this.sockets.has(url) && this.sockets.get(url).readyState === WebSocket.OPEN) {
        resolve();
        return;
      }

      const ws = new WebSocket(url);
      
      ws.onopen = () => {
        console.log(`[nostr] Connected to ${url}`);
        this.sockets.set(url, ws);
        this.connectionStatus.update(s => ({ ...s, [url]: 'connected' }));
        this.updateConnectedStatus();
        resolve();
      };

      ws.onclose = () => {
        console.log(`[nostr] Disconnected from ${url}`);
        this.sockets.delete(url);
        this.connectionStatus.update(s => ({ ...s, [url]: 'disconnected' }));
        this.updateConnectedStatus();
        // Auto-reconnect after 5 seconds
        setTimeout(() => this.connectRelay(url), 5000);
      };

      ws.onerror = (err) => {
        console.error(`[nostr] Error on ${url}:`, err);
        this.connectionStatus.update(s => ({ ...s, [url]: 'error' }));
        reject(err);
      };

      ws.onmessage = (e) => {
        this.handleMessage(url, e.data);
      };
    });
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
          // Handle publish confirmation
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

    // Send REQ to all connected relays
    const req = JSON.stringify(['REQ', subId, ...filters]);
    this.sockets.forEach((ws, url) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(req);
      }
    });

    // Return unsubscribe function
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

      const unsub = this.subscribe(filters, {
        onEvent: (event) => {
          // Deduplicate by event ID
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

      // Timeout fallback
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

// Fetch all soul templates
export async function fetchTemplates(authorPubkey = null) {
  const filter = { kinds: [KINDS.SOUL_TEMPLATE] };
  if (authorPubkey) {
    filter.authors = [authorPubkey];
  }
  return nostr.query([filter]);
}

// Fetch all agent souls
export async function fetchSouls(authorPubkey = null) {
  const filter = { kinds: [KINDS.AGENT_SOUL] };
  if (authorPubkey) {
    filter.authors = [authorPubkey];
  }
  return nostr.query([filter]);
}

// Fetch a specific soul by agent ID
export async function fetchSoul(agentId, authorPubkey) {
  const events = await nostr.query([{
    kinds: [KINDS.AGENT_SOUL],
    '#d': [agentId],
    authors: authorPubkey ? [authorPubkey] : undefined
  }]);
  return events[0] || null;
}

// Subscribe to provisioning progress
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

// Parse a soul event into a structured object
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

// Parse a template event
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

// Parse provisioning status event
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

// Parse provisioning result event
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
