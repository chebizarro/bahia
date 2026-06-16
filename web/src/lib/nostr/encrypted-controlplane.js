export {
  CONTEXTVM_EPHEMERAL_GIFT_WRAP_KIND,
  CONTEXTVM_GIFT_WRAP_KIND,
  CONTEXTVM_MESSAGE_KIND,
  ENCRYPTED_REQUEST_KIND,
  ENCRYPTED_REQUEST_ROUTING_TAG,
  ENCRYPTED_REQUEST_WIRE_VERSION,
  ENCRYPTED_RESULT_KIND
} from './encrypted-controlplane-constants.js';
export { EncryptedControlplaneTransport } from './encrypted-controlplane-transport.js';
export {
  encryptedRelayUrlsFromSystemInfo,
  encryptedRequestsAvailable,
  servicePubkeyFromSystemInfo
} from './encrypted-controlplane-utils.js';

import { authState } from '$lib/stores/auth.js';
import {
  CONTEXTVM_EPHEMERAL_GIFT_WRAP_KIND,
  CONTEXTVM_GIFT_WRAP_KIND,
  ENCRYPTED_RESULT_KIND
} from './encrypted-controlplane-constants.js';
import { EncryptedControlplaneTransport } from './encrypted-controlplane-transport.js';
import { isContextVMWrapperKind, parseContextVMResultPayload } from './encrypted-controlplane-result.js';
import {
  encryptedRelayUrlsFromSystemInfo,
  extractContextVMResult,
  hasTagValue,
  randomId,
  servicePubkeyFromSystemInfo,
  signalAbortError,
  throwIfSignalAborted
} from './encrypted-controlplane-utils.js';
import { ensureHexPubkey } from './nostr-hex.js';

// ---------------------------------------------------------------------------
// Shared encrypted controlplane — one persistent subscription for all results
// ---------------------------------------------------------------------------

let sharedTransport = null;
let sharedUnsubscribe = null;
let sharedRequesterPubkey = null;
const pendingRequests = new Map(); // requestEventId -> { resolve, reject, servicePubkey, contextVMRequestId, seen }

function ensureSharedTransport() {
  const relays = encryptedRelayUrlsFromSystemInfo();
  const servicePubkey = servicePubkeyFromSystemInfo();
  if (sharedTransport?.connected && relaysMatch(sharedTransport.relays, relays)) {
    return sharedTransport;
  }
  // Relays changed or not connected — rebuild
  teardownSharedSubscription();
  if (sharedTransport) {
    sharedTransport.disconnect();
  }
  sharedTransport = new EncryptedControlplaneTransport({ relays, servicePubkey });
  return sharedTransport;
}

function relaysMatch(a, b) {
  if (a.length !== b.length) return false;
  const setA = new Set(a);
  return b.every((r) => setA.has(r));
}

function teardownSharedSubscription() {
  if (sharedUnsubscribe) {
    sharedUnsubscribe();
    sharedUnsubscribe = null;
  }
  sharedRequesterPubkey = null;
}

function ensureSharedSubscription(transport) {
  const requesterPubkey = authState.pubkey;
  if (!requesterPubkey) throw new Error('Nostr authentication is required for ContextVM requests');

  // Already have a matching subscription
  if (sharedUnsubscribe && sharedRequesterPubkey === requesterPubkey) return;

  // Pubkey changed or no subscription yet — (re)create
  teardownSharedSubscription();
  sharedRequesterPubkey = requesterPubkey;

  const wrapperKinds = [CONTEXTVM_GIFT_WRAP_KIND, CONTEXTVM_EPHEMERAL_GIFT_WRAP_KIND];
  const filters = [{ kinds: wrapperKinds, '#p': [requesterPubkey] }];

  sharedUnsubscribe = transport.client.subscribe(filters, {
    onEvent: async (event) => {
      if (!hasTagValue(event, 'p', requesterPubkey)) return;
      // Find which pending request this result belongs to
      const eTags = (event.tags || []).filter((t) => t[0] === 'e').map((t) => t[1]);
      for (const eTag of eTags) {
        const entry = pendingRequests.get(eTag);
        if (!entry) continue;
        if (entry.seen.has(event.id)) return;
        entry.seen.add(event.id);
        if (!isContextVMWrapperKind(event.kind)) return;
        try {
          const payload = await parseContextVMResultPayload(event, entry.servicePubkey);
          const resultPayload = extractContextVMResult(payload, eTag, entry.contextVMRequestId);
          entry.resolve({ event, payload: resultPayload, jsonrpc: payload?.jsonrpc === '2.0' ? payload : null });
        } catch (error) {
          entry.reject(error);
        }
        return;
      }
    },
    onEose: () => {
      // Informational — we're caught up with stored events, keep listening
    },
    onClosed: (reason = '', relay) => {
      const reasonText = String(reason || '');
      if (reasonText.toLowerCase().includes('auth')) {
        // Auth failure — reject all pending and tear down
        const error = new Error(`ContextVM result subscription auth closure: ${relay ? `${relay}: ` : ''}${reasonText}`);
        rejectAllPending(error);
        teardownSharedSubscription();
      }
    }
  });
}

