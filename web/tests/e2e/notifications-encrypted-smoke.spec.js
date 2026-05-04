import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

const SERVICE_PUBKEY = 'b'.repeat(64);
const PUBLIC_RELAY = 'ws://public.test.local';
const ENCRYPTED_RELAY = 'ws://encrypted.test.local';
const now = new Date().toISOString();

const systemInfo = {
  nostr: {
    browser_relays: [PUBLIC_RELAY],
    browser_encrypted_request_relays: [ENCRYPTED_RELAY],
    service_pubkey: SERVICE_PUBKEY
  },
  features: {
    relay_sidecar: true,
    relay_read_models: true,
    encrypted_nostr_requests: true,
    legacy_sse: false
  }
};

const initialChannels = [
  {
    id: 'ch-1',
    name: 'Ops Webhook',
    channel_type: 'webhook',
    config: { url: 'https://hooks.example.com/ops' },
    event_filter: { types: ['deployment.failed'] },
    enabled: true,
    created_at: now,
    updated_at: now
  }
];

function installEncryptedNotificationHarness(page) {
  return page.addInitScript(({ servicePubkey, encryptedRelay, publicRelay, initialChannels, initialLogs }) => {
    window.__BAHIA_E2E_ENCRYPTED_PUBLISHES = [];
    window.__BAHIA_E2E_ENCRYPTED_OPERATIONS = [];
    window.__BAHIA_E2E_NOTIFICATION_STATE = {
      nextId: 2,
      channels: (initialChannels || []).map((channel) => ({ ...channel })),
      logs: (initialLogs || []).map((log) => ({ ...log }))
    };

    window.nostr = {
      ...(window.nostr || {}),
      nip44: {
        encrypt: async (_recipientPubkey, plaintext) => `enc44:${plaintext}`,
        decrypt: async (_senderPubkey, ciphertext) => {
          if (typeof ciphertext !== 'string' || !ciphertext.startsWith('enc44:')) {
            throw new Error('bad ciphertext');
          }
          return ciphertext.slice('enc44:'.length);
        }
      }
    };

    function matchesFilter(event, filter) {
      if (!filter || typeof filter !== 'object') return true;
      if (Array.isArray(filter.kinds) && !filter.kinds.includes(event.kind)) return false;
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

    function notificationResult(operation, payload = {}) {
      const state = window.__BAHIA_E2E_NOTIFICATION_STATE;
      switch (operation) {
        case 'notifications.channels.list':
          return { status: 'ok', payload: { channels: [...state.channels] } };
        case 'notifications.channels.create': {
          const channel = {
            id: `ch-${state.nextId++}`,
            name: payload.name,
            channel_type: payload.channel_type,
            config: payload.config || {},
            event_filter: payload.event_filter || {},
            enabled: payload.enabled !== false,
            created_at: new Date().toISOString(),
            updated_at: new Date().toISOString()
          };
          state.channels = [channel, ...state.channels];
          return { status: 'ok', payload: { channel } };
        }
        case 'notifications.channels.update': {
          const index = state.channels.findIndex((channel) => channel.id === payload.id);
          if (index === -1) {
            return { status: 'error', error: { code: 'not_found', message: 'notification channel not found' } };
          }
          const current = state.channels[index];
          const next = {
            ...current,
            ...payload,
            config: payload.config ? { ...payload.config } : current.config,
            event_filter: payload.event_filter ? { ...payload.event_filter } : current.event_filter,
            updated_at: new Date().toISOString()
          };
          state.channels = state.channels.map((channel, i) => (i === index ? next : channel));
          return { status: 'ok', payload: { channel: next } };
        }
        case 'notifications.channels.test':
          return { status: 'ok', payload: { status: 'test sent' } };
        case 'notifications.channels.delete':
          state.channels = state.channels.filter((channel) => channel.id !== payload.id);
          return { status: 'ok', payload: { status: 'deleted', id: payload.id } };
        case 'notifications.logs.list':
          return { status: 'ok', payload: { logs: [...state.logs] } };
        default:
          return { status: 'error', error: { code: 'unsupported_operation', message: `unsupported encrypted op: ${operation}` } };
      }
    }

    const OriginalWebSocket = window.WebSocket;
    const originalSend = OriginalWebSocket.prototype.send;

    OriginalWebSocket.prototype.send = function patchedSend(data) {
      let message;
      try {
        message = JSON.parse(data);
      } catch {
        return originalSend.call(this, data);
      }

      if (Array.isArray(message) && message[0] === 'REQ') {
        this.__bahiaSubs ??= new Map();
        this.__bahiaSubs.set(message[1], message.slice(2));
        return originalSend.call(this, data);
      }

      if (Array.isArray(message) && message[0] === 'CLOSE') {
        this.__bahiaSubs?.delete(message[1]);
        return originalSend.call(this, data);
      }

      if (Array.isArray(message) && message[0] === 'EVENT' && message[1]?.kind === 5980) {
        const event = message[1];
        const relay = this.url;
        const plaintext = String(event.content || '').replace(/^enc44:/, '');
        const envelope = JSON.parse(plaintext);
        const result = notificationResult(envelope.operation, envelope.payload || {});
        window.__BAHIA_E2E_ENCRYPTED_PUBLISHES.push({ relay, eventId: event.id });
        window.__BAHIA_E2E_ENCRYPTED_OPERATIONS.push(envelope.operation);

        originalSend.call(this, data);

        const resultEvent = {
          id: `result-${event.id}`,
          kind: 7980,
          pubkey: servicePubkey,
          created_at: Math.floor(Date.now() / 1000),
          tags: [
            ['e', event.id],
            ['p', event.pubkey],
            ['encrypted', 'bahia-encrypted-v1']
          ],
          content: `enc44:${JSON.stringify(
            result.status === 'error'
              ? { request_event_id: event.id, status: 'error', error: result.error }
              : { request_event_id: event.id, status: 'ok', payload: result.payload }
          )}`,
          sig: '0'.repeat(128)
        };

        setTimeout(() => {
          if (this.readyState !== OriginalWebSocket.OPEN) return;
          const subs = this.__bahiaSubs || new Map();
          for (const [subId, filters] of subs.entries()) {
            if (Array.isArray(filters) && filters.some((filter) => matchesFilter(resultEvent, filter))) {
              this.onmessage?.({ data: JSON.stringify(['EVENT', subId, resultEvent]) });
            }
          }
        }, 0);

        return;
      }

      return originalSend.call(this, data);
    };

    window.__BAHIA_E2E_ENCRYPTED_EXPECTED_RELAYS = { publicRelay, encryptedRelay };
  }, {
    servicePubkey: SERVICE_PUBKEY,
    encryptedRelay: ENCRYPTED_RELAY,
    publicRelay: PUBLIC_RELAY,
    initialChannels,
    initialLogs: []
  });
}

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page, { systemInfo });
  await installEncryptedNotificationHarness(page);
});

