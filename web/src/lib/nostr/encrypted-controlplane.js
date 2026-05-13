import { authState, decryptWithAuth, encryptWithAuth, signWithAuth } from '$lib/stores/auth.js';
import { currentSystemInfo } from '$lib/stores/system.svelte.js';
import { NostrClient, getTagValues, nostr } from './client.js';

export const ENCRYPTED_REQUEST_ROUTING_TAG = 'encrypted';
export const ENCRYPTED_REQUEST_WIRE_VERSION = 'bahia-encrypted-v1';
export const ENCRYPTED_REQUEST_KIND = 5980;
export const ENCRYPTED_RESULT_KIND = 7980;

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
    throw new Error(`Encrypted Nostr result decrypted but did not contain valid JSON: ${error.message}`);
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

export function encryptedRelayUrlsFromSystemInfo(systemInfo = currentSystemInfo()) {
  // Prefer server-advertised relay list; fall back to the relays configured in Settings
  const serverRelays = normalizeRelays(systemInfo?.nostr?.browser_encrypted_request_relays || []);
  if (serverRelays.length > 0) return serverRelays;
  try {
    const configured = typeof nostr?.getRelays === 'function' ? nostr.getRelays() : [];
    return normalizeRelays(Array.isArray(configured) ? configured : []);
  } catch {
    return [];
  }
}

export function servicePubkeyFromSystemInfo(systemInfo = currentSystemInfo()) {
  return systemInfo?.nostr?.service_pubkey || '';
}

export function encryptedRequestsAvailable(systemInfo = currentSystemInfo()) {
  return encryptedRelayUrlsFromSystemInfo(systemInfo).length > 0 && Boolean(servicePubkeyFromSystemInfo(systemInfo));
}

export class EncryptedControlplaneTransport {
  constructor({ relays = encryptedRelayUrlsFromSystemInfo(), servicePubkey = servicePubkeyFromSystemInfo(), client = null } = {}) {
    this.relays = normalizeRelays(relays);
    this.servicePubkey = servicePubkey || '';

    if (client) {
      // Caller-provided client: use as-is, caller owns lifecycle.
      this.client = client;
      this.ownClient = false;
    } else {
      // Reuse the singleton when it already covers these relay URLs — avoids
      // opening duplicate connections and leaking reconnect timers.
      const singletonRelays = new Set(nostr.getRelays());
      const coveredBySingleton =
        this.relays.length > 0 && this.relays.every((r) => singletonRelays.has(r));
      if (coveredBySingleton) {
        this.client = nostr;
        this.ownClient = false;
      } else {
        this.client = new NostrClient({ relays: this.relays });
        this.ownClient = true;
      }
    }
    this.connected = false;
  }

  async connect() {
    if (this.relays.length === 0) {
      throw new Error('No relay URLs configured for encrypted Nostr events. Add relay URLs in Settings.');
    }
    if (!this.connected) {
      if (this.ownClient) {
        // Ephemeral client: establish its own connections.
        await this.client.connect(this.relays);
      }
      // Singleton: already managed externally; just mark ready.
      this.connected = true;
    }
    return this;
  }

  // Tear down ephemeral connections. No-op when reusing the singleton.
  disconnect() {
    if (this.ownClient) {
      this.client.disconnect();
      this.ownClient = false;
    }
    this.connected = false;
  }

