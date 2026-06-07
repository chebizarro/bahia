import { requestEncryptedResult } from './encrypted-controlplane.js';

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
    schema: 'bahia.relay-settings.v1',
    browser_relays: normalizeRelayArray(policy.browser_relays),
    contextvm_relays: normalizeRelayArray(policy.contextvm_relays),
    service_relays: normalizeRelayArray(policy.service_relays),
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
