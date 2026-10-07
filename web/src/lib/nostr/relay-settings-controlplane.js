import { getEventStore, getServicePubkey } from './boot.js';
import { FLEET_INTENT_ORG_ID } from './intent-client.svelte.js';
import { relayPolicyIntent } from './final-ops-intents.js';
import { submitSensitiveIntent } from '../stores/sensitive-intents.svelte.js';
import { CASCADIA_CONTROLPLANE_STATE } from './kinds.gen.js';
import { parseJsonContent } from './content.js';
import { getDTag, getTagValue } from './tags.js';
import { nostr as defaultNostr } from './subscriptions.js';

export const RELAY_SETTINGS_SCHEMA = 'bahia.relay-settings.v1';
export const RELAY_SETTINGS_DOMAIN = 'relay-settings';
export const RELAY_SETTINGS_DTAG = 'relay-settings:operator';

export const RELAY_POLICY_TRUTH_STATES = Object.freeze({
  LOADING: 'loading',
  LOADED_LIVE: 'loaded-live',
  LOADED_CACHED: 'loaded-cached',
  LOADED_STALE: 'loaded-stale',
  UNAVAILABLE: 'unavailable',
  NEVER_CONFIGURED: 'never-configured',
  INTENTIONALLY_EMPTY: 'intentionally-empty'
});

function normalizeRelayArray(values = []) {
  if (!Array.isArray(values)) return [];
  const seen = new Set();
  const out = [];
  for (const raw of values) {
    for (const part of String(raw || '').split(',')) {
      const value = part.trim();
      if (!value || seen.has(value)) continue;
      seen.add(value);
      out.push(value);
    }
  }
  return out;
}

function normalizePubkeyArray(values = []) {
  if (!Array.isArray(values)) return [];
  const seen = new Set();
  const out = [];
  for (const raw of values) {
    const value = String(raw || '').trim().toLowerCase();
    if (!/^[0-9a-f]{64}$/.test(value) || seen.has(value)) continue;
    seen.add(value);
    out.push(value);
  }
  return out.sort();
}

export function buildRelayPolicyPayload(policy = {}) {
  return {
    schema: RELAY_SETTINGS_SCHEMA,
    browser_relays: normalizeRelayArray(policy.browser_relays),
    contextvm_relays: normalizeRelayArray(policy.contextvm_relays),
    service_relays: normalizeRelayArray(policy.service_relays),
    nip34_relays: normalizeRelayArray(policy.nip34_relays),
    trusted_relay_monitor_pubkeys: normalizePubkeyArray(policy.trusted_relay_monitor_pubkeys),
    dm_relay_lists: (policy.dm_relay_lists || []).map((list) => ({
      enabled: Boolean(list.enabled),
      feature: String(list.feature || '').trim().toLowerCase(),
      identity: String(list.identity || '').trim().toLowerCase(),
      relays: normalizeRelayArray(list.relays)
    })),
    relay_administration: {
      enabled: Boolean(policy.relay_administration?.enabled),
      targets: (policy.relay_administration?.targets || []).map((target) => ({
        ref: String(target.ref || '').trim(),
        relay_url: String(target.relay_url || '').trim(),
        http_url: String(target.http_url || '').trim(),
        authorization: String(target.authorization || '').trim().toLowerCase(),
        administrator_pubkeys: normalizePubkeyArray(target.administrator_pubkeys)
      }))
    }
  };
}

export function isRelayPolicyIntentionallyEmpty(policy = {}) {
  const normalized = buildRelayPolicyPayload(policy);
  return normalized.browser_relays.length === 0
    && normalized.contextvm_relays.length === 0
    && normalized.service_relays.length === 0
    && normalized.nip34_relays.length === 0
    && normalized.trusted_relay_monitor_pubkeys.length === 0
    && normalized.dm_relay_lists.length === 0
    && !normalized.relay_administration.enabled
    && normalized.relay_administration.targets.length === 0;
}

function safeProvenanceToken(value) {
  const token = String(value || '').trim();
  return /^[a-zA-Z0-9:_-]{1,256}$/.test(token) ? token : '';
}

function safeProvenanceRelay(value) {
  try {
    const url = new URL(String(value || '').trim());
    if (!['ws:', 'wss:'].includes(url.protocol) || url.username || url.password) return '';
    url.search = '';
    url.hash = '';
    return url.toString();
  } catch {
    return '';
  }
}

