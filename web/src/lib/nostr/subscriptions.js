import { createNostrPoolClient as createPoolClient } from './pool.js';
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
function getConfiguredRelays() {
  if (typeof window === 'undefined' || typeof localStorage === 'undefined' || typeof localStorage.getItem !== 'function') return DEFAULT_RELAYS;

  try {
    const stored = localStorage.getItem(RELAY_CONFIG_KEY);
    if (stored) {
      const relays = JSON.parse(stored);
      if (Array.isArray(relays) && relays.length > 0) {
        return relays;
      }
    }
  } catch (e) {
    console.error('[nostr] Failed to load relay config:', e);
  }

  return DEFAULT_RELAYS;
}

/**
 * Save relay configuration to localStorage
 */
export function saveRelayConfig(relays) {
  if (typeof window === 'undefined' || typeof localStorage === 'undefined' || typeof localStorage.setItem !== 'function') return;

  try {
    if (Array.isArray(relays) && relays.length > 0) {
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

export async function fetchTemplates(authorPubkey = null) {
  const filter = { kinds: [KINDS.SOUL_TEMPLATE] };
  if (authorPubkey) {
    filter.authors = [authorPubkey];
  }
  return dedupeReplaceableEvents(await nostr.queryUntilEose([filter]));
}

export async function fetchSouls(authorPubkey = null) {
  const filter = { kinds: [KINDS.AGENT_SOUL] };
  if (authorPubkey) {
    filter.authors = [authorPubkey];
  }
  return dedupeReplaceableEvents(await nostr.queryUntilEose([filter]));
}

export async function fetchSoulDrafts(authorPubkey = null) {
  const filter = { kinds: [KINDS.SOUL_DRAFT] };
  if (authorPubkey) {
    filter.authors = [authorPubkey];
  }
  return dedupeReplaceableEvents(await nostr.queryUntilEose([filter]));
}

export async function fetchSoulDraft(agentId, authorPubkey) {
  const events = await nostr.queryUntilEose([{
    kinds: [KINDS.SOUL_DRAFT],
    '#d': [agentId],
    authors: authorPubkey ? [authorPubkey] : undefined
  }]);
  return dedupeReplaceableEvents(events)[0] || null;
}

export async function fetchRuntimeCapabilities({ runtime = null, controllerPubkey = null, method = null, limit = 200 } = {}) {
  const events = dedupeReplaceableEvents(await nostr.queryUntilEose([{ kinds: [KINDS.RUNTIME_CAPABILITY], limit }]));
  return events
    .map(parseRuntimeCapabilityEvent)
    .filter(Boolean)
    .filter((capability) => runtimeCapabilitySupports(capability, { runtime, controllerPubkey, method }));
}

export async function fetchSoul(agentId, authorPubkey) {
  const events = await nostr.queryUntilEose([{
    kinds: [KINDS.AGENT_SOUL],
    '#d': [agentId],
    authors: authorPubkey ? [authorPubkey] : undefined
  }]);
  return dedupeReplaceableEvents(events)[0] || null;
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