  async buildEncryptedRequestEvent({ operation, payload = {}, tags = [], kind = ENCRYPTED_REQUEST_KIND, created_at = Math.floor(Date.now() / 1000) } = {}) {
    if (authState.status !== 'authenticated' || !authState.pubkey) {
      throw new Error('Nostr authentication is required for encrypted Nostr events');
    }
    if (typeof operation !== 'string' || !operation.trim()) {
      throw new Error('Encrypted Nostr event operation is required');
    }
    ensureHexPubkey(this.servicePubkey, 'servicePubkey');

    const envelope = {
      version: ENCRYPTED_REQUEST_WIRE_VERSION,
      operation: operation.trim(),
      requester_pubkey: authState.pubkey,
      payload
    };
    const ciphertext = await encryptWithAuth(this.servicePubkey, jsonContent(envelope));
    const mergedTags = [
      ...normalizeTags(tags),
      ['p', this.servicePubkey],
      [ENCRYPTED_REQUEST_ROUTING_TAG, ENCRYPTED_REQUEST_WIRE_VERSION]
    ];

    return signWithAuth({
      kind,
      created_at,
      tags: mergedTags,
      content: ciphertext
    });
  }

  async publishEncryptedRequest(event) {
    if (!event?.id) throw new Error('Cannot publish unsigned encrypted Nostr request event');
    await this.connect();
    const results = await this.client.publish(event);
    const acceptedRelays = results.filter(publishAccepted);
    const rejectedRelays = results.filter((result) => !publishAccepted(result));

    if (acceptedRelays.length === 0) {
      const reason = rejectedRelays.map((result) => result.message).filter(Boolean).join('; ') || 'no encrypted request relay accepted the request';
      throw new Error(`Encrypted Nostr request publish rejected: ${reason}`);
    }

    return {
      requestEventId: event.id,
      event,
      ok: results,
      acceptedRelays,
      rejectedRelays
    };
  }

  awaitEncryptedResult({ requestEventId, resultKinds = [ENCRYPTED_RESULT_KIND], signal, servicePubkey = this.servicePubkey } = {}) {
    if (!requestEventId) return Promise.reject(new Error('requestEventId is required'));
    if (!Array.isArray(resultKinds) || resultKinds.length === 0) return Promise.reject(new Error('resultKinds are required'));
    if (authState.status !== 'authenticated' || !authState.pubkey) return Promise.reject(new Error('Nostr authentication is required for encrypted Nostr events'));
    ensureHexPubkey(servicePubkey, 'servicePubkey');

    return new Promise((resolve, reject) => {
      let unsubscribe = null;
      let settled = false;
      const seen = new Set();
      const pendingRelays = openRelayUrls(this.client);
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

      const onAbort = () => settle(reject, signal?.reason instanceof Error ? signal.reason : new Error('Encrypted Nostr result wait aborted'));

      if (signal?.aborted) {
        onAbort();
        return;
      }

      if (pendingRelays && pendingRelays.length === 0) {
        settle(reject, new Error('No connected encrypted Nostr relays available for result subscription'));
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
              settle(reject, new Error(`Encrypted Nostr result subscription closed before result: ${reason || 'all relays closed'}`));
              return;
            }
          }
          if (reason && String(reason).toLowerCase().includes('auth')) {
            settle(reject, new Error(`Encrypted Nostr result subscription closed: ${reason}`));
          }
        }
      });
    });
  }

  async requestEncryptedResult({ resultKinds = [ENCRYPTED_RESULT_KIND], signal, ...request } = {}) {
    await this.connect();
    const event = await this.buildEncryptedRequestEvent(request);
    const abortController = typeof AbortController !== 'undefined' ? new AbortController() : null;
    const waitSignal = abortController?.signal || signal;
    const forwardAbort = () => abortController?.abort(signal?.reason);
    signal?.addEventListener?.('abort', forwardAbort, { once: true });

    const resultPromise = this.awaitEncryptedResult({ requestEventId: event.id, resultKinds, signal: waitSignal });
    try {
      const publishResult = await this.publishEncryptedRequest(event);
      const result = await resultPromise;
      return { ...publishResult, resultEvent: result.event, result: result.payload };
    } catch (error) {
      if (!signal?.aborted) {
        abortController?.abort(new Error(`Encrypted Nostr request failed before terminal result: ${error.message}`));
      }
      await resultPromise.catch(() => null);
      signal?.throwIfAborted?.();
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