export function normalizeRelayPolicyProjectionResponse(response = {}) {
  const payload = response;
  const projection = payload?.server_projection && typeof payload.server_projection === 'object'
    ? payload.server_projection
    : {};
  const policy = payload?.canonical_policy || payload?.state || null;
  const status = String(payload?.status || '').trim();
  const advertisedTruth = String(payload?.truth_state || '').trim();

  let truthState = RELAY_POLICY_TRUTH_STATES.UNAVAILABLE;
  if (advertisedTruth === RELAY_POLICY_TRUTH_STATES.LOADING) {
    truthState = RELAY_POLICY_TRUTH_STATES.LOADING;
  } else if (advertisedTruth === RELAY_POLICY_TRUTH_STATES.NEVER_CONFIGURED || status === 'never-configured') {
    truthState = RELAY_POLICY_TRUTH_STATES.NEVER_CONFIGURED;
  } else if (policy) {
    if (isRelayPolicyIntentionallyEmpty(policy)) {
      truthState = RELAY_POLICY_TRUTH_STATES.INTENTIONALLY_EMPTY;
    } else if (projection.freshness === 'stale' || advertisedTruth === RELAY_POLICY_TRUTH_STATES.LOADED_STALE) {
      truthState = RELAY_POLICY_TRUTH_STATES.LOADED_STALE;
    } else {
      truthState = RELAY_POLICY_TRUTH_STATES.LOADED_CACHED;
    }
  }

  return {
    truthState,
    policy: policy ? buildRelayPolicyPayload(policy) : null,
    provenance: {
      event_id: safeProvenanceToken(projection.event_id),
      event_created_at: String(projection.event_created_at || '').trim(),
      hash: safeProvenanceToken(projection.hash),
      source_relay: safeProvenanceRelay(projection.source_relay),
      last_sync_at: String(projection.last_sync_at || '').trim(),
      freshness: String(projection.freshness || '').trim(),
      source: String(projection.source || '').trim()
    }
  };
}

export function liveRelayPolicyTruth(state, { event, relay, receivedAt = new Date().toISOString() } = {}) {
  return {
    truthState: isRelayPolicyIntentionallyEmpty(state)
      ? RELAY_POLICY_TRUTH_STATES.INTENTIONALLY_EMPTY
      : RELAY_POLICY_TRUTH_STATES.LOADED_LIVE,
    policy: buildRelayPolicyPayload(state),
    provenance: {
      event_id: safeProvenanceToken(event?.id),
      event_created_at: Number.isFinite(Number(event?.created_at))
        ? new Date(Number(event.created_at) * 1000).toISOString()
        : '',
      hash: '',
      source_relay: safeProvenanceRelay(relay),
      last_sync_at: receivedAt,
      freshness: 'live',
      source: 'canonical_relay_event'
    }
  };
}

export function compareRelayPolicyTruthCandidates(candidate, current) {
  if (!current) return 1;
  if (!candidate) return -1;
  const candidateCreated = Date.parse(candidate.provenance?.event_created_at || '') || 0;
  const currentCreated = Date.parse(current.provenance?.event_created_at || '') || 0;
  if (candidateCreated !== currentCreated) return candidateCreated > currentCreated ? 1 : -1;
  const candidateID = String(candidate.provenance?.event_id || '');
  const currentID = String(current.provenance?.event_id || '');
  if (!candidateID || !currentID || candidateID === currentID) return 0;
  return candidateID < currentID ? 1 : -1;
}

// The operator policy is one addressable coordinate, so the REQ names it by
// its single-letter #d (plus kind and service author). Relays index only
// single-letter tags: domain and schema are checked locally in
// parseRelayPolicyStateEvent, never sent as #domain/#schema. The record also
// carries t=relay-settings, but #t would AND with #d and miss a policy
// retained from before the topic was stamped (bahia-irsry.37).
export function relayPolicyReadModelFilter({ servicePubkey, since, limit = 10 } = {}) {
  const filter = {
    kinds: [CASCADIA_CONTROLPLANE_STATE],
    '#d': [RELAY_SETTINGS_DTAG],
    limit
  };
  const author = String(servicePubkey || '').trim().toLowerCase();
  if (author) filter.authors = [author];
  if (since) filter.since = since;
  return filter;
}

export function parseRelayPolicyStateEvent(event, { servicePubkey } = {}) {
  if (!event || event.kind !== CASCADIA_CONTROLPLANE_STATE) return null;
  const trustedAuthor = String(servicePubkey || '').trim().toLowerCase();
  if (trustedAuthor && String(event.pubkey || '').toLowerCase() !== trustedAuthor) return null;
  if (getDTag(event) !== RELAY_SETTINGS_DTAG) return null;
  if (getTagValue(event, 'domain', '') !== RELAY_SETTINGS_DOMAIN) return null;
  if (getTagValue(event, 'schema', '') !== RELAY_SETTINGS_SCHEMA) return null;

  const content = parseJsonContent(event, null);
  if (!content || typeof content !== 'object' || Array.isArray(content)) return null;
  if (content.schema !== RELAY_SETTINGS_SCHEMA) return null;

  const state = buildRelayPolicyPayload(content);
  return {
    ...state,
    updated_at: String(content.updated_at || ''),
    updated_by: String(content.updated_by || '')
  };
}