function rejectAllPending(error) {
  for (const [id, entry] of pendingRequests) {
    entry.reject(error);
  }
  pendingRequests.clear();
}

function waitForResult(requestEventId, { contextVMRequestId, servicePubkey, signal, timeoutMs }) {
  return new Promise((resolve, reject) => {
    let timer = null;
    let settled = false;

    const cleanup = () => {
      if (settled) return;
      settled = true;
      if (timer) clearTimeout(timer);
      signal?.removeEventListener?.('abort', onAbort);
      pendingRequests.delete(requestEventId);
    };

    const settle = (fn, value) => {
      if (settled) return;
      cleanup();
      fn(value);
    };

    const onAbort = () => settle(reject, signalAbortError(signal));

    if (signal?.aborted) {
      reject(signalAbortError(signal));
      return;
    }

    signal?.addEventListener?.('abort', onAbort, { once: true });

    if (Number.isFinite(timeoutMs) && timeoutMs > 0) {
      timer = setTimeout(() => {
        settle(reject, new Error(`ContextVM request timed out after ${timeoutMs}ms waiting for result`));
      }, timeoutMs);
    }

    pendingRequests.set(requestEventId, {
      resolve: (value) => settle(resolve, value),
      reject: (error) => settle(reject, error),
      servicePubkey,
      contextVMRequestId,
      seen: new Set()
    });
  });
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

export function disconnectEncryptedControlplane() {
  rejectAllPending(new Error('Encrypted controlplane disconnected'));
  teardownSharedSubscription();
  if (sharedTransport) {
    sharedTransport.disconnect();
    sharedTransport = null;
  }
}

export function createEncryptedControlplaneTransport(options = {}) {
  return new EncryptedControlplaneTransport(options);
}

export async function buildEncryptedRequestEvent(options) {
  const transport = ensureSharedTransport();
  await transport.connect();
  return transport.buildEncryptedRequestEvent(options);
}

export async function publishEncryptedRequest(options) {
  const transport = ensureSharedTransport();
  try {
    await transport.connect();
    const event = options?.event || await transport.buildEncryptedRequestEvent(options);
    return await transport.publishEncryptedRequest(event);
  } catch (error) {
    // On publish failure, don't tear down shared transport — other requests may be in flight
    throw error;
  }
}

export async function awaitEncryptedResult(options) {
  const transport = ensureSharedTransport();
  await transport.connect();
  ensureSharedSubscription(transport);

  const {
    requestEventId,
    contextVMRequestId = requestEventId,
    resultKinds = [ENCRYPTED_RESULT_KIND],
    signal,
    servicePubkey = transport.servicePubkey
  } = options || {};

  if (!requestEventId) throw new Error('requestEventId is required');
  ensureHexPubkey(servicePubkey, 'servicePubkey');

  return waitForResult(requestEventId, {
    contextVMRequestId,
    servicePubkey,
    signal,
    timeoutMs: options?.timeoutMs ?? 30000
  });
}

export async function requestEncryptedResult(options = {}) {
  const {
    resultKinds = [ENCRYPTED_RESULT_KIND],
    signal,
    timeoutMs = 30000,
    ...request
  } = options;

  throwIfSignalAborted(signal, 'ContextVM request aborted before publish');

  const transport = ensureSharedTransport();
  await transport.connect();
  ensureSharedSubscription(transport);

  throwIfSignalAborted(signal, 'ContextVM request aborted before publish');

  const contextVMRequestId = request.requestId || randomId();
  const event = await transport.buildEncryptedRequestEvent({ ...request, requestId: contextVMRequestId });

  throwIfSignalAborted(signal, 'ContextVM request aborted before publish');

  const servicePubkey = transport.servicePubkey;
  const resultPromise = waitForResult(event.id, {
    contextVMRequestId,
    servicePubkey,
    signal,
    timeoutMs
  });

  try {
    const publishResult = await transport.publishEncryptedRequest(event);
    const result = await resultPromise;
    return { ...publishResult, resultEvent: result.event, result: result.payload };
  } catch (error) {
    // Cancel the pending wait if publish failed
    if (pendingRequests.has(event.id)) {
      pendingRequests.delete(event.id);
    }
    if (signal?.aborted) throw signalAbortError(signal, 'ContextVM request aborted');
    throw error;
  }
}