test.describe('Notifications encrypted transport smoke', () => {
  test('browser notifications flow uses encrypted request/result transport end-to-end', async ({ page }) => {
    await page.goto('/notifications');

    await expect(page.getByRole('heading', { name: 'Notifications' })).toBeVisible();
    await expect(page.getByText('Ops Webhook')).toBeVisible();
    await expect(page.getByText('https://hooks.example.com/ops')).toBeVisible();

    await page.getByRole('button', { name: 'Create channel' }).click();
    await expect(page).toHaveURL(/\/notifications\/new$/);
    await expect(page.getByRole('heading', { name: 'Create notification channel' })).toBeVisible();

    await page.locator('#notification-channel-name').fill('PagerDuty Webhook');
    await page.locator('#webhook-url').fill('https://hooks.example.com/pagerduty');
    await page.locator('form').getByRole('button', { name: 'Create channel' }).click();

    await expect(page).toHaveURL(/\/notifications$/);
    const row = page.locator('tr', { hasText: 'PagerDuty Webhook' });
    await expect(row).toBeVisible();
    await expect(page.getByText('PagerDuty Webhook created')).toBeVisible();

    await row.getByRole('button', { name: 'Test' }).click();
    await expect(page.getByText('Test notification sent to PagerDuty Webhook')).toBeVisible();

    const transportTrace = await page.evaluate(() => ({
      relays: window.__BAHIA_E2E_ENCRYPTED_PUBLISHES.map((entry) => entry.relay),
      operations: [...window.__BAHIA_E2E_ENCRYPTED_OPERATIONS]
    }));

    expect(transportTrace.relays.length).toBeGreaterThanOrEqual(3);
    expect(transportTrace.relays.every((relay) => relay === 'ws://encrypted.test.local')).toBe(true);
    expect(transportTrace.relays.some((relay) => relay === 'ws://public.test.local')).toBe(false);
    expect(transportTrace.operations).toEqual(expect.arrayContaining([
      'notifications.channels.list',
      'notifications.channels.create',
      'notifications.channels.test'
    ]));
  });
});
