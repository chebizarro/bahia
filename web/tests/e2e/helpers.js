export const TEST_PUBKEY = 'f'.repeat(64);

const DEFAULT_DISCOVERY_INFO = {
  nostr: {
    browser_relays: [],
    service_pubkey: 'b'.repeat(64)
  },
  features: {
    relay_sidecar: true,
    relay_read_models: true,
    legacy_sse: false
  }
};

/**
 * Install browser-side mocks that must exist before Svelte mounts.
 * This gives protected-route smoke tests a persisted NIP-07 identity and a
 * deterministic WebSocket relay environment without requiring extensions, a live
 * backend, or a live relay.
 */
export async function installE2EMocks(
  page,
  {
    authenticated = true,
    extension = true,
    sseEvents = [],
    nostrEvents = [],
    systemInfo = DEFAULT_DISCOVERY_INFO,
    routeRoleRequirements = null
  } = {}
) {
  await page.addInitScript(({ authenticated, extension, pubkey, sseEvents, nostrEvents, systemInfo, routeRoleRequirements }) => {
    const existingSseEvents = localStorage.getItem('__bahia_e2e_sse_events');
    if (!existingSseEvents || (Array.isArray(sseEvents) && sseEvents.length > 0)) {
      localStorage.setItem('__bahia_e2e_sse_events', JSON.stringify(sseEvents || []));
    }
    const servicePubkey = systemInfo?.nostr?.service_pubkey || 'b'.repeat(64);
    const browserRelays = systemInfo?.nostr?.browser_relays || [];
    window.__BAHIA_BOOTSTRAP__ = {
      schema: 'bahia.bootstrap.v1',
      relay_urls: browserRelays,
      service_pubkeys: [servicePubkey]
    };
    window.__BAHIA_E2E_TRUST_MOCK_RELAY_EVENTS = true;
    const discoveryEvents = [
      {
        id: 'e2e-system-discovery',
        kind: 11316,
        pubkey: servicePubkey,
        created_at: 1,
        tags: [['d', 'bahia-system-v1']],
        content: JSON.stringify({ ...systemInfo, schema: 'bahia.system-discovery.v1' }),
        sig: '0'.repeat(128)
      },
      {
        id: 'e2e-browser-relays',
        kind: 30002,
        pubkey: servicePubkey,
        created_at: 1,
        tags: [['d', 'bahia-browser-v1'], ...browserRelays.map((relay) => ['relay', relay])],
        content: '',
        sig: '0'.repeat(128)
      }
    ];
    const serviceRelays = systemInfo?.nostr?.service_relays || browserRelays;
    discoveryEvents.push({
      id: 'e2e-service-relays',
      kind: 30002,
      pubkey: servicePubkey,
      created_at: 1,
      tags: [['d', 'bahia-service-v1'], ...serviceRelays.map((relay) => ['relay', relay])],
      content: '',
      sig: '0'.repeat(128)
    });
    const existingNostrEvents = localStorage.getItem('__bahia_e2e_nostr_events');
    const seedAlreadyApplied = sessionStorage.getItem('__bahia_e2e_nostr_seeded') === 'true';
    if (!existingNostrEvents || (Array.isArray(nostrEvents) && nostrEvents.length > 0 && !seedAlreadyApplied)) {
      localStorage.setItem('__bahia_e2e_nostr_events', JSON.stringify([...discoveryEvents, ...(nostrEvents || [])]));
      sessionStorage.setItem('__bahia_e2e_nostr_seeded', 'true');
    }
    if (routeRoleRequirements && typeof routeRoleRequirements === 'object') {
      window.__BAHIA_E2E_ROUTE_ROLE_REQUIREMENTS = routeRoleRequirements;
    } else {
      delete window.__BAHIA_E2E_ROUTE_ROLE_REQUIREMENTS;
    }
    sessionStorage.removeItem('bahia_dashboard_pending_deployments');

    if (authenticated) {
      localStorage.removeItem('bahia_token');
      localStorage.setItem('bahia_auth_session', JSON.stringify({
        pubkey,
        relays: {
          'wss://relay.example.com': { read: true, write: true }
        },
        lastAuthenticatedAt: new Date().toISOString()
      }));
    } else {
      localStorage.removeItem('bahia_token');
      localStorage.removeItem('bahia_auth_session');
    }

    if (extension) {
      const encodeMockCiphertext = (plaintext) => `mock-nip44:${btoa(unescape(encodeURIComponent(plaintext)))}`;
      const decodeMockCiphertext = (ciphertext) => decodeURIComponent(escape(atob(String(ciphertext).replace(/^mock-nip44:/, ''))));
      window.nostr = {
        getPublicKey: async () => pubkey,
        signEvent: async (event) => ({
          ...event,
          pubkey,
          id: `mock-event-id-${Date.now()}-${Math.random().toString(36).slice(2)}`,
          sig: `mock-signature-${Math.random().toString(36).slice(2)}`
        }),
        getRelays: async () => ({
          'wss://relay.example.com': { read: true, write: true }
        }),
        nip44: {
          encrypt: async (_recipient, plaintext) => encodeMockCiphertext(plaintext),
          decrypt: async (_sender, ciphertext) => decodeMockCiphertext(ciphertext)
        }
      };
    } else {
      delete window.nostr;
    }

    function readMockNostrEvents() {
      try {
        return JSON.parse(localStorage.getItem('__bahia_e2e_nostr_events') || '[]');
      } catch {
        return [];
      }
    }

    function matchesFilter(event, filter) {
      if (!filter || typeof filter !== 'object') return true;
      if (Array.isArray(filter.kinds) && !filter.kinds.includes(event.kind)) return false;
      if (typeof filter.since === 'number' && Number(event.created_at || 0) < filter.since) return false;
      if (Array.isArray(filter.authors) && !filter.authors.includes(event.pubkey)) return false;
      for (const [key, values] of Object.entries(filter)) {
        if (!key.startsWith('#') || !Array.isArray(values)) continue;
        const tagName = key.slice(1);
        const tags = Array.isArray(event.tags) ? event.tags : [];
        if (!tags.some((tag) => Array.isArray(tag) && tag[0] === tagName && values.includes(tag[1]))) {
          return false;
        }
      }
      return true;
    }

    window.__BAHIA_E2E_WS_CONNECTIONS = [];

    async function sha256Hex(input) {
      const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(input));
      return Array.from(new Uint8Array(digest)).map((byte) => byte.toString(16).padStart(2, '0')).join('');
    }

    async function normalizeMockEventForDelivery(event) {
      const now = Math.floor(Date.now() / 1000);
      const createdAt = Number.isInteger(event?.created_at) && event.created_at > now - 365 * 24 * 60 * 60 && event.created_at <= now + 600
        ? event.created_at
        : now;
      const normalized = {
        ...event,
        pubkey: typeof event?.pubkey === 'string' && /^[0-9a-f]{64}$/.test(event.pubkey) ? event.pubkey : servicePubkey,
        created_at: createdAt,
        tags: Array.isArray(event?.tags) ? event.tags.map((tag) => Array.isArray(tag) ? tag.map((value) => String(value)) : []).filter((tag) => tag.length > 0) : [],
        content: typeof event?.content === 'string' ? event.content : JSON.stringify(event?.content ?? {}),
        sig: typeof event?.sig === 'string' && /^[0-9a-f]{128}$/.test(event.sig) ? event.sig : '0'.repeat(128)
      };
      normalized.id = await sha256Hex(JSON.stringify([0, normalized.pubkey, normalized.created_at, normalized.kind, normalized.tags, normalized.content]));
      return normalized;
    }

    function deliverMockEvent(socket, subId, event) {
      if (socket.readyState !== MockWebSocket.OPEN) return;
      void normalizeMockEventForDelivery(event).then((normalized) => {
        if (socket.readyState !== MockWebSocket.OPEN) return;
        socket.onmessage?.({ data: JSON.stringify(['EVENT', subId, normalized]) });
      });
    }

    function persistMockNostrEvent(event) {
      const events = readMockNostrEvents();
      if (!event?.id || events.some((existing) => existing?.id === event.id)) {
        return;
      }
      events.push(event);
      localStorage.setItem('__bahia_e2e_nostr_events', JSON.stringify(events));
    }

    function readMockServiceSecrets() {
      try { return JSON.parse(localStorage.getItem('__bahia_e2e_service_secrets') || '{}'); } catch { return {}; }
    }

    function writeMockServiceSecrets(state) {
      localStorage.setItem('__bahia_e2e_service_secrets', JSON.stringify(state || {}));
    }

    function handleEncryptedServiceSecretRequest(event) {
      if (event?.kind !== 25910 || !String(event.content || '').startsWith('mock-nip44:')) return null;
      let envelope;
      try {
        envelope = JSON.parse(decodeURIComponent(escape(atob(String(event.content).replace(/^mock-nip44:/, '')))));
      } catch {
        return null;
      }
      const operation = String(envelope.method || envelope.operation || '');
      const params = { ...(envelope.params || envelope.payload || {}) };
      delete params._meta;
      if (!operation.startsWith('services/secrets-') && !operation.startsWith('services.secrets.')) return null;

      const state = readMockServiceSecrets();
      const serviceId = params.service_id || 'service-1';
      const secrets = Array.isArray(state[serviceId]) ? state[serviceId] : [];
      let payload = {};
      if (operation === 'services/secrets-list' || operation === 'services.secrets.list') {
        payload = { secrets: secrets.map(({ value, ...secret }) => secret) };
      } else if (operation === 'services/secrets-create' || operation === 'services.secrets.create') {
        const secret = {
          id: `secret-${Date.now()}`,
          service_id: serviceId,
          name: params.name,
          value: params.value,
          version: 1,
          created_at: new Date().toISOString()
        };
        state[serviceId] = [secret, ...secrets];
        writeMockServiceSecrets(state);
        const { value, ...safeSecret } = secret;
        payload = { secret: safeSecret };
      } else if (operation === 'services/secrets-update' || operation === 'services.secrets.update') {
        const updated = secrets.map((secret) => secret.id === params.secret_id
          ? { ...secret, value: params.value, version: Number(secret.version || 1) + 1, updated_at: new Date().toISOString() }
          : secret);
        state[serviceId] = updated;
        writeMockServiceSecrets(state);
        const match = updated.find((secret) => secret.id === params.secret_id) || {};
        const { value, ...safeSecret } = match;
        payload = { secret: safeSecret };
      } else if (operation === 'services/secrets-delete' || operation === 'services.secrets.delete') {
        state[serviceId] = secrets.filter((secret) => secret.id !== params.secret_id);
        writeMockServiceSecrets(state);
        payload = { deleted: true };
      } else if (operation === 'services/secrets-reveal' || operation === 'services.secrets.reveal') {
        payload = { value: secrets.find((secret) => secret.id === params.secret_id)?.value || '' };
      }

      const response = {
        jsonrpc: '2.0',
        id: envelope.id || event.id,
        result: { status: 'success', payload }
      };
      return {
        id: `mock-result-${event.id}`,
        kind: 25910,
        pubkey: servicePubkey,
        created_at: Math.floor(Date.now() / 1000),
        tags: [['e', event.id], ['p', event.pubkey], ['encrypted', 'contextvm-jsonrpc-v1']],
        content: `mock-nip44:${btoa(unescape(encodeURIComponent(JSON.stringify(response))))}`,
        sig: 'mock-service-signature'
      };
    }

    class MockWebSocket {
      static CONNECTING = 0;
      static OPEN = 1;
      static CLOSING = 2;
      static CLOSED = 3;

      constructor(url) {
        this.url = url;
        this.readyState = MockWebSocket.OPEN;
        this.onopen = null;
        this.onmessage = null;
        this.onerror = null;
        this.onclose = null;
        this.subscriptions = new Map();
        window.__BAHIA_E2E_WS_CONNECTIONS.push(this);
        setTimeout(() => this.onopen?.({ type: 'open', target: this }), 0);
      }

      emitEvent(event) {
        this.subscriptions.forEach((filters, subId) => {
          if (filters.some((filter) => matchesFilter(event, filter))) {
            deliverMockEvent(this, subId, event);
          }
        });
      }

      send(data) {
        let message;
        try {
          message = JSON.parse(data);
        } catch {
          return;
        }

        if (Array.isArray(message) && message[0] === 'REQ') {
          const subId = message[1];
          const filters = message.slice(2);
          this.subscriptions.set(subId, filters);
          const events = readMockNostrEvents().filter((event) => filters.some((filter) => matchesFilter(event, filter)));
          void Promise.all(events.map((event) => normalizeMockEventForDelivery(event))).then(async (normalizedEvents) => {
            if (this.readyState !== MockWebSocket.OPEN) return;
            for (const event of normalizedEvents) {
              await this.onmessage?.({ data: JSON.stringify(['EVENT', subId, event]) });
            }
            await this.onmessage?.({ data: JSON.stringify(['EOSE', subId]) });
          });
        } else if (Array.isArray(message) && message[0] === 'CLOSE') {
          this.subscriptions.delete(message[1]);
        } else if (Array.isArray(message) && message[0] === 'EVENT') {
          const event = message[1];
          persistMockNostrEvent(event);
          const encryptedResult = handleEncryptedServiceSecretRequest(event);
          if (encryptedResult) {
            persistMockNostrEvent(encryptedResult);
            setTimeout(() => this.emitEvent(encryptedResult), 0);
          }
          setTimeout(() => {
            this.onmessage?.({ data: JSON.stringify(['OK', event?.id, true, '']) });
          }, 0);
        }
      }

      close() {
        this.readyState = MockWebSocket.CLOSED;
        window.__BAHIA_E2E_WS_CONNECTIONS = (window.__BAHIA_E2E_WS_CONNECTIONS || []).filter((socket) => socket !== this);
        this.onclose?.({ type: 'close', target: this });
      }
    }

    window.WebSocket = MockWebSocket;
    window.__bahiaPushNostrEvent = (event) => {
      persistMockNostrEvent(event);
      for (const socket of window.__BAHIA_E2E_WS_CONNECTIONS || []) {
        socket.emitEvent?.(event);
      }
    };

    class MockEventSource {
      static CONNECTING = 0;
      static OPEN = 1;
      static CLOSED = 2;

      constructor(url) {
        this.url = url;
        this.readyState = MockEventSource.CONNECTING;
        this.onopen = null;
        this.onmessage = null;
        this.onerror = null;
        this.listeners = new Map();

        setTimeout(() => {
          if (this.readyState === MockEventSource.CLOSED) return;
          this.readyState = MockEventSource.OPEN;
          const openEvent = { type: 'open', target: this };
          this.onopen?.(openEvent);
          this.dispatchEvent(openEvent);

          let events = [];
          try {
            events = JSON.parse(localStorage.getItem('__bahia_e2e_sse_events') || '[]');
          } catch {
            events = [];
          }

          const deliverEvents = () => {
            if (!this.onmessage && !this.listeners.has('message')) {
              setTimeout(deliverEvents, 20);
              return;
            }

            events.forEach((event, index) => {
              setTimeout(() => {
                if (this.readyState === MockEventSource.CLOSED) return;
                const messageEvent = { type: 'message', data: JSON.stringify(event), target: this };
                this.onmessage?.(messageEvent);
                this.dispatchEvent(messageEvent);
              }, index * 5);
            });
          };

          deliverEvents();
        }, 0);
      }

      addEventListener(type, listener) {
        if (!this.listeners.has(type)) {
          this.listeners.set(type, new Set());
        }
        this.listeners.get(type).add(listener);
      }

      removeEventListener(type, listener) {
        this.listeners.get(type)?.delete(listener);
      }

      dispatchEvent(event) {
        this.listeners.get(event.type)?.forEach((listener) => listener.call(this, event));
      }

      close() {
        this.readyState = MockEventSource.CLOSED;
      }
    }

    window.EventSource = MockEventSource;
  }, { authenticated, extension, pubkey: TEST_PUBKEY, sseEvents, nostrEvents, systemInfo, routeRoleRequirements });
}

export async function seedSseEvents(page, events) {
  await page.addInitScript((events) => {
    localStorage.setItem('__bahia_e2e_sse_events', JSON.stringify(events || []));
  }, events);
}

export async function seedNostrEvents(page, events) {
  await page.addInitScript((events) => {
    let existing = [];
    try {
      existing = JSON.parse(localStorage.getItem('__bahia_e2e_nostr_events') || '[]');
    } catch {
      existing = [];
    }
    const bootstrapEvents = existing.filter((event) => event?.kind === 11316 || event?.kind === 30002);
    localStorage.setItem('__bahia_e2e_nostr_events', JSON.stringify([...bootstrapEvents, ...(events || [])]));
  }, events);
}
