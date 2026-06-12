import { authState, decryptWithAuth } from '$lib/stores/auth.js';
import {
  CONTEXTVM_EPHEMERAL_GIFT_WRAP_KIND,
  CONTEXTVM_GIFT_WRAP_KIND,
  CONTEXTVM_MESSAGE_KIND,
  ENCRYPTED_RESULT_KIND
} from './encrypted-controlplane-constants.js';
import {
  extractContextVMResult,
  formatClosedRelays,
  hasTagValue,
  openRelayUrls,
  parseJson,
  signalAbortError
} from './encrypted-controlplane-utils.js';
import { ensureHexPubkey } from './nostr-hex.js';

function isContextVMWrapperKind(kind) {
  return kind === CONTEXTVM_GIFT_WRAP_KIND || kind === CONTEXTVM_EPHEMERAL_GIFT_WRAP_KIND;
}

async function parseContextVMResultPayload(event, servicePubkey) {
  if (isContextVMWrapperKind(event.kind)) {
    const payload = parseJson(await decryptWithAuth(event.pubkey, event.content || ''));
    if (payload?.kind === CONTEXTVM_MESSAGE_KIND && typeof payload.content === 'string') {
      return parseJson(payload.content);
    }
    return payload;
  }

  try {
    return parseJson(event.content || '');
  } catch (plaintextError) {
    try {
      return parseJson(await decryptWithAuth(servicePubkey, event.content || ''));
    } catch {
      throw plaintextError;
    }
  }
}

export function awaitEncryptedResultForTransport(transport, {
  requestEventId,
  contextVMRequestId = requestEventId,
  resultKinds = [ENCRYPTED_RESULT_KIND],
  signal,
  servicePubkey = transport.servicePubkey
} = {}) {
  if (!requestEventId) return Promise.reject(new Error('requestEventId is required'));
  if (!Array.isArray(resultKinds) || resultKinds.length === 0) return Promise.reject(new Error('resultKinds are required'));
  if (authState.status !== 'authenticated' || !authState.pubkey) {
    return Promise.reject(new Error('Nostr authentication is required for ContextVM requests'));
  }
  ensureHexPubkey(servicePubkey, 'servicePubkey');

  return new Promise((resolve, reject) => {
    let unsubscribe = null;
    let settled = false;
    const seen = new Set();
    const pendingRelays = openRelayUrls(transport.client);
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
    const wrapperKinds = resultKinds.filter(isContextVMWrapperKind);
    const messageKinds = resultKinds.filter((kind) => !isContextVMWrapperKind(kind));
    const filters = [];
    if (wrapperKinds.length > 0) filters.push({ kinds: wrapperKinds, '#e': [requestEventId], '#p': [requesterPubkey] });
    if (messageKinds.length > 0) filters.push({ kinds: messageKinds, '#e': [requestEventId], '#p': [requesterPubkey], authors: [servicePubkey] });
    if (filters.length === 0) filters.push({ kinds: [CONTEXTVM_MESSAGE_KIND], '#e': [requestEventId], '#p': [requesterPubkey], authors: [servicePubkey] });

    unsubscribe = transport.client.subscribe(filters, {
      onEvent: async (event) => {
        if (!hasTagValue(event, 'e', requestEventId) || !hasTagValue(event, 'p', requesterPubkey)) return;
        if (!isContextVMWrapperKind(event.kind) && event?.pubkey !== servicePubkey) return;
        if (seen.has(event.id)) return;
        seen.add(event.id);

        try {
          const payload = await parseContextVMResultPayload(event, servicePubkey);
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
