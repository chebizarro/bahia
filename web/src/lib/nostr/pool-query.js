import { NostrIncompleteEOSEError } from './pool-errors.js';
import { relaySummaryFromStates, uniqueRelays } from './pool-utils.js';

function isAuthRequiredReason(reason = '') {
  const normalized = String(reason || '').toLowerCase().trim();
  return normalized === 'auth-required' || normalized.startsWith('auth-required:');
}

function withEoseMetadata(events, relayStates) {
  const relaySummary = relaySummaryFromStates(relayStates);
  Object.defineProperty(events, 'eose', {
    value: {
      complete: relaySummary.every((relay) => relay.status === 'eose' || relay.status === 'excluded'),
      degraded: relaySummary.some((relay) => relay.status === 'excluded')
        ? {
            incomplete: false,
            reason: 'auth_required_relays_excluded',
            message: 'One or more AUTH-required relays were excluded from this EOSE query because no AUTH event was available.',
            relaySummary
          }
        : null,
      relaySummary
    },
    enumerable: false,
    configurable: true
  });
  return events;
}

export function queryUntilEose(client, filters, options = {}) {
  const queryOptions = typeof options === 'number' ? { timeoutMs: options } : (options || {});
  const { timeoutMs = null, signal = null, relays = null } = queryOptions;

  return new Promise((resolve, reject) => {
    const events = [];
    const seenEventIds = new Set();
    const targetRelays = Array.isArray(relays) ? uniqueRelays(relays) : client.getConnectedRelays();
    const relayStates = new Map(targetRelays.map((relay) => [relay, { status: 'pending', reason: '' }]));
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
      const activeStates = states.filter((state) => state.status !== 'excluded');
      if (activeStates.length === 0) {
        settle(reject, incomplete('all_relays_excluded', 'All Nostr query relays require AUTH, and no AUTH event was available'));
        return;
      }
      if (activeStates.every((state) => state.status === 'eose')) {
        settle(resolve, withEoseMetadata(events, relayStates));
        return;
      }
      if (activeStates.every((state) => state.status !== 'pending')) {
        settle(reject, incomplete('all_relays_closed', 'Nostr query relays closed before remaining relays satisfied EOSE'));
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

    unsub = client.subscribeOnRelays(targetRelays, filters, {
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
            const reasonText = String(reason || '');
            const excluded = meta?.authRequired === true || isAuthRequiredReason(reasonText);
            relayStates.set(relay, meta?.terminal === false
              ? { status: 'pending', reason: reasonText }
              : excluded
                ? { status: 'excluded', reason: reasonText || 'auth-required' }
                : { status: 'closed', reason: reasonText });
          }
        }
        if (meta?.terminal !== false) evaluateCompletion();
      }
    });
  });
}
