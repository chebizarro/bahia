export const TEST_PUBKEY = 'f'.repeat(64);

/**
 * Install browser-side mocks that must exist before Svelte mounts.
 * This gives protected-route smoke tests a persisted backend session and a
 * deterministic NIP-07/EventSource environment without requiring extensions or
 * a live backend.
 */
export async function installE2EMocks(page, { authenticated = true, extension = true, sseEvents = [] } = {}) {
  await page.addInitScript(({ authenticated, extension, pubkey, sseEvents }) => {
    localStorage.setItem('__bahia_e2e_sse_events', JSON.stringify(sseEvents || []));
    sessionStorage.removeItem('bahia_dashboard_pending_deployments');

    if (authenticated) {
      localStorage.setItem('bahia_token', 'e2e-test-token');
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
        setTimeout(() => this.onopen?.({ type: 'open', target: this }), 0);
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
          setTimeout(() => {
            this.onmessage?.({ data: JSON.stringify(['EOSE', subId]) });
          }, 0);
        } else if (Array.isArray(message) && message[0] === 'EVENT') {
          const event = message[1];
          setTimeout(() => {
            this.onmessage?.({ data: JSON.stringify(['OK', event?.id, true, '']) });
          }, 0);
        }
      }

      close() {
        this.readyState = MockWebSocket.CLOSED;
        this.onclose?.({ type: 'close', target: this });
      }
    }

    window.WebSocket = MockWebSocket;

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
  }, { authenticated, extension, pubkey: TEST_PUBKEY, sseEvents });
}

export async function seedSseEvents(page, events) {
  await page.addInitScript((events) => {
    localStorage.setItem('__bahia_e2e_sse_events', JSON.stringify(events || []));
  }, events);
}
