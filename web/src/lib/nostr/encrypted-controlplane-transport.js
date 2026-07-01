import { finalizeEvent, generateSecretKey, getPublicKey, nip44 } from 'nostr-tools';
import { authState, ensureEncryptedSignerReady, signWithAuth } from '$lib/stores/auth.js';
import { createNostrPoolClient } from './client.js';
import {
  CONTEXTVM_GIFT_WRAP_KIND,
  CONTEXTVM_MESSAGE_KIND,
  ENCRYPTED_REQUEST_KIND,
  ENCRYPTED_REQUEST_ROUTING_TAG,
  ENCRYPTED_REQUEST_WIRE_VERSION,
  ENCRYPTED_RESULT_KIND
} from './encrypted-controlplane-constants.js';
import { awaitEncryptedResultForTransport } from './encrypted-controlplane-result.js';
import {
  buildContextVMRequest,
  encryptedRelayUrlsFromSystemInfo,
  jsonContent,
  normalizeRelays,
  normalizeTags,
  publishAccepted,
  randomId,
  servicePubkeyFromSystemInfo,
  signalAbortError,
  throwIfSignalAborted
} from './encrypted-controlplane-utils.js';
import { ensureHexPubkey } from './nostr-hex.js';

export class EncryptedControlplaneTransport {
  constructor({ relays = encryptedRelayUrlsFromSystemInfo(), servicePubkey = servicePubkeyFromSystemInfo(), client = null } = {}) {
    this.relays = normalizeRelays(relays);
    this.servicePubkey = servicePubkey || '';
    this.client = client || createNostrPoolClient({ relays: this.relays });
    this.ownClient = !client;
    this.connected = false;
  }

  async connect() {
    if (this.relays.length === 0) {
      throw new Error('No Bahia relay URLs are available for ContextVM requests. Configure browser/bootstrap or ContextVM relays in Bahia discovery before publishing.');
    }
    // Delegate connection idempotency to the pool client: it short-circuits (and
    // skips the "[nostr] Connected to N/N relays" log) when the relay set is already
    // fully connected, and re-establishes any dropped relay otherwise. This avoids a
    // per-request reconnect without risking a stale connection being reused.
    if (this.ownClient) await this.client.connect(this.relays);
    this.connected = true;
    return this;
  }

  disconnect() {
    if (this.ownClient) this.client.disconnect();
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
    const mergedTags = [
      ...normalizeTags(tags),
      ['p', this.servicePubkey],
      [ENCRYPTED_REQUEST_ROUTING_TAG, ENCRYPTED_REQUEST_WIRE_VERSION],
      ['method', envelope.method]
    ];
    const innerEvent = await signWithAuth({ kind: CONTEXTVM_MESSAGE_KIND, created_at, tags: mergedTags, content: jsonContent(envelope) });

    if (kind !== CONTEXTVM_GIFT_WRAP_KIND) return innerEvent;

    const wrapperSecretKey = generateSecretKey();
    const wrapperPubkey = getPublicKey(wrapperSecretKey);
    const conversationKey = nip44.v2.utils.getConversationKey(wrapperSecretKey, this.servicePubkey);
    const ciphertext = nip44.v2.encrypt(jsonContent(innerEvent), conversationKey);
    return finalizeEvent({ kind, pubkey: wrapperPubkey, created_at, tags: [['p', this.servicePubkey]], content: ciphertext }, wrapperSecretKey);
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

    return { requestEventId: event.id, event, ok: results, acceptedRelays, rejectedRelays };
  }

  awaitEncryptedResult(options = {}) {
    return awaitEncryptedResultForTransport(this, options);
  }

  async requestEncryptedResult({ resultKinds = [ENCRYPTED_RESULT_KIND], signal, timeoutMs = 30000, ...request } = {}) {
    throwIfSignalAborted(signal, 'ContextVM request aborted before publish');
    const abortController = typeof AbortController !== 'undefined' ? new AbortController() : null;
    const waitSignal = abortController?.signal || signal;
    const forwardAbort = () => abortController?.abort(signal?.reason);
    const timeout = abortController && Number.isFinite(timeoutMs) && timeoutMs > 0
      ? setTimeout(() => abortController.abort(new Error(`ContextVM request timed out after ${timeoutMs}ms waiting for result`)), timeoutMs)
      : null;
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
        if (!signal?.aborted) abortController?.abort(new Error(`ContextVM request failed before terminal result: ${error.message}`));
        await resultPromise.catch(() => null);
      }
      if (signal?.aborted) throw signalAbortError(signal, 'ContextVM request aborted before publish');
      throw error;
    } finally {
      if (timeout) clearTimeout(timeout);
      signal?.removeEventListener?.('abort', forwardAbort);
      this.disconnect();
    }
  }
}
