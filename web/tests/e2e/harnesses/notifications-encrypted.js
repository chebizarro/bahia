import { E2E_SERVICE_PUBKEY, TEST_PUBKEY } from '../helpers.js';
import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';
import { base64Encode, CONFIDENTIAL_ALGORITHM, CONFIDENTIAL_SCHEMA, OCK_WRAP_SCHEMA } from '../../../src/lib/nostr/confidential.js';
import { confidentialCpStateFixture } from '../cp-state-fixtures.js';

export const SERVICE_PUBKEY = E2E_SERVICE_PUBKEY;
export const PUBLIC_RELAY = 'ws://public.test.local';
export const ENCRYPTED_RELAY = 'ws://encrypted.test.local';
export const KIND_CONTEXTVM = 25910;
export const KIND_GIFT_WRAP = 1059;
export const NOTIFICATION_TEST_ORG_ID = '0199c749-9300-7444-8444-444444444444';

export function notificationRelayFixtures(channels = []) {
  if (!channels.length) return [];
  const key = new Uint8Array(32).fill(7);
  const orgId = channels[0].org_id || NOTIFICATION_TEST_ORG_ID;
  const wrap = { schema: OCK_WRAP_SCHEMA, org_id: orgId, key_ref: `ock:${orgId}`,
    version: 1, key: base64Encode(key), recipient_pubkey: TEST_PUBKEY };
  const events = [confidentialCpStateFixture({
    d: `org-key:${orgId}:v1:e2e-notification`, topic: 'org-key-envelope', legacyKind: 32010,
    content: `enc44:${JSON.stringify(wrap)}`
  })];
  for (const [index, channel] of channels.entries()) {
    const d = `notification:channel:${channel.id}`;
    const associatedData = { d, key_org: orgId, key_ref: `ock:${orgId}`,
      key_version: 'v1', legacy_kind: '32009', schema: CONFIDENTIAL_SCHEMA, t: 'notification-channel' };
    const nonce = new Uint8Array(24);
    nonce[0] = index + 1;
    const ciphertext = xchacha20poly1305(key, nonce, new TextEncoder().encode(JSON.stringify(associatedData)))
      .encrypt(new TextEncoder().encode(JSON.stringify({ ...channel, org_id: orgId })));
    events.push(confidentialCpStateFixture({
      d, topic: 'notification-channel', legacyKind: 32009,
      content: JSON.stringify({ schema: CONFIDENTIAL_SCHEMA, algorithm: CONFIDENTIAL_ALGORITHM,
        key_org: orgId, key_ref: `ock:${orgId}`, key_version: 'v1', nonce: base64Encode(nonce),
        ciphertext: base64Encode(ciphertext), associated_data: associatedData })
    }));
  }
  return events;
}

export function createEncryptedNotificationsSystemInfo({
  publicRelay = PUBLIC_RELAY,
  encryptedRelay = ENCRYPTED_RELAY,
  servicePubkey = SERVICE_PUBKEY,
  extraFeatures = {}
} = {}) {
  return {
    nostr: {
      browser_relays: [encryptedRelay || publicRelay],
      service_pubkey: servicePubkey
    },
    features: {
      relay_sidecar: true,
      relay_read_models: true,
      encrypted_nostr_requests: true,
      legacy_sse: false,
      ...extraFeatures
    }
  };
}

