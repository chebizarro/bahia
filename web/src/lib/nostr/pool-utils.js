/**
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
  return typeof relay === 'string' && relay.trim() ? relay.trim() : 'unknown';
}

/**
 * Tracks pool subscription callback state for EOSE-authoritative historical reads.
 * CLOSED/AUTH before EOSE produces an explicit incomplete metadata contract;
 * EOSE remains the only successful completion signal.
 */
export function createReadModelMetadataTracker({ relays = [], partialEventCount = null } = {}) {
  const relayStates = new Map();
  const seenEvents = new Set();
  let observedEventCount = 0;

  for (const relay of uniqueRelays(relays)) {
    relayStates.set(relay, relayState());
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

function arrayFrom(value) {
  if (!value) return [];
  return Array.isArray(value) ? value : [value];
}

export function normalizeRelayUrl(url) {
  if (typeof url !== 'string' || !url.trim()) return '';
  try {
    let normalized = url.trim();
    if (!normalized.includes('://')) normalized = `wss://${normalized}`;
    const parsed = new URL(normalized);
    if (parsed.protocol === 'http:') parsed.protocol = 'ws:';
    if (parsed.protocol === 'https:') parsed.protocol = 'wss:';
    parsed.pathname = parsed.pathname.replace(/\/+/g, '/');
    if (parsed.pathname.endsWith('/')) parsed.pathname = parsed.pathname.slice(0, -1);
    if ((parsed.port === '80' && parsed.protocol === 'ws:') || (parsed.port === '443' && parsed.protocol === 'wss:')) parsed.port = '';
    parsed.searchParams.sort();
    parsed.hash = '';
    return parsed.toString();
  } catch {
    return url.trim();
  }
}

export function uniqueRelays(relays = []) {
  const seen = new Set();
  const out = [];
  for (const relay of arrayFrom(relays)) {
    const url = typeof relay === 'string' ? relay.trim() : '';
    if (!url) continue;
    const key = normalizeRelayUrl(url);
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(url);
  }
  return out;
}

export function summarizeRelayConnections(relays, statusMap = {}) {
  const relayStatuses = relays.map((url) => ({
    url,
    status: statusMap[url] || 'unknown'
  }));
  const connected = relayStatuses.filter((relay) => relay.status === 'connected').length;
  const failed = relayStatuses.filter((relay) => ['error', 'failed', 'disconnected', 'auth-required'].includes(relay.status)).length;
  const connecting = relayStatuses.filter((relay) => relay.status === 'connecting').length;
  return { total: relays.length, connected, failed, connecting, relays: relayStatuses };
}

export function messageFromError(error) {
  if (error instanceof Error) return error.message;
  if (typeof error === 'string') return error;
  return String(error || 'unknown relay error');
}

export function publishSentBeforeFailure(message) {
  const lower = String(message || '').toLowerCase();
  return !lower.includes('connection failure') &&
    !lower.includes('closed connection') &&
    !lower.includes('connection skipped') &&
    !lower.includes('relay connection closed') &&
    !lower.includes('websocket closed');
}