function isNewerReplaceableEvent(candidate, current) {
  if (!current) return true;
  const candidateCreated = Number(candidate?.created_at || 0);
  const currentCreated = Number(current?.created_at || 0);
  if (candidateCreated !== currentCreated) return candidateCreated > currentCreated;
  return String(candidate?.id || '') < String(current?.id || '');
}

export function subscribeRelayPolicyReadModel({
  client = defaultNostr,
  relays,
  servicePubkey,
  since,
  onState,
  onEose,
  onClosed,
  onAuth
} = {}) {
  const targetRelays = normalizeRelayArray(Array.isArray(relays) && relays.length > 0 ? relays : client?.getRelays?.());
  if (!client || typeof client.subscribeOnRelays !== 'function') {
    throw new Error('relay settings read-model subscription requires a Nostr client');
  }
  if (targetRelays.length === 0) {
    throw new Error('relay settings read-model subscription requires at least one relay');
  }
  if (!String(servicePubkey || '').trim()) {
    throw new Error('relay settings read-model subscription requires a trusted service pubkey');
  }

  let latestEvent = null;
  const seenEventIds = new Set();
  // Pool-backed subscriptions validate NIP-01 event id/signature before invoking onEvent;
  // this helper then performs relay-settings schema/tag/trusted-author validation.
  const applyEvent = (event, relay) => {
    if (event?.id && seenEventIds.has(event.id)) return;
    if (event?.id) seenEventIds.add(event.id);
    const state = parseRelayPolicyStateEvent(event, { servicePubkey });
    if (!state || !isNewerReplaceableEvent(event, latestEvent)) return;
    latestEvent = event;
    onState?.(state, { event, relay });
  };

  return client.subscribeOnRelays(targetRelays, [relayPolicyReadModelFilter({ servicePubkey, since })], {
    onEvent: applyEvent,
    onEose,
    onClosed,
    onAuth
  });
}

export async function getRelayPolicy() {
  const servicePubkey = getServicePubkey();
  const event = getEventStore()?.query(relayPolicyReadModelFilter({ servicePubkey, limit: 1 }))?.[0];
  const policy = parseRelayPolicyStateEvent(event, { servicePubkey });
  if (!policy) return { truth_state: 'loading', canonical_policy: null };
  return { truth_state: 'loaded-cached', canonical_policy: policy, server_projection: {
    event_id: event.id, event_created_at: new Date(event.created_at * 1000).toISOString(),
    freshness: 'cached', source: 'canonical_event_store'
  } };
}

export async function applyRelayPolicy({ policy, expectedProjection = null, replacementConfirmation = null } = {}) {
  const payload = buildRelayPolicyPayload(policy);
  if (expectedProjection) {
    payload.expected_projection = {
      availability: String(expectedProjection.availability || '').trim(),
      event_id: String(expectedProjection.event_id || '').trim(),
      hash: String(expectedProjection.hash || '').trim()
    };
  }
  if (replacementConfirmation) {
    payload.replacement_confirmation = {
      confirmed: replacementConfirmation.confirmed === true,
      previous_truth_state: String(replacementConfirmation.previous_truth_state || '').trim(),
      reason_code: String(replacementConfirmation.reason_code || '').trim(),
      change_reference: String(replacementConfirmation.change_reference || '').trim()
    };
  }
  return submitSensitiveIntent(relayPolicyIntent(payload, null, null, FLEET_INTENT_ORG_ID));
}

/**
 * Guard that deduplicates async projection hydration calls per key.
 *
 * Once acquired for a key, subsequent acquire() calls with the same key
 * return false — whether the original call is still in flight or already
 * completed.  On error, release() re-opens the key for retry.  reset()
 * clears the guard entirely (e.g. on identity/key change).
 */
export function createProjectionHydrationGuard() {
  let activeKey = '';
  return {
    /** Returns true and locks the key if hydration should proceed; false if already acquired. */
    acquire(key) {
      if (!key || activeKey === key) return false;
      activeKey = key;
      return true;
    },
    /** Releases the guard for the given key (call on error to allow retry). */
    release(key) {
      if (activeKey === key) activeKey = '';
    },
    /** Clears the guard entirely (call on identity/key change). */
    reset() {
      activeKey = '';
    },
    /** The currently guarded key (empty string when idle). */
    get currentKey() {
      return activeKey;
    }
  };
}