export async function installEncryptedNotificationHarness(
  page,
  {
    servicePubkey = SERVICE_PUBKEY,
    encryptedRelay = ENCRYPTED_RELAY,
    publicRelay = PUBLIC_RELAY,
    initialChannels = [],
    initialLogs = [],
    operationErrors = {}
  } = {}
) {
  await page.exposeFunction('__bahiaE2EChannelProjection', (channel) => notificationRelayFixtures([channel]).at(-1));
  await page.addInitScript(({ servicePubkey, encryptedRelay, publicRelay, initialChannels, initialLogs, operationErrors, operatorPubkey }) => {
    window.__BAHIA_E2E_ENCRYPTED_PUBLISHES = [];
    window.__BAHIA_E2E_ENCRYPTED_WIRE_PUBLISHES = [];
    window.__BAHIA_E2E_ENCRYPTED_OPERATIONS = [];
    window.__BAHIA_E2E_ENCRYPTED_REQUESTS = [];
    window.__BAHIA_E2E_ENCRYPTED_OKS = [];
    window.__BAHIA_E2E_ENCRYPTED_RESULTS = [];
    window.__BAHIA_E2E_ENCRYPTED_PENDING_RESULTS = [];
    window.__BAHIA_E2E_INTENT_STATUS_OVERRIDE = Object.fromEntries(
      Object.entries(operationErrors || {}).filter(([operation]) => operation.startsWith('notification.intent.'))
        .map(([operation, error]) => [operation.replace('.intent.', '.'), error.message || String(error)])
    );
    window.__BAHIA_E2E_NOTIFICATION_STATE = {
      nextId: 2,
      channels: (initialChannels || []).map((channel) => ({ ...channel })),
      logs: (initialLogs || []).map((log) => ({ ...log }))
    };

    const originalSignEvent = window.nostr?.signEvent?.bind(window.nostr);
    window.__BAHIA_E2E_ENCRYPTED_SIGNED_CONTEXTVM = [];
    window.nostr = {
      ...(window.nostr || {}),
      signEvent: async (event) => {
        const signed = originalSignEvent ? await originalSignEvent(event) : await window.__bahiaE2ESignMockEvent({ ...event, pubkey: operatorPubkey }, operatorPubkey);
        if (signed?.kind === 25910) {
          try {
            const parsed = parseContextVMRequest(signed);
            window.__BAHIA_E2E_ENCRYPTED_SIGNED_CONTEXTVM.push({ ...parsed, signedEvent: signed });
          } catch {}
        }
        return signed;
      },
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

    const KIND_CONTEXTVM = 25910;
    const KIND_GIFT_WRAP = 1059;

    function isRelayUrl(url, expected) {
      return String(url || '').replace(/\/$/, '') === String(expected || '').replace(/\/$/, '');
    }

    function parseContextVMRequest(event) {
      const plaintext = String(event.content || '').replace(/^(enc44:|mock-nip44:)/, '');
      const envelope = JSON.parse(plaintext || '{}');
      const payload = { ...(envelope.params || {}) };
      delete payload._meta;
      return { envelope, operation: envelope.method, payload };
    }

    function contextVMResultContent(envelope, result) {
      return `enc44:${JSON.stringify({ jsonrpc: '2.0', id: envelope.id, result })}`;
    }

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

    async function normalizeEncryptedEventForDelivery(event) {
      const normalized = {
        ...event,
        pubkey: typeof event?.pubkey === 'string' && /^[0-9a-f]{64}$/.test(event.pubkey) ? event.pubkey : servicePubkey,
        created_at: Number.isInteger(event?.created_at) ? event.created_at : Math.floor(Date.now() / 1000),
        tags: Array.isArray(event?.tags) ? event.tags.map((tag) => Array.isArray(tag) ? tag.map((value) => String(value)) : []).filter((tag) => tag.length > 0) : [],
        content: typeof event?.content === 'string' ? event.content : JSON.stringify(event?.content ?? {}),
      };
      // Signed as its author by the E2E test keyring (helpers.js).
      return window.__bahiaE2ESignMockEvent(normalized, servicePubkey);
    }

    function deliverEncryptedResult(candidate, subId, event) {
      void normalizeEncryptedEventForDelivery(event).then((normalized) => {
        if (candidate.readyState !== OriginalWebSocket.OPEN) return;
        candidate.onmessage?.({ data: JSON.stringify(['EVENT', subId, normalized]) });
      });
    }

    function deliverPendingEncryptedResults(socket = null) {
      const sockets = socket ? [socket] : Array.from(window.__BAHIA_E2E_ENCRYPTED_SOCKETS || []);
      window.__BAHIA_E2E_ENCRYPTED_PENDING_RESULTS = window.__BAHIA_E2E_ENCRYPTED_PENDING_RESULTS.filter((event) => {
        let delivered = false;
        for (const candidate of sockets) {
          if (!candidate || candidate.readyState !== OriginalWebSocket.OPEN) continue;
          const subs = candidate.__bahiaSubs || new Map();
          for (const [subId, filters] of subs.entries()) {
            if (Array.isArray(filters) && filters.some((filter) => matchesFilter(event, filter))) {
              deliverEncryptedResult(candidate, subId, event);
              delivered = true;
            }
          }
        }
        return !delivered;
      });
    }

    function queueEncryptedResult(event) {
      window.__BAHIA_E2E_ENCRYPTED_PENDING_RESULTS.push(event);
      deliverPendingEncryptedResults();
    }

    function canonicalNotificationOperation(operation) {
      return String(operation || '')
        .replace(/^notifications\/channels-/, 'notifications.channels.')
        .replace(/^notifications\/logs-/, 'notifications.logs.');
    }

    function notificationResult(operation, payload = {}) {
      const canonicalOperation = canonicalNotificationOperation(operation);
      const forcedError = operationErrors?.[canonicalOperation] || operationErrors?.[operation];
      if (forcedError) {
        return { status: 'error', error: { ...forcedError } };
      }

      const state = window.__BAHIA_E2E_NOTIFICATION_STATE;
      switch (canonicalOperation) {
        case 'notifications.channels.list':
          return { status: 'ok', payload: { channels: [...state.channels] } };
        case 'notifications.channels.get': {
          const channel = state.channels.find((candidate) => candidate.id === payload.id) || null;
          if (!channel) {
            return { status: 'error', error: { code: 'not_found', message: 'notification channel not found' } };
          }
          return { status: 'ok', payload: { channel: { ...channel } } };
        }
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
    window.__BAHIA_E2E_ENCRYPTED_SOCKETS = window.__BAHIA_E2E_ENCRYPTED_SOCKETS || new Set();

    OriginalWebSocket.prototype.send = function patchedSend(data) {
      window.__BAHIA_E2E_ENCRYPTED_SOCKETS.add(this);
      let message;
      try {
        message = JSON.parse(data);
      } catch {
        return originalSend.call(this, data);
      }

      if (Array.isArray(message) && message[0] === 'EVENT' && message[1]?.kind === KIND_GIFT_WRAP) {
        window.__BAHIA_E2E_ENCRYPTED_WIRE_PUBLISHES.push({ relay: this.url, eventId: message[1].id, kind: message[1].kind });
      }

      if (Array.isArray(message) && message[0] === 'REQ') {
        this.__bahiaSubs ??= new Map();
        this.__bahiaSubs.set(message[1], message.slice(2));
        const sent = originalSend.call(this, data);
        deliverPendingEncryptedResults(this);
        return sent;
      }

      if (Array.isArray(message) && message[0] === 'CLOSE') {
        this.__bahiaSubs?.delete(message[1]);
        return originalSend.call(this, data);
      }

      const signedIntent = message?.[0] === 'EVENT' && message[1]?.kind === KIND_GIFT_WRAP
        ? window.__BAHIA_E2E_SIGNED_INTENTS?.[0] : null;
      if (signedIntent?.tags?.some(tag => tag[0] === 'domain' && tag[1] === 'notification')) {
        const content = JSON.parse(signedIntent.content || '{}');
        const op = signedIntent.tags.find(tag => tag[0] === 'op')?.[1];
        const state = window.__BAHIA_E2E_NOTIFICATION_STATE;
        if (window.__BAHIA_E2E_INTENT_STATUS_OVERRIDE?.[`notification.${op}`]) {
          // Rejected intents do not mutate canonical channel state.
        } else if (op === 'delete') state.channels = state.channels.filter(channel => channel.id !== content.id);
        else {
          const current = state.channels.find(channel => channel.id === content.id);
          const channel = { ...current, ...content, id: content.id, updated_at: new Date().toISOString() };
          state.channels = [channel, ...state.channels.filter(candidate => candidate.id !== channel.id)];
          void window.__bahiaE2EChannelProjection(channel).then((event) => window.__bahiaPushNostrEvent(event));
        }
        window.__BAHIA_E2E_ENCRYPTED_OPERATIONS.push(`notification.intent.${op}`);
        return originalSend.call(this, data);
      }

      if (Array.isArray(message) && message[0] === 'EVENT' && [KIND_CONTEXTVM, KIND_GIFT_WRAP].includes(message[1]?.kind) && isRelayUrl(this.url, encryptedRelay)) {
        const event = message[1];
        const relay = this.url;
        const signedContext = event.kind === KIND_GIFT_WRAP ? window.__BAHIA_E2E_ENCRYPTED_SIGNED_CONTEXTVM.shift() : null;
        const { envelope, operation: rawOperation, payload, signedEvent } = signedContext || parseContextVMRequest(event);
        const operation = canonicalNotificationOperation(rawOperation);
        const requesterPubkey = event.kind === KIND_GIFT_WRAP ? (signedEvent?.pubkey || operatorPubkey) : event.pubkey;
        const result = notificationResult(operation, payload || {});
        window.__BAHIA_E2E_ENCRYPTED_PUBLISHES.push({ relay, eventId: event.id, kind: event.kind });
        window.__BAHIA_E2E_ENCRYPTED_REQUESTS.push({
          relay,
          eventId: event.id,
          kind: event.kind,
          tags: event.tags || [],
          operation,
          requesterPubkey,
          wrapperPubkey: event.kind === KIND_GIFT_WRAP ? event.pubkey : null,
          innerKind: signedEvent?.kind || event.kind,
          innerEventId: signedEvent?.id || event.id
        });
        window.__BAHIA_E2E_ENCRYPTED_OKS.push({ relay, eventId: event.id, kind: event.kind, sent: true, accepted: true, message: '' });
        window.__BAHIA_E2E_ENCRYPTED_OPERATIONS.push(operation);

        originalSend.call(this, data);

        const resultEvent = {
          id: `result-${event.id}`,
          kind: event.kind === KIND_GIFT_WRAP ? KIND_GIFT_WRAP : KIND_CONTEXTVM,
          pubkey: servicePubkey,
          created_at: Math.floor(Date.now() / 1000),
          tags: [
            ['e', event.id],
            ['p', requesterPubkey],
            ['encrypted', 'contextvm-jsonrpc-v1'],
            ['method', operation]
          ],
          content: contextVMResultContent(
            envelope,
            result.status === 'error'
              ? { status: 'error', error: result.error }
              : { status: 'ok', payload: result.payload }
          ),
          sig: '0'.repeat(128)
        };

        window.__BAHIA_E2E_ENCRYPTED_RESULTS.push({
          relay,
          eventId: resultEvent.id,
          kind: resultEvent.kind,
          requestEventId: event.id,
          requesterPubkey,
          wrapperPubkey: event.kind === KIND_GIFT_WRAP ? event.pubkey : null,
          pubkey: resultEvent.pubkey,
          status: result.status,
          error: result.error || null,
          operation,
          tags: resultEvent.tags || []
        });
        queueEncryptedResult(resultEvent);

        return;
      }

      return originalSend.call(this, data);
    };

    window.__BAHIA_E2E_ENCRYPTED_EXPECTED_RELAYS = { publicRelay, encryptedRelay };
  }, {
    servicePubkey,
    encryptedRelay,
    publicRelay,
    initialChannels,
    initialLogs,
    operationErrors,
    operatorPubkey: TEST_PUBKEY
  });
}
