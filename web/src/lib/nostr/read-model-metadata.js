import { uniqueRelays } from './pool-utils.js';

export const MAX_SEEN_EVENT_IDS = 4096;

export function createBoundedEventIdSet(capacity = MAX_SEEN_EVENT_IDS) {
  if (!Number.isInteger(capacity) || capacity < 1) throw new RangeError('capacity must be positive');
  const ids = new Map();
  return {
    has(id) {
      if (!ids.has(id)) return false;
      ids.delete(id);
      ids.set(id, true);
      return true;
    },
    add(id) {
      ids.delete(id);
      ids.set(id, true);
      if (ids.size > capacity) ids.delete(ids.keys().next().value);
    },
    get size() { return ids.size; }
  };
}

/***/
 * @typedef {Object} PoolReadModelMetadata
 * @property {boolean} complete True only when every expected/observed relay reached EOSE.
 * @property {Object|null} degraded Incomplete/degraded read details, or null for complete history.
 * @property {Array<Object>} relaySummary Per-relay EOSE/CLOSED/AUTH state used to build the metadata.
 */

export function relaySummaryFromStates(relayStates) {
  return Array.from(relayStates.entries()).map(([relay, state]) => ({ relay, ...state }));
}

function relayState(status = 'pending') {
  return {
    status,
    eose: false,
    closed: false,
    authRequired: false,
    reason: '',
    source: '',
    terminal: false
  };
}

function defaultIncompleteReason(relaySummary) {
  const auth = relaySummary.find((relay) => relay.authRequired || relay.status === 'auth-required');
  if (auth) return 'auth-required';
  const closed = relaySummary.find((relay) => relay.closed);
  if (closed) return closed.source || 'closed-before-eose';
  const pending = relaySummary.find((relay) => !relay.terminal);
  if (pending) return 'incomplete';
  return 'incomplete';
}

function defaultIncompleteMessage(relaySummary) {
  const auth = relaySummary.find((relay) => relay.authRequired || relay.status === 'auth-required');
  if (auth) return `Relay ${auth.relay || 'unknown'} required AUTH before EOSE${auth.reason ? `: ${auth.reason}` : ''}`;
  const closed = relaySummary.find((relay) => relay.closed);
  if (closed) return `Relay ${closed.relay || 'unknown'} closed before EOSE${closed.reason ? `: ${closed.reason}` : ''}`;
  return 'Historical relay read did not reach EOSE on every expected relay.';
}

function normalizeRelayForState(relay) {
  return typeof relay === 'string' && relay.trim() ? relay.trim().replace(/\/+$/, '') : 'unknown';
}

/***/
 * Tracks pool subscription callback state for EOSE-authoritative historical reads.
 * CLOSED/AUTH before EOSE produces an explicit incomplete metadata contract;
 * EOSE remains the only successful completion signal.
 */
export function createReadModelMetadataTracker({ relays = [], partialEventCount = null } = {}) {
  const relayStates = new Map();
  const seenEvents = createBoundedEventIdSet();
  let observedEventCount = 0;

  for (const relay of uniqueRelays(relays)) {
    relayStates.set(normalizeRelayForState(relay), relayState());
  }

  const ensureRelayState = (relay) => {
    const key = normalizeRelayForState(relay);
    if (!relayStates.has(key)) relayStates.set(key, relayState());
    return [key, relayStates.get(key)];
  };

  const countEvents = () => {
    try {
      const value = partialEventCount();
      if (Number.isFinite(value)) return value;
    } catch {
      // Fall back to events observed by this tracker.
    }
    return observedEventCount;
  };

  const relaySummary = () => relaySummaryFromStates(relayStates);
  const isComplete = () => relayStates.size > 0 && Array.from(relayStates.values()).every((state) => state.eose && !state.closed && !state.authRequired);
  const isTerminal = () => relayStates.size > 0 && Array.from(relayStates.values()).every((state) => state.terminal);

  return {
    relayStates,
    markEvent(event, relay) {
      if (relay) ensureRelayState(relay);
      if (event?.id) {
        if (seenEvents.has(event.id)) return;
        seenEvents.add(event.id);
      }
      observedEventCount += 1;
    },
    markEose(relay) {
      const [, state] = ensureRelayState(relay);
      state.status = 'eose';
      state.eose = true;
      state.closed = false;
      state.authRequired = false;
      state.reason = '';
      state.source = '';
      state.terminal = true;
    },
    markClosed(reason = '', relay = '', meta = {}) {
      const [, state] = ensureRelayState(relay);
      const reasonText = String(reason || '');
      const authRequired = meta?.authRequired === true || meta?.source === 'auth' || reasonText.toLowerCase().trim().startsWith('auth-required');
      if (state.eose) {
        state.closedAfterEose = true;
        state.closeReason = reasonText;
        state.closeSource = meta?.source || 'closed';
        state.terminal = true;
        return;
      }
      state.status = authRequired ? 'auth-required' : 'closed';
      state.closed = true;
      state.authRequired = authRequired;
      state.reason = reasonText;
      state.source = meta?.source || (authRequired ? 'auth' : 'closed');
      state.terminal = meta?.terminal !== false;
    },
    markAuth(challenge = '', relay = '') {
      const [, state] = ensureRelayState(relay);
      if (state.eose || state.closed) return;
      state.status = 'auth-challenge';
      state.authRequired = true;
      state.reason = String(challenge || 'auth-required');
      state.source = 'auth';
    },
    isComplete,
    isTerminal,
    relaySummary,
    metadata({ degraded = null, forceIncomplete = false, reason = '', message = '' } = {}) {
      const summary = relaySummary();
      const complete = !forceIncomplete && isComplete();
      let degradedMeta = null;

      if (!complete || forceIncomplete) {
        degradedMeta = {
          incomplete: true,
          reason: reason || defaultIncompleteReason(summary),
          message: message || defaultIncompleteMessage(summary),
          relaySummary: summary,
          partialEventCount: countEvents(),
          authRequired: summary.some((relay) => relay.authRequired || relay.status === 'auth-required')
        };
      } else if (degraded) {
        degradedMeta = {
          ...degraded,
          incomplete: degraded.incomplete === true,
          relaySummary: Array.isArray(degraded.relaySummary) && degraded.relaySummary.length > 0 ? degraded.relaySummary : summary,
          partialEventCount: Number.isFinite(degraded.partialEventCount) ? degraded.partialEventCount : countEvents()
        };
      }

      return { complete, degraded: degradedMeta, relaySummary: summary };
    }
  };
}
