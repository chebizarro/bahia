import { finalizeEvent, generateSecretKey, getPublicKey, nip44 } from 'nostr-tools';
import { authState, ensureEncryptedSignerReady, signWithAuth } from '$lib/stores/auth.js';
import { nostr } from './subscriptions.js';
import {
  CONTEXTVM_EPHEMERAL_GIFT_WRAP_KIND,
  CONTEXTVM_GIFT_WRAP_KIND,
  CONTEXTVM_MESSAGE_KIND,
  ENCRYPTED_REQUEST_KIND,
  ENCRYPTED_REQUEST_ROUTING_TAG,
  ENCRYPTED_REQUEST_WIRE_VERSION,
  ENCRYPTED_RESULT_KIND,
  NIP44_MAX_PLAINTEXT_BYTES,
} from './encrypted-controlplane-constants.js';
import { awaitEncryptedResultForTransport } from './encrypted-controlplane-result.js';
import {
  assertConnectedBahiaRelays,
  assertRelayMessageFits,
  buildContextVMRequest,
  encryptedRelayUrlsFromSystemInfo,
  jsonContent,
  normalizeRelays,
  normalizeTags,
  publishAccepted,
  servicePubkeyFromSystemInfo,
  signalAbortError,
  throwIfSignalAborted
} from './encrypted-controlplane-utils.js';
import { ensureHexPubkey } from './nostr-hex.js';
import { mintEntityId } from '../entity-id.js';
import { relayLimits } from './relay-nip11.js';

export class EncryptedControlplaneTransport {
  constructor({ relays = encryptedRelayUrlsFromSystemInfo(), servicePubkey = servicePubkeyFromSystemInfo(), client = null } = {}) {
    this.relays = normalizeRelays(relays);
    this.servicePubkey = servicePubkey || '';
    this.client = client || {
      connect: (relays) => nostr.connect(relays),
      getConnectedRelays: () => nostr.getConnectedRelays(this.relays),
      subscribe: (filters, handlers) => nostr.subscribeOnRelays(this.relays, filters, handlers),
      publish: (event) => nostr.publish(event, { relays: this.relays })
    };
    this.connected = false;
    this.connectedRelays = [];
  }

  async connect() {
    if (this.relays.length === 0) {
      throw new Error('No Bahia relay URLs are available for ContextVM requests. Configure browser/bootstrap or ContextVM relays in Bahia discovery before publishing.');
    }
    // Delegate connection idempotency to the pool client: it short-circuits (and
    // skips the "[nostr] Connected to N/N relays" log) when the relay set is already
    // fully connected, and re-establishes any dropped relay otherwise. This avoids a
    // per-request reconnect without risking a stale connection being reused.
    if (typeof this.client.connect === 'function') {
      const summary = await this.client.connect(this.relays);
      if (summary?.connected === 0) {
        this.connected = false;
        throw new Error('ContextVM encrypted requests are not available. No Bahia relay is connected for encrypted control-plane traffic.');
      }
    }
    assertConnectedBahiaRelays(this.client);
    this.connectedRelays = this.client.getConnectedRelays?.() || this.relays;
    void relayLimits.resolve(this.connectedRelays);
    this.connected = true;
    return this;
  }

  disconnect() {
    // The shared pool stays alive for other subscriptions.
    this.connected = false;
  }

  async buildEncryptedRequestEvent({ operation, payload = {}, tags = [], kind = ENCRYPTED_REQUEST_KIND, created_at = Math.floor(Date.now() / 1000), requestId = mintEntityId() } = {}) {
    if (authState.status !== 'authenticated' || !authState.pubkey) {
      throw new Error('Nostr authentication is required for ContextVM requests');
    }
    if (typeof operation !== 'string' || !operation.trim()) {
      throw new Error('ContextVM request operation is required');
    }
    ensureHexPubkey(this.servicePubkey, 'servicePubkey');
    if (kind === CONTEXTVM_GIFT_WRAP_KIND) {
      await ensureEncryptedSignerReady(this.servicePubkey);
    }

    const envelope = buildContextVMRequest({ operation, payload, requestId });
    const mergedTags = [
      ...normalizeTags(tags),
      ['p', this.servicePubkey],
      [ENCRYPTED_REQUEST_ROUTING_TAG, ENCRYPTED_REQUEST_WIRE_VERSION],
      ['method', envelope.method]
    ];
    const innerEvent = await signWithAuth({ kind: CONTEXTVM_MESSAGE_KIND, created_at, tags: mergedTags, content: jsonContent(envelope) });

    if (kind !== CONTEXTVM_GIFT_WRAP_KIND) return innerEvent;

    const plaintext = jsonContent(innerEvent);
    const plaintextBytes = new TextEncoder().encode(plaintext).length;
    if (plaintextBytes > NIP44_MAX_PLAINTEXT_BYTES) {
      throw new Error(`ContextVM request is ${plaintextBytes} bytes, over the ${NIP44_MAX_PLAINTEXT_BYTES}-byte NIP-44 encryption limit; send large documents by reference (upload them to Blossom and pass the location).`);
    }
    const wrapperSecretKey = generateSecretKey();
    const wrapperPubkey = getPublicKey(wrapperSecretKey);
    const conversationKey = nip44.v2.utils.getConversationKey(wrapperSecretKey, this.servicePubkey);
    const ciphertext = nip44.v2.encrypt(plaintext, conversationKey);
    // Bahia's relay stores at most STORED_EVENT_MAX_CONTENT_BYTES of content,
    // so a wrap too large to store is sent as an ephemeral 21059: relayed live
    // to the daemon (which answers in kind), never stored.
    const wrapKind = ciphertext.length > relayLimits.cached(this.connectedRelays).maxContentBytes ? CONTEXTVM_EPHEMERAL_GIFT_WRAP_KIND : kind;
    return finalizeEvent({ kind: wrapKind, pubkey: wrapperPubkey, created_at, tags: [['p', this.servicePubkey]], content: ciphertext }, wrapperSecretKey);
  }

  async publishEncryptedRequest(event) {
    if (!event?.id) throw new Error('Cannot publish unsigned ContextVM request event');
    await this.connect();
    assertRelayMessageFits(event, relayLimits.cached(this.connectedRelays).maxMessageBytes);
    assertConnectedBahiaRelays(this.client);
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
      const contextVMRequestId = request.requestId || mintEntityId();
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
