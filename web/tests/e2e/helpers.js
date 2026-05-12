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
    const discoveryEvents = [
      {
        id: 'e2e-system-discovery',
        kind: 31974,
        pubkey: servicePubkey,
        created_at: 1,
        tags: [['d', 'bahia-system-v1']],
        content: JSON.stringify({ ...systemInfo, schema: 'bahia.system-discovery.v1' }),
        sig: 'e2e'
      },
      {
        id: 'e2e-browser-relays',
        kind: 30002,
        pubkey: servicePubkey,
        created_at: 1,
        tags: [['d', 'bahia-browser-v1'], ...browserRelays.map((relay) => ['relay', relay])],
        content: '',
        sig: 'e2e'
      }
    ];
    const requestRelays = systemInfo?.nostr?.browser_encrypted_request_relays || [];
    if (requestRelays.length > 0) {
      discoveryEvents.push({
        id: 'e2e-request-relays',
        kind: 30002,
        pubkey: servicePubkey,
        created_at: 1,
        tags: [['d', 'bahia-requests-v1'], ...requestRelays.map((relay) => ['relay', relay])],
        content: '',
        sig: 'e2e'
      });
    }
    const serviceRelays = systemInfo?.nostr?.service_relays || browserRelays;
    discoveryEvents.push({
      id: 'e2e-service-relays',
      kind: 30002,
      pubkey: servicePubkey,
      created_at: 1,
      tags: [['d', 'bahia-service-v1'], ...serviceRelays.map((relay) => ['relay', relay])],
      content: '',
      sig: 'e2e'
    });
    const existingNostrEvents = localStorage.getItem('__bahia_e2e_nostr_events');
    if (!existingNostrEvents || (Array.isArray(nostrEvents) && nostrEvents.length > 0)) {
      localStorage.setItem('__bahia_e2e_nostr_events', JSON.stringify([...discoveryEvents, ...(nostrEvents || [])]));
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
      window.nostr = {
        getPublicKey: async () => pubkey,
        signEvent: async (event) => ({
          ...event,
          pubkey,
          id: `mock-event-id-${Date.now()}`,
          sig: `mock-signature-${Math.random().toString(36).slice(2)}`
        }),
        getRelays: async () => ({
          'wss://relay.example.com': { read: true, write: true }
        })
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

    function deliverMockEvent(socket, subId, event) {
      if (socket.readyState !== MockWebSocket.OPEN) return;
      socket.onmessage?.({ data: JSON.stringify(['EVENT', subId, event]) });
    }

    function persistMockNostrEvent(event) {
      const events = readMockNostrEvents();
      if (!event?.id || events.some((existing) => existing?.id === event.id)) {
        return;
      }
      events.push(event);
      localStorage.setItem('__bahia_e2e_nostr_events', JSON.stringify(events));
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
          events.forEach((event, index) => {
            setTimeout(() => deliverMockEvent(this, subId, event), index * 5);
          });
          setTimeout(() => {
            if (this.readyState === MockWebSocket.OPEN) {
              this.onmessage?.({ data: JSON.stringify(['EOSE', subId]) });
            }
          }, events.length * 5);
        } else if (Array.isArray(message) && message[0] === 'CLOSE') {
          this.subscriptions.delete(message[1]);
        } else if (Array.isArray(message) && message[0] === 'EVENT') {
          const event = message[1];
          persistMockNostrEvent(event);
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
    localStorage.setItem('__bahia_e2e_nostr_events', JSON.stringify(events || []));
  }, events);
}
