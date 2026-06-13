import { createNostrPoolClient as createPoolClient, NostrIncompleteEOSEError } from './pool.js';
import { KINDS, isLifecycleResultKind } from './kinds.js';
import { validateInboundNostrEvent } from './validation.js';
import { dedupeReplaceableEvents } from './replaceable.js';
import { parseRuntimeCapabilityEvent, runtimeCapabilitySupports } from './runtime.js';


// Default relays - can be overridden via localStorage or connect() parameter
const DEFAULT_RELAYS = [
  'wss://bahia.sharegap.net/relay'
];

// Storage key for user-configured relays
const RELAY_CONFIG_KEY = 'bahia_nostr_relays';


/**
 * Get configured relays from localStorage or return defaults
 */
export function getConfiguredRelays() {
  if (typeof window === 'undefined' || typeof localStorage === 'undefined' || typeof localStorage.getItem !== 'function') return DEFAULT_RELAYS;

  try {
    const stored = localStorage.getItem(RELAY_CONFIG_KEY);
    if (stored !== null) {
      const relays = JSON.parse(stored);
      if (Array.isArray(relays)) {
        return relays;
      }
    }
  } catch (e) {
    console.error('[nostr] Failed to load relay config:', e);
  }

  return DEFAULT_RELAYS;
}

export function hasSavedRelayConfig() {
  return typeof window !== 'undefined'
    && typeof localStorage !== 'undefined'
    && typeof localStorage.getItem === 'function'
    && localStorage.getItem(RELAY_CONFIG_KEY) !== null;
}

/**
 * Save relay configuration to localStorage
 */
export function saveRelayConfig(relays) {
  if (typeof window === 'undefined' || typeof localStorage === 'undefined' || typeof localStorage.setItem !== 'function') return;

  try {
    if (Array.isArray(relays)) {
      localStorage.setItem(RELAY_CONFIG_KEY, JSON.stringify(relays));
    } else {
      localStorage.removeItem(RELAY_CONFIG_KEY);
    }
  } catch (e) {
    console.error('[nostr] Failed to save relay config:', e);
  }
}

/**
 * Get the default relay list
 */
export function getDefaultRelays() {
  return [...DEFAULT_RELAYS];
}

export function createNostrPoolClient(options = {}) {
  return createPoolClient({
    relays: getConfiguredRelays(),
    saveRelayConfig,
    validateEvent: validateInboundNostrEvent,
    ...options
  });
}

// Singleton instance
export const nostr = createNostrPoolClient();

// --- Soul Factory specific helpers ---

function logIncompleteEoseFallback(scope, error, events, { acceptedEmpty = false } = {}) {
  const relaySummary = Array.isArray(error?.relaySummary)
    ? error.relaySummary.map((relay) => `${relay.relay}:${relay.status}${relay.reason ? ` (${relay.reason})` : ''}`).join(', ')
    : '';
  const reason = error?.reason || 'unknown';
  const detail = relaySummary ? ` relays=${relaySummary}` : '';
  const qualifier = acceptedEmpty && events.length === 0 ? 'empty partial ' : 'partial ';
  console.warn(`[${scope}] Using ${events.length} ${qualifier}Nostr event(s) after incomplete EOSE (${reason}).${detail}`);
}

export function partialEventsFromIncompleteEose(error) {
  if (!(error instanceof NostrIncompleteEOSEError)) return [];
  return Array.isArray(error.partialEvents) ? error.partialEvents.filter(Boolean) : [];
}

function degradedMetadataFromIncompleteEose(error, events) {
  return {
    incomplete: true,
    reason: error?.reason || 'unknown',
    message: error?.message || 'Nostr query did not receive complete EOSE history',
    relaySummary: Array.isArray(error?.relaySummary) ? error.relaySummary : [],
    partialEventCount: events.length
  };
}

function queryResult(events, degraded = null) {
  const eose = events?.eose;
  const effectiveDegraded = degraded || eose?.degraded || null;
  return {
    events,
    complete: !effectiveDegraded,
    degraded: effectiveDegraded,
    relaySummary: effectiveDegraded?.relaySummary || eose?.relaySummary || []
  };
}

export function readModelEvents(result) {
  if (Array.isArray(result)) return result;
  return Array.isArray(result?.events) ? result.events : [];
}

export function readModelDegraded(result) {
  return result?.degraded || null;
}

export function attachReadModelMetadata(items, result) {
  const list = Array.isArray(items) ? items : [];
  Object.defineProperty(list, 'eose', {
    value: {
      complete: result?.complete !== false,
      degraded: readModelDegraded(result),
      relaySummary: Array.isArray(result?.relaySummary) ? result.relaySummary : []
    },
    enumerable: false,
    configurable: true
  });
  return list;
}

export async function queryUntilEoseOrPartial(filters, options = {}) {
  const queryOptions = typeof options === 'number' ? { timeoutMs: options } : (options || {});
  const { scope = 'nostr.query', allowEmptyOnIncomplete = false, ...nostrOptions } = queryOptions;

  try {
    return queryResult(await nostr.queryUntilEose(filters, nostrOptions));
  } catch (error) {
    const partialEvents = partialEventsFromIncompleteEose(error);
    if (partialEvents.length > 0) {
      logIncompleteEoseFallback(scope, error, partialEvents);
      return queryResult(partialEvents, degradedMetadataFromIncompleteEose(error, partialEvents));
    }
    if (allowEmptyOnIncomplete && error instanceof NostrIncompleteEOSEError) {
      logIncompleteEoseFallback(scope, error, partialEvents, { acceptedEmpty: true });
      return queryResult([], degradedMetadataFromIncompleteEose(error, partialEvents));
    }
    throw error;
  }
}

