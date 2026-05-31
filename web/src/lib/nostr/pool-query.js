import { NostrIncompleteEOSEError } from './pool-errors.js';
import { relaySummaryFromStates } from './pool-utils.js';

export function queryUntilEose(client, filters, options = {}) {
  const queryOptions = typeof options === 'number' ? { timeoutMs: options } : (options || {});
  const { timeoutMs = null, signal = null } = queryOptions;

  return new Promise((resolve, reject) => {
    const events = [];
    const seenEventIds = new Set();
    const connectedRelays = client.getConnectedRelays();
    const relayStates = new Map(connectedRelays.map((relay) => [relay, { status: 'pending', reason: '' }]));
    let unsub = null;
    let timer = null;
    let settled = false;

    const cleanup = () => {
      if (timer) clearTimeout(timer);
      timer = null;
      if (unsub) unsub();
      unsub = null;
      signal?.removeEventListener?.('abort', onAbort);
    };
    const incomplete = (reason, message = '') => new NostrIncompleteEOSEError(reason, {
      partialEvents: [...events], relaySummary: relaySummaryFromStates(relayStates), message
    });
    const settle = (fn, value) => {
      if (settled) return;
      settled = true;
      cleanup();
      fn(value);
    };
    const onAbort = () => {
      settle(reject, incomplete('aborted', signal?.reason?.message || 'Nostr query aborted before EOSE completion'));
    };
    const evaluateCompletion = () => {
      if (settled) return;
      const states = Array.from(relayStates.values());
      if (states.length === 0) return;
      if (states.every((state) => state.status === 'eose')) {
        settle(resolve, events);
        return;
      }
      if (states.every((state) => state.status !== 'pending')) {
        settle(reject, incomplete('all_relays_closed', 'Nostr query relays closed before all EOSE messages were received'));
      }
    };

    if (relayStates.size === 0) {
      settle(reject, incomplete('all_relays_closed', 'No connected Nostr relays available for EOSE query'));
      return;
    }
    if (signal?.aborted) {
      onAbort();
      return;
    }
    signal?.addEventListener?.('abort', onAbort, { once: true });
    if (Number.isFinite(timeoutMs) && timeoutMs > 0) {
      timer = setTimeout(() => {
        settle(reject, incomplete('timeout', `Timed out waiting for Nostr EOSE after ${timeoutMs}ms`));
      }, timeoutMs);
    }

    unsub = client.subscribeOnRelays(connectedRelays, filters, {
      onEvent: (event) => {
        if (event?.id && seenEventIds.has(event.id)) return;
        if (event?.id) seenEventIds.add(event.id);
        events.push(event);
      },
      onEose: (relay) => {
        if (relayStates.has(relay)) relayStates.set(relay, { status: 'eose', reason: '' });
        evaluateCompletion();
      },
      onClosed: (reason = '', relay, meta = {}) => {
        if (relayStates.has(relay)) {
          const current = relayStates.get(relay);
          if (current?.status !== 'eose') {
            relayStates.set(relay, meta?.terminal === false
              ? { status: 'pending', reason: String(reason || '') }
              : { status: 'closed', reason: String(reason || '') });
          }
        }
        if (meta?.terminal !== false) evaluateCompletion();
      }
    });
  });
}
