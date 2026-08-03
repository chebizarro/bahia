import { createNostrPoolClient as createPoolClient } from './pool.js';
import { KINDS, isLifecycleResultKind } from './kinds.js';
import { validateInboundNostrEvent } from './validation.js';


// Default relays - can be overridden via localStorage or connect() parameter
const DEFAULT_RELAYS = [
  'wss://relay.sharegap.net'
];

// Storage key for the explicitly local, noncanonical emergency override.
const RELAY_CONFIG_KEY = 'bahia_nostr_relays';
export const RELAY_OVERRIDE_STORAGE_SCHEMA = 'bahia.browser-relay-override.v2';

function normalizeLocalRelayOverride(values) {
  if (!Array.isArray(values)) return [];
  const normalized = [];
  const seen = new Set();
  for (const raw of values) {
    try {
      const url = new URL(String(raw || '').trim());
      if (!['ws:', 'wss:'].includes(url.protocol)) continue;
      // Browser storage must never retain credential-bearing relay URLs.
      if (url.username || url.password || url.search || url.hash) continue;
      const value = url.toString();
      if (!seen.has(value)) {
        seen.add(value);
        normalized.push(value);
      }
    } catch {
      // Invalid legacy entries are incompatible and intentionally not migrated.
    }
  }
  return normalized;
}

function relayOverrideEnvelope(relays) {
  return {
    schema: RELAY_OVERRIDE_STORAGE_SCHEMA,
    scope: 'browser-local-noncanonical',
    relays: normalizeLocalRelayOverride(relays)
  };
}

function parseRelayOverride(raw) {
  const parsed = JSON.parse(raw);
  if (Array.isArray(parsed)) {
    return { relays: normalizeLocalRelayOverride(parsed), migrationRequired: true };
  }
  if (parsed && typeof parsed === 'object' && Array.isArray(parsed.relays)) {
    const relays = normalizeLocalRelayOverride(parsed.relays);
    return {
      relays,
      migrationRequired: parsed.schema !== RELAY_OVERRIDE_STORAGE_SCHEMA
        || parsed.scope !== 'browser-local-noncanonical'
        || JSON.stringify(parsed.relays) !== JSON.stringify(relays)
    };
  }
  return null;
}

/**
 * Get the local, noncanonical emergency override or return defaults. Legacy
 * arrays and compatible object envelopes are migrated in place without
 * changing their safe relay values.
 */
export function getConfiguredRelays() {
  if (typeof window === 'undefined' || typeof localStorage === 'undefined' || typeof localStorage.getItem !== 'function') return [...DEFAULT_RELAYS];

  try {
    const stored = localStorage.getItem(RELAY_CONFIG_KEY);
    if (stored !== null) {
      const decoded = parseRelayOverride(stored);
      if (decoded) {
        if (decoded.migrationRequired) {
          localStorage.setItem(RELAY_CONFIG_KEY, JSON.stringify(relayOverrideEnvelope(decoded.relays)));
        }
        return decoded.relays;
      }
      localStorage.setItem(RELAY_CONFIG_KEY, JSON.stringify(relayOverrideEnvelope([])));
      return [];
    }
  } catch {
    // Do not log parse details: a malformed legacy value could contain secrets.
    try {
      localStorage.setItem(RELAY_CONFIG_KEY, JSON.stringify(relayOverrideEnvelope([])));
      return [];
    } catch {
      console.error('[nostr] Failed to scrub local emergency relay override');
    }
  }

  return [...DEFAULT_RELAYS];
}

export function hasSavedRelayConfig() {
  return typeof window !== 'undefined'
    && typeof localStorage !== 'undefined'
    && typeof localStorage.getItem === 'function'
    && localStorage.getItem(RELAY_CONFIG_KEY) !== null;
}

/**
 * Save the explicitly local, noncanonical emergency override. Credential,
 * query, and fragment-bearing URLs are excluded from browser persistence.
 */
export function saveRelayConfig(relays) {
  if (typeof window === 'undefined' || typeof localStorage === 'undefined' || typeof localStorage.setItem !== 'function') return;

  try {
    if (Array.isArray(relays)) {
      localStorage.setItem(RELAY_CONFIG_KEY, JSON.stringify(relayOverrideEnvelope(relays)));
    } else {
      localStorage.removeItem(RELAY_CONFIG_KEY);
    }
  } catch {
    console.error('[nostr] Failed to save local emergency relay override');
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