export async function queryOrPartial(filters, options = {}) {
  const queryOptions = typeof options === 'number' ? { timeoutMs: options } : (options || {});
  return queryUntilEoseOrPartial(filters, { ...queryOptions, allowEmptyOnIncomplete: true });
}

export async function fetchTemplates(authorPubkey = null) {
  const filter = { kinds: [KINDS.SOUL_TEMPLATE] };
  if (authorPubkey) {
    filter.authors = [authorPubkey];
  }
  const result = await queryUntilEoseOrPartial([filter], { scope: 'templates', allowEmptyOnIncomplete: true });
  return attachReadModelMetadata(dedupeReplaceableEvents(readModelEvents(result)), result);
}

export async function fetchSouls(authorPubkey = null) {
  const filter = { kinds: [KINDS.AGENT_SOUL] };
  if (authorPubkey) {
    filter.authors = [authorPubkey];
  }
  const result = await queryUntilEoseOrPartial([filter], { scope: 'souls', allowEmptyOnIncomplete: true });
  return attachReadModelMetadata(dedupeReplaceableEvents(readModelEvents(result)), result);
}

export async function fetchSoulDrafts(authorPubkey = null) {
  const filter = { kinds: [KINDS.SOUL_DRAFT] };
  if (authorPubkey) {
    filter.authors = [authorPubkey];
  }
  const result = await queryUntilEoseOrPartial([filter], { scope: 'soul-drafts', allowEmptyOnIncomplete: true });
  return attachReadModelMetadata(dedupeReplaceableEvents(readModelEvents(result)), result);
}

export async function fetchSoulDraft(agentId, authorPubkey) {
  const result = await queryUntilEoseOrPartial([{
    kinds: [KINDS.SOUL_DRAFT],
    '#d': [agentId],
    authors: authorPubkey ? [authorPubkey] : undefined
  }], { scope: 'soul-draft', allowEmptyOnIncomplete: true });
  const [event = null] = dedupeReplaceableEvents(readModelEvents(result));
  return event ? { ...event, eose: { complete: result.complete, degraded: result.degraded, relaySummary: result.relaySummary } } : null;
}

export async function fetchRuntimeCapabilities({ runtime = null, controllerPubkey = null, method = null, limit = 200 } = {}) {
  const result = await queryUntilEoseOrPartial([{ kinds: [KINDS.RUNTIME_CAPABILITY], limit }], { scope: 'runtime-capabilities', allowEmptyOnIncomplete: true });
  const events = dedupeReplaceableEvents(readModelEvents(result));
  const capabilities = events
    .map(parseRuntimeCapabilityEvent)
    .filter(Boolean)
    .filter((capability) => runtimeCapabilitySupports(capability, { runtime, controllerPubkey, method }));
  return attachReadModelMetadata(capabilities, result);
}

export async function fetchSoul(agentId, authorPubkey) {
  const result = await queryUntilEoseOrPartial([{
    kinds: [KINDS.AGENT_SOUL],
    '#d': [agentId],
    authors: authorPubkey ? [authorPubkey] : undefined
  }], { scope: 'soul', allowEmptyOnIncomplete: true });
  const [event = null] = dedupeReplaceableEvents(readModelEvents(result));
  return event ? { ...event, eose: { complete: result.complete, degraded: result.degraded, relaySummary: result.relaySummary } } : null;
}

export function subscribeToProvisioningProgress(requestEventId, onStatus, onResult) {
  return nostr.subscribe([
    { kinds: [KINDS.PROVISIONING_STATUS], '#e': [requestEventId] },
    { kinds: [KINDS.PROVISIONING_RESULT, KINDS.SOUL_ACTION_LEGACY_RESULT], '#e': [requestEventId] }
  ], {
    onEvent: (event) => {
      if (event.kind === KINDS.PROVISIONING_STATUS) {
        onStatus(parseProvisioningStatus(event));
      } else if (isLifecycleResultKind(event.kind)) {
        onResult(parseProvisioningResult(event));
      }
    }
  });
}

function parseProvisioningStatus(event) {
  const status = {
    id: event.id,
    step: '',
    progress: { current: 0, total: 0 },
    message: event.content
  };

  for (const tag of event.tags) {
    switch (tag[0]) {
      case 'step': status.step = tag[1]; break;
      case 'progress':
        status.progress = { current: parseInt(tag[1]), total: parseInt(tag[2]) };
        break;
    }
  }

  return status;
}

function parseProvisioningResult(event) {
  const result = {
    id: event.id,
    success: false,
    error: '',
    soulRef: '',
    action: '',
    requestKind: '',
    specHash: '',
    data: {},
    legacyKind: event.kind === KINDS.SOUL_ACTION_LEGACY_RESULT
  };

  for (const tag of event.tags) {
    switch (tag[0]) {
      case 'status':
        result.success = tag[1] === 'success';
        if (tag[1] === 'error') result.error = event.content;
        break;
      case 'soul': result.soulRef = tag[1]; break;
      case 'action': result.action = tag[1]; break;
      case 'request-kind': result.requestKind = tag[1]; break;
      case 'spec-hash': result.specHash = tag[1]; break;
    }
  }

  if (event.content) {
    try {
      result.data = JSON.parse(event.content);
    } catch (e) {
      // Content might not be JSON
    }
  }

  return result;
}

export default nostr;
