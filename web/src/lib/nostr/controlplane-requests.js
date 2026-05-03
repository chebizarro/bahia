import { authState, signWithAuth } from '$lib/stores/auth.js';
import { currentSystemInfo } from '$lib/stores/system.svelte.js';
import { getTagValues, nostr, parseJsonContent } from './client.js';

function ensureKind(kind) {
  if (!Number.isInteger(kind) || kind <= 0) {
    throw new Error('Nostr request kind must be a positive integer');
  }
}

function normalizeTags(tags) {
  if (!Array.isArray(tags)) return [];
  return tags
    .filter((tag) => Array.isArray(tag) && typeof tag[0] === 'string' && tag[0].length > 0)
    .map((tag) => tag.map((value) => String(value)));
}

function normalizeContent(content) {
  if (content === null || content === undefined) return '';
  if (typeof content === 'string') return content;
  return JSON.stringify(content);
}

function publishAccepted(result) {
  return result?.sent === true && result?.accepted === true;
}

function controlplaneServicePubkey(servicePubkey) {
  if (servicePubkey !== undefined) return servicePubkey || '';
  return currentSystemInfo()?.nostr?.service_pubkey || '';
}

function eventMatchesAuthor(event, servicePubkey) {
  const expected = controlplaneServicePubkey(servicePubkey);
  return !expected || event?.pubkey === expected;
}

function requestFilter(kinds, requestEventId, servicePubkey) {
  const filter = { kinds, '#e': [requestEventId] };
  const author = controlplaneServicePubkey(servicePubkey);
  if (author) filter.authors = [author];
  return filter;
}

function openRelayUrls() {
  if (!(nostr?.sockets instanceof Map)) return null;
  const openState = typeof WebSocket !== 'undefined' ? WebSocket.OPEN : 1;
  return Array.from(nostr.sockets.entries())
    .filter(([, ws]) => ws?.readyState === openState)
    .map(([url]) => url);
}

function requestCorrelationValues(event) {
  const values = new Set(getTagValues(event, 'e'));
  const content = parseJsonContent(event, null);
  if (content && typeof content === 'object') {
    for (const key of ['request_event_id', 'requestEventId', 'request_id', 'requestId']) {
      if (typeof content[key] === 'string' && content[key]) values.add(content[key]);
    }
  }
  return values;
}

export function isCorrelatedEvent(event, requestEventId) {
  if (!event || !requestEventId) return false;
  return requestCorrelationValues(event).has(requestEventId);
}

export async function publishRequest({ kind, tags = [], content = '', created_at = Math.floor(Date.now() / 1000) } = {}) {
  ensureKind(kind);

  const event = await signWithAuth({
    kind,
    created_at,
    tags: normalizeTags(tags),
    content: normalizeContent(content)
  });

  const results = await nostr.publish(event);
  const acceptedRelays = results.filter(publishAccepted);
  const rejectedRelays = results.filter((result) => !publishAccepted(result));

  if (acceptedRelays.length === 0) {
    const reason = rejectedRelays.map((result) => result.message).filter(Boolean).join('; ') || 'no relay accepted the request';
    throw new Error(`Nostr request publish rejected: ${reason}`);
  }

  return {
    requestEventId: event.id,
    event,
    ok: results,
    acceptedRelays,
    rejectedRelays
  };
}

export function subscribeStatus({ requestEventId, statusKinds, onStatus, onClosed, servicePubkey } = {}) {
  if (!requestEventId) throw new Error('requestEventId is required');
  if (!Array.isArray(statusKinds) || statusKinds.length === 0) throw new Error('statusKinds are required');
  if (typeof onStatus !== 'function') throw new Error('onStatus callback is required');

  const seen = new Set();
  return nostr.subscribe([requestFilter(statusKinds, requestEventId, servicePubkey)], {
    onEvent: (event, relay) => {
      if (!eventMatchesAuthor(event, servicePubkey)) return;
      if (!isCorrelatedEvent(event, requestEventId)) return;
      if (seen.has(event.id)) return;
      seen.add(event.id);
      onStatus(event, relay);
    },
    onClosed: (reason, relay) => {
      if (typeof onClosed === 'function') onClosed(reason, relay);
    }
  });
}

export function awaitResult({ requestEventId, resultKinds, signal, timeoutMs = null, servicePubkey } = {}) {
  if (!requestEventId) return Promise.reject(new Error('requestEventId is required'));
  if (!Array.isArray(resultKinds) || resultKinds.length === 0) return Promise.reject(new Error('resultKinds are required'));

  return new Promise((resolve, reject) => {
    let unsubscribe = null;
    let timer = null;
    let settled = false;
    const seen = new Set();
    const pendingRelays = openRelayUrls();

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

    const onAbort = () => settle(reject, signal?.reason instanceof Error ? signal.reason : new Error('Nostr result wait aborted'));

    if (signal?.aborted) {
      onAbort();
      return;
    }

    if (pendingRelays && pendingRelays.length === 0) {
      settle(reject, new Error('No connected Nostr relays available for result subscription'));
      return;
    }

    signal?.addEventListener?.('abort', onAbort, { once: true });

    if (Number.isFinite(timeoutMs) && timeoutMs > 0) {
      timer = setTimeout(() => {
        settle(reject, new Error(`Timed out waiting for Nostr result for ${requestEventId}`));
      }, timeoutMs);
    }

    unsubscribe = nostr.subscribe([requestFilter(resultKinds, requestEventId, servicePubkey)], {
      onEvent: (event) => {
        if (!eventMatchesAuthor(event, servicePubkey)) return;
        if (!isCorrelatedEvent(event, requestEventId)) return;
        if (seen.has(event.id)) return;
        seen.add(event.id);
        settle(resolve, event);
      },
      onClosed: (reason, relay) => {
        if (pendingRelays && relay) {
          const index = pendingRelays.indexOf(relay);
          if (index >= 0) pendingRelays.splice(index, 1);
          if (pendingRelays.length === 0) {
            settle(reject, new Error(`Nostr result subscription closed before result: ${reason || 'all relays closed'}`));
            return;
          }
        }
        if (reason && String(reason).toLowerCase().includes('auth')) {
          settle(reject, new Error(`Nostr result subscription closed: ${reason}`));
        }
      }
    });
  });
}

export function currentRequesterPubkey() {
  return authState.status === 'authenticated' ? authState.pubkey : null;
}
