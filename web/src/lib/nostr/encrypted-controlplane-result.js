import { authState, decryptWithAuth } from '$lib/stores/auth.js';
import { ENCRYPTED_RESULT_KIND } from './encrypted-controlplane-constants.js';
import {
  extractContextVMResult,
  formatClosedRelays,
  hasTagValue,
  openRelayUrls,
  parseJson,
  signalAbortError
} from './encrypted-controlplane-utils.js';
import { ensureHexPubkey } from './nostr-hex.js';

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
    const filter = { kinds: resultKinds, '#e': [requestEventId], '#p': [requesterPubkey], authors: [servicePubkey] };
    unsubscribe = transport.client.subscribe([filter], {
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
