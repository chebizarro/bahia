import { authState, decryptWithAuth, encryptWithAuth, ensureEncryptedSignerReady, signWithAuth } from '$lib/stores/auth.js';
import { currentSystemInfo } from '$lib/stores/system.svelte.js';
import { createNostrPoolClient, getTagValues, nostr } from './client.js';

export const ENCRYPTED_REQUEST_ROUTING_TAG = 'encrypted';
export const ENCRYPTED_REQUEST_WIRE_VERSION = 'contextvm-jsonrpc-v1';
export const CONTEXTVM_MESSAGE_KIND = 25910;
export const CONTEXTVM_GIFT_WRAP_KIND = 1059;
export const CONTEXTVM_EPHEMERAL_GIFT_WRAP_KIND = 21059;
export const ENCRYPTED_REQUEST_KIND = CONTEXTVM_MESSAGE_KIND;
export const ENCRYPTED_RESULT_KIND = CONTEXTVM_MESSAGE_KIND;

function ensureHexPubkey(pubkey, field) {
  if (typeof pubkey !== 'string' || !/^[0-9a-fA-F]{64}$/.test(pubkey)) {
    throw new Error(`${field} must be a 64-character hex pubkey`);
  }
}

function normalizeTags(tags) {
  if (!Array.isArray(tags)) return [];
  return tags
    .filter((tag) => Array.isArray(tag) && typeof tag[0] === 'string' && tag[0])
    .map((tag) => tag.map((value) => String(value)));
}

