import { authState, decryptWithAuth, encryptWithAuth, signWithAuth } from '$lib/stores/auth.js';
import { currentSystemInfo } from '$lib/stores/system.svelte.js';
import { NostrClient, getTagValues } from './client.js';

export const PRIVATE_TRANSPORT_VERSION = 'bahia-private-v1';
export const PRIVATE_REQUEST_KIND = 5980;
export const PRIVATE_RESULT_KIND = 7980;

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

function parseJson(value) {
  try {
    return JSON.parse(value);
  } catch (error) {
    throw new Error(`Private Nostr result decrypted but did not contain valid JSON: ${error.message}`);
  }
}

function hasTagValue(event, name, value) {
  return getTagValues(event, name).includes(value);
}

function openRelayUrls(client) {
  if (!(client?.sockets instanceof Map)) return null;
  const openState = typeof WebSocket !== 'undefined' ? WebSocket.OPEN : 1;
  return Array.from(client.sockets.entries())
    .filter(([, ws]) => ws?.readyState === openState)
    .map(([url]) => url);
}

export function privateRelayUrlsFromSystemInfo(systemInfo = currentSystemInfo()) {
  return normalizeRelays(systemInfo?.nostr?.private_browser_relays || []);
}

export function privateServicePubkeyFromSystemInfo(systemInfo = currentSystemInfo()) {
  return systemInfo?.nostr?.service_pubkey || '';
}

export function privateTransportAvailable(systemInfo = currentSystemInfo()) {
  return privateRelayUrlsFromSystemInfo(systemInfo).length > 0 && Boolean(privateServicePubkeyFromSystemInfo(systemInfo));
}

export class PrivateControlplaneTransport {
  constructor({ relays = privateRelayUrlsFromSystemInfo(), servicePubkey = privateServicePubkeyFromSystemInfo(), client = null } = {}) {
    this.relays = normalizeRelays(relays);
    this.servicePubkey = servicePubkey || '';
    this.client = client || new NostrClient({ relays: this.relays });
    this.connected = false;
  }

  async connect() {
    if (this.relays.length === 0) {
      throw new Error('No private browser relays are configured for Bahia private transport');
    }
    if (!this.connected) {
      await this.client.connect(this.relays);
      this.connected = true;
    }
    return this;
  }

  async buildPrivateRequestEvent({ operation, payload = {}, tags = [], kind = PRIVATE_REQUEST_KIND, created_at = Math.floor(Date.now() / 1000) } = {}) {
    if (authState.status !== 'authenticated' || !authState.pubkey) {
      throw new Error('Nostr authentication is required for private transport');
    }
    if (typeof operation !== 'string' || !operation.trim()) {
      throw new Error('Private transport operation is required');
    }
    ensureHexPubkey(this.servicePubkey, 'servicePubkey');

    const envelope = {
      version: PRIVATE_TRANSPORT_VERSION,
      operation: operation.trim(),
      requester_pubkey: authState.pubkey,
      payload
    };
    const ciphertext = await encryptWithAuth(this.servicePubkey, jsonContent(envelope));
    const mergedTags = [
      ...normalizeTags(tags),
      ['p', this.servicePubkey],
      ['private', PRIVATE_TRANSPORT_VERSION]
    ];

    return signWithAuth({
      kind,
      created_at,
      tags: mergedTags,
      content: ciphertext
    });
  }

  async publishPrivateRequest(event) {
    if (!event?.id) throw new Error('Cannot publish unsigned private request event');
    await this.connect();
    const results = await this.client.publish(event);
    const acceptedRelays = results.filter(publishAccepted);
    const rejectedRelays = results.filter((result) => !publishAccepted(result));

    if (acceptedRelays.length === 0) {
      const reason = rejectedRelays.map((result) => result.message).filter(Boolean).join('; ') || 'no private relay accepted the request';
      throw new Error(`Private Nostr request publish rejected: ${reason}`);
    }

    return {
      requestEventId: event.id,
      event,
      ok: results,
      acceptedRelays,
      rejectedRelays
    };
  }

