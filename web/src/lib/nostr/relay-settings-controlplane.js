import { requestEncryptedResult } from './encrypted-controlplane.js';
import { CASCADIA_CONTROLPLANE_STATE } from './kinds.gen.js';
import { parseJsonContent } from './content.js';
import { getDTag, getTagValue } from './tags.js';
import { nostr as defaultNostr } from './subscriptions.js';

export const RELAY_SETTINGS_SCHEMA = 'bahia.relay-settings.v1';
export const RELAY_SETTINGS_DOMAIN = 'relay-settings';
export const RELAY_SETTINGS_DTAG = 'relay-settings:operator';

export const RELAY_SETTINGS_OPERATIONS = {
  GET: 'settings/relay-policy.get',
  APPLY: 'settings/relay-policy.apply',
  ADMIN_CALL: 'settings/relay-admin.call'
};

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

export function relayPolicyReadModelFilter({ servicePubkey, since, limit = 10 } = {}) {
  const filter = {
    kinds: [CASCADIA_CONTROLPLANE_STATE],
    '#d': [RELAY_SETTINGS_DTAG],
    '#domain': [RELAY_SETTINGS_DOMAIN],
    '#schema': [RELAY_SETTINGS_SCHEMA],
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

export async function getRelayPolicy({ signal } = {}) {
  return requestEncryptedResult({
    operation: RELAY_SETTINGS_OPERATIONS.GET,
    payload: {},
    tags: [['domain', 'relay-settings'], ['action', 'relay_policy_get']],
    signal
  });
}

export async function applyRelayPolicy({ policy, signal } = {}) {
  return requestEncryptedResult({
    operation: RELAY_SETTINGS_OPERATIONS.APPLY,
    payload: buildRelayPolicyPayload(policy),
    tags: [['domain', 'relay-settings'], ['action', 'relay_policy_apply']],
    signal
  });
}

export async function callRelayAdmin({ targetRef, method, params = [], signal } = {}) {
  return requestEncryptedResult({
    operation: RELAY_SETTINGS_OPERATIONS.ADMIN_CALL,
    payload: {
      target_ref: String(targetRef || '').trim(),
      method: String(method || '').trim(),
      params: Array.isArray(params) ? params : []
    },
    tags: [['domain', 'relay-settings'], ['action', 'relay_admin_call'], ['target', String(targetRef || '').trim()]],
    signal
  });
}