function normalizeRelays(relays) {
  if (!Array.isArray(relays)) return [];
  const seen = new Set();
  const out = [];
  for (const relay of relays) {
    const url = typeof relay === 'string' ? relay.trim() : '';
    if (!/^wss?:\/\//i.test(url) || seen.has(url)) continue;
    seen.add(url);
    out.push(url);
  }
  return out;
}

function publishAccepted(result) {
  return result?.sent === true && result?.accepted === true;
}

function jsonContent(value) {
  if (typeof value === 'string') return value;
  return JSON.stringify(value ?? {});
}

function contextVMMethod(operation) {
  const trimmed = String(operation || '').trim();
  if (!trimmed) return '';
  if (trimmed.includes('/')) return trimmed;
  const parts = trimmed.split('.').map((part) => part.trim()).filter(Boolean);
  if (parts.length >= 3 && parts[parts.length - 1] === 'request') {
    return `${parts[0]}/${parts.slice(1, -1).join('-')}`;
  }
  if (parts.length >= 2) {
    return `${parts[0]}/${parts.slice(1).join('-')}`;
  }
  return trimmed;
}

function buildContextVMRequest({ operation, payload, requestId }) {
  const method = contextVMMethod(operation);
  const params = payload && typeof payload === 'object' && !Array.isArray(payload) ? { ...payload } : { value: payload };
  params._meta = {
    ...(params._meta && typeof params._meta === 'object' ? params._meta : {}),
    progressToken: requestId
  };
  return {
    jsonrpc: '2.0',
    id: requestId,
    method,
    params
  };
}

function extractContextVMResult(payload, requestEventId, contextVMRequestId = requestEventId) {
  if (payload?.jsonrpc === '2.0') {
    if (payload.id !== contextVMRequestId) {
      throw new Error('ContextVM encrypted result payload did not correlate to the ContextVM request id');
    }
    if (payload.error) {
      const message = payload.error.message || 'ContextVM encrypted request failed';
      throw new Error(message);
    }
    return payload.result ?? {};
  }
  if (payload?.request_event_id !== requestEventId) {
    throw new Error('ContextVM result payload did not correlate to the request event id');
  }
  return payload;
}

function parseJson(value) {
  try {
    return JSON.parse(value);
  } catch (error) {
    throw new Error(`ContextVM result decrypted but did not contain valid JSON: ${error.message}`);
  }
}

function randomId() {
  const cryptoApi = globalThis.crypto;
  if (cryptoApi?.randomUUID) return cryptoApi.randomUUID();
  if (cryptoApi?.getRandomValues) {
    const bytes = new Uint8Array(16);
    cryptoApi.getRandomValues(bytes);
    return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
  }
  return `req-${Date.now()}`;
}

function hasTagValue(event, name, value) {
  return getTagValues(event, name).includes(value);
}

function openRelayUrls(client) {
  if (typeof client?.getConnectedRelays !== 'function') return null;
  return client.getConnectedRelays();
}

function formatClosedRelays(closedRelays) {
  return Array.from(closedRelays.entries())
    .map(([relay, reason]) => `${relay}${reason ? ` (${reason})` : ''}`)
    .join('; ');
}

function signalAbortError(signal, fallback = 'ContextVM result wait aborted') {
  return signal?.reason instanceof Error ? signal.reason : new Error(fallback);
}

function throwIfSignalAborted(signal, fallback) {
  if (signal?.aborted) throw signalAbortError(signal, fallback);
}

export function encryptedRelayUrlsFromSystemInfo(systemInfo = currentSystemInfo()) {
  return normalizeRelays(systemInfo?.nostr?.contextvm_relays || systemInfo?.nostr?.browser_relays);
}

export function servicePubkeyFromSystemInfo(systemInfo = currentSystemInfo()) {
  return systemInfo?.nostr?.service_pubkey || '';
}

export function encryptedRequestsAvailable(systemInfo = currentSystemInfo()) {
  return systemInfo?.features?.encrypted_nostr_requests === true
    && encryptedRelayUrlsFromSystemInfo(systemInfo).length > 0
    && Boolean(servicePubkeyFromSystemInfo(systemInfo));
}

export class EncryptedControlplaneTransport {
  constructor({ relays = encryptedRelayUrlsFromSystemInfo(), servicePubkey = servicePubkeyFromSystemInfo(), client = null } = {}) {
    this.relays = normalizeRelays(relays);
    this.servicePubkey = servicePubkey || '';

    // Use an isolated client by default so request/result flows do not interfere
    // with the general dashboard subscription lifecycle.
    this.client = client || createNostrPoolClient({ relays: this.relays });
    this.ownClient = !client;
    this.connected = false;
  }

  async connect() {
    if (this.relays.length === 0) {
      throw new Error('No Bahia relay URLs are available for ContextVM requests. Configure browser/bootstrap or ContextVM relays in Bahia discovery before publishing.');
    }
    if (this.ownClient) {
      await this.client.connect(this.relays, { force: true });
    }
    this.connected = true;
    return this;
  }

  disconnect() {
    if (this.ownClient) {
      this.client.disconnect();
    }
    this.connected = false;
  }

  async buildEncryptedRequestEvent({ operation, payload = {}, tags = [], kind = ENCRYPTED_REQUEST_KIND, created_at = Math.floor(Date.now() / 1000), requestId = randomId() } = {}) {
    if (authState.status !== 'authenticated' || !authState.pubkey) {
      throw new Error('Nostr authentication is required for ContextVM requests');
    }
    if (typeof operation !== 'string' || !operation.trim()) {
      throw new Error('ContextVM request operation is required');
    }
    ensureHexPubkey(this.servicePubkey, 'servicePubkey');
    await ensureEncryptedSignerReady(this.servicePubkey);

    const envelope = buildContextVMRequest({ operation, payload, requestId });
    const ciphertext = await encryptWithAuth(this.servicePubkey, jsonContent(envelope));
    const mergedTags = [
      ...normalizeTags(tags),
      ['p', this.servicePubkey],
      [ENCRYPTED_REQUEST_ROUTING_TAG, ENCRYPTED_REQUEST_WIRE_VERSION],
      ['method', envelope.method]
    ];

    return signWithAuth({
      kind,
      created_at,
      tags: mergedTags,
      content: ciphertext
    });
  }

  async publishEncryptedRequest(event) {
    if (!event?.id) throw new Error('Cannot publish unsigned ContextVM request event');
    await this.connect();
    const results = await this.client.publish(event);
    const acceptedRelays = results.filter(publishAccepted);
    const rejectedRelays = results.filter((result) => !publishAccepted(result));

    if (acceptedRelays.length === 0) {
      const reason = rejectedRelays.map((result) => result.message).filter(Boolean).join('; ') || 'no Bahia relay accepted the ContextVM request';
      throw new Error(`ContextVM request publish rejected: ${reason}`);
    }

    return {
      requestEventId: event.id,
      event,
      ok: results,
      acceptedRelays,
      rejectedRelays
    };
  }

  awaitEncryptedResult({ requestEventId, contextVMRequestId = requestEventId, resultKinds = [ENCRYPTED_RESULT_KIND], signal, servicePubkey = this.servicePubkey } = {}) {
    if (!requestEventId) return Promise.reject(new Error('requestEventId is required'));
    if (!Array.isArray(resultKinds) || resultKinds.length === 0) return Promise.reject(new Error('resultKinds are required'));
    if (authState.status !== 'authenticated' || !authState.pubkey) return Promise.reject(new Error('Nostr authentication is required for ContextVM requests'));
    ensureHexPubkey(servicePubkey, 'servicePubkey');

    return new Promise((resolve, reject) => {
      let unsubscribe = null;
      let settled = false;
      const seen = new Set();
      const pendingRelays = openRelayUrls(this.client);
      const closedRelays = new Map();
      const eoseRelays = new Set();
      const requesterPubkey = authState.pubkey;

      const cleanup = () => {
        if (unsubscribe) unsubscribe();
        unsubscribe = null;
        signal?.removeEventListener?.('abort', onAbort);
      };

      const settle = (fn, value) => {
        if (settled) return;
        settled = true;
        cleanup();
        fn(value);
      };

      const onAbort = () => settle(reject, signalAbortError(signal));

      if (signal?.aborted) {
        onAbort();
        return;
      }

      if (pendingRelays && pendingRelays.length === 0) {
        settle(reject, new Error('No connected Bahia relays are available for ContextVM result subscription'));
        return;
      }

      signal?.addEventListener?.('abort', onAbort, { once: true });

      const filter = { kinds: resultKinds, '#e': [requestEventId], '#p': [requesterPubkey], authors: [servicePubkey] };
      unsubscribe = this.client.subscribe([filter], {
        onEvent: async (event) => {
          if (event?.pubkey !== servicePubkey) return;
          if (!hasTagValue(event, 'e', requestEventId) || !hasTagValue(event, 'p', requesterPubkey)) return;
          if (seen.has(event.id)) return;
          seen.add(event.id);

          try {
            const plaintext = await decryptWithAuth(servicePubkey, event.content || '');
            const payload = parseJson(plaintext);
            const resultPayload = extractContextVMResult(payload, requestEventId, contextVMRequestId);
            settle(resolve, { event, payload: resultPayload, jsonrpc: payload?.jsonrpc === '2.0' ? payload : null });
          } catch (error) {
            settle(reject, error);
          }
        },
        onEose: (relay) => {
          if (relay) eoseRelays.add(relay);
        },
        onClosed: (reason = '', relay) => {
          const reasonText = String(reason || '');
          if (relay) closedRelays.set(relay, eoseRelays.has(relay) && reasonText ? `after EOSE: ${reasonText}` : reasonText);
          if (reasonText.toLowerCase().includes('auth')) {
            const relayLabel = relay ? `${relay}: ` : '';
            settle(reject, new Error(`ContextVM result subscription auth closure: ${relayLabel}${reasonText}`));
            return;
          }
          if (!pendingRelays || !relay) {
            settle(reject, new Error(`ContextVM result subscription closed before result: ${reasonText || 'relay closed without a reason'}`));
            return;
          }
          const index = pendingRelays.indexOf(relay);
          if (index >= 0) pendingRelays.splice(index, 1);
          if (pendingRelays.length === 0) {
            const summary = formatClosedRelays(closedRelays) || reasonText || 'all relays closed';
            settle(reject, new Error(`ContextVM result subscription closed before result from all Bahia relays: ${summary}`));
          }
        }
      });
    });
  }

  async requestEncryptedResult({ resultKinds = [ENCRYPTED_RESULT_KIND], signal, ...request } = {}) {
    throwIfSignalAborted(signal, 'ContextVM request aborted before publish');
    const abortController = typeof AbortController !== 'undefined' ? new AbortController() : null;
    const waitSignal = abortController?.signal || signal;
    const forwardAbort = () => abortController?.abort(signal?.reason);
    let resultPromise = null;

    try {
      await this.connect();
      throwIfSignalAborted(signal, 'ContextVM request aborted before publish');
      const contextVMRequestId = request.requestId || randomId();
      const event = await this.buildEncryptedRequestEvent({ ...request, requestId: contextVMRequestId });
      throwIfSignalAborted(signal, 'ContextVM request aborted before publish');
      signal?.addEventListener?.('abort', forwardAbort, { once: true });
      if (signal?.aborted) forwardAbort();

      resultPromise = this.awaitEncryptedResult({ requestEventId: event.id, contextVMRequestId, resultKinds, signal: waitSignal });
      const publishResult = await this.publishEncryptedRequest(event);
      const result = await resultPromise;
      return { ...publishResult, resultEvent: result.event, result: result.payload };
    } catch (error) {
      if (resultPromise) {
        if (!signal?.aborted) {
          abortController?.abort(new Error(`ContextVM request failed before terminal result: ${error.message}`));
        }
        await resultPromise.catch(() => null);
      }
      if (signal?.aborted) throw signalAbortError(signal, 'ContextVM request aborted before publish');
      throw error;
    } finally {
      signal?.removeEventListener?.('abort', forwardAbort);
      // Tear down ephemeral connections now that the request is settled.
      this.disconnect();
    }
  }
}

export function createEncryptedControlplaneTransport(options = {}) {
  return new EncryptedControlplaneTransport(options);
}

export async function buildEncryptedRequestEvent(options) {
  const transport = createEncryptedControlplaneTransport(options?.transport);
  return transport.buildEncryptedRequestEvent(options);
}

export async function publishEncryptedRequest(options) {
  const transport = createEncryptedControlplaneTransport(options?.transport);
  try {
    const event = options?.event || await transport.buildEncryptedRequestEvent(options);
    return await transport.publishEncryptedRequest(event);
  } finally {
    transport.disconnect();
  }
}

export async function awaitEncryptedResult(options) {
  const transport = createEncryptedControlplaneTransport(options?.transport);
  try {
    await transport.connect();
    return await transport.awaitEncryptedResult(options);
  } finally {
    transport.disconnect();
  }
}

export async function requestEncryptedResult(options) {
  const transport = createEncryptedControlplaneTransport(options?.transport);
  // transport.requestEncryptedResult already calls this.disconnect() in its finally
  // block, but we guard here too in case it throws before reaching that path.
  try {
    return await transport.requestEncryptedResult(options);
  } finally {
    transport.disconnect();
  }
}