  awaitPrivateResult({ requestEventId, resultKinds = [PRIVATE_RESULT_KIND], signal, timeoutMs = null, servicePubkey = this.servicePubkey } = {}) {
    if (!requestEventId) return Promise.reject(new Error('requestEventId is required'));
    if (!Array.isArray(resultKinds) || resultKinds.length === 0) return Promise.reject(new Error('resultKinds are required'));
    if (authState.status !== 'authenticated' || !authState.pubkey) return Promise.reject(new Error('Nostr authentication is required for private transport'));
    ensureHexPubkey(servicePubkey, 'servicePubkey');

    return new Promise((resolve, reject) => {
      let unsubscribe = null;
      let timer = null;
      let settled = false;
      const seen = new Set();
      const pendingRelays = openRelayUrls(this.client);
      const requesterPubkey = authState.pubkey;

      const cleanup = () => {
        if (timer) clearTimeout(timer);
        timer = null;
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

      const onAbort = () => settle(reject, signal?.reason instanceof Error ? signal.reason : new Error('Private Nostr result wait aborted'));

      if (signal?.aborted) {
        onAbort();
        return;
      }

      if (pendingRelays && pendingRelays.length === 0) {
        settle(reject, new Error('No connected private Nostr relays available for result subscription'));
        return;
      }

      signal?.addEventListener?.('abort', onAbort, { once: true });

      if (Number.isFinite(timeoutMs) && timeoutMs > 0) {
        timer = setTimeout(() => {
          settle(reject, new Error(`Timed out waiting for private Nostr result for ${requestEventId}`));
        }, timeoutMs);
      }

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
            if (payload?.request_event_id && payload.request_event_id !== requestEventId) return;
            settle(resolve, { event, payload });
          } catch (error) {
            settle(reject, error);
          }
        },
        onClosed: (reason, relay) => {
          if (pendingRelays && relay) {
            const index = pendingRelays.indexOf(relay);
            if (index >= 0) pendingRelays.splice(index, 1);
            if (pendingRelays.length === 0) {
              settle(reject, new Error(`Private Nostr result subscription closed before result: ${reason || 'all relays closed'}`));
              return;
            }
          }
          if (reason && String(reason).toLowerCase().includes('auth')) {
            settle(reject, new Error(`Private Nostr result subscription closed: ${reason}`));
          }
        }
      });
    });
  }

  async requestPrivateResult({ resultKinds = [PRIVATE_RESULT_KIND], signal, timeoutMs = null, ...request } = {}) {
    await this.connect();
    const event = await this.buildPrivateRequestEvent(request);
    const abortController = typeof AbortController !== 'undefined' ? new AbortController() : null;
    const waitSignal = abortController?.signal || signal;
    const forwardAbort = () => abortController?.abort(signal?.reason);
    signal?.addEventListener?.('abort', forwardAbort, { once: true });

    const resultPromise = this.awaitPrivateResult({ requestEventId: event.id, resultKinds, signal: waitSignal, timeoutMs });
    try {
      const publishResult = await this.publishPrivateRequest(event);
      const result = await resultPromise;
      return { ...publishResult, resultEvent: result.event, result: result.payload };
    } catch (error) {
      if (!signal?.aborted) {
        abortController?.abort(new Error(`Private Nostr request failed before terminal result: ${error.message}`));
      }
      await resultPromise.catch(() => null);
      signal?.throwIfAborted?.();
      throw error;
    } finally {
      signal?.removeEventListener?.('abort', forwardAbort);
    }
  }
}

export function createPrivateControlplaneTransport(options = {}) {
  return new PrivateControlplaneTransport(options);
}

export async function buildPrivateRequestEvent(options) {
  const transport = createPrivateControlplaneTransport(options?.transport);
  return transport.buildPrivateRequestEvent(options);
}

export async function publishPrivateRequest(options) {
  const transport = createPrivateControlplaneTransport(options?.transport);
  const event = options?.event || await transport.buildPrivateRequestEvent(options);
  return transport.publishPrivateRequest(event);
}

export async function awaitPrivateResult(options) {
  const transport = createPrivateControlplaneTransport(options?.transport);
  await transport.connect();
  return transport.awaitPrivateResult(options);
}

export async function requestPrivateResult(options) {
  const transport = createPrivateControlplaneTransport(options?.transport);
  return transport.requestPrivateResult(options);
}
