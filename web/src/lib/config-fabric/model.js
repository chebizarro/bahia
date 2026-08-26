import { CONFIG_ACL_LIST, CONFIG_POLICY } from '$lib/nostr/kinds.gen.js';

export { CONFIG_ACL_LIST, CONFIG_POLICY };

const NAME_PATTERN = /^[a-z0-9][a-z0-9._-]*$/;
const SCHEMA_PATTERN = /^cascadia\.config\.([a-z0-9][a-z0-9._-]*)\.v1$/;
const SUSPICIOUS_KEY_PATTERN = /(^|_)(password|passwd|private_?key|secret|token|api_?key|credential)($|_)/i;
const HEX_64_PATTERN = /^[0-9a-fA-F]{64}$/;

export function configCoordinate(row = {}) {
  return [row.service_id || '', row.policy_name || '', row.scope || ''].join('~');
}

export function configFabricHref(row = {}) {
  return `/config-fabric/${encodeURIComponent(configCoordinate(row))}`;
}

export function findConfigCoordinate(rows = [], encodedCoordinate = '') {
  let coordinate;
  try {
    coordinate = decodeURIComponent(encodedCoordinate);
  } catch {
    return null;
  }
  return (rows || []).find((row) => configCoordinate(row) === coordinate) || null;
}

export function shortEventId(eventId = '') {
  return eventId ? `${eventId.slice(0, 8)}…${eventId.slice(-6)}` : 'Not applied';
}

export function configPayload(version) {
  if (!version) return null;
  const payload = version.kind === CONFIG_ACL_LIST
    ? { items: version.items || [] }
    : { policy: version.policy || {} };
  if (version.secret_refs && Object.keys(version.secret_refs).length > 0) {
    payload.secret_refs = version.secret_refs;
  }
  return payload;
}

export function initialConfigPublishForm(row = null) {
  const desired = row?.desired || null;
  return {
    kind: String(desired?.kind || CONFIG_POLICY),
    service_id: row?.service_id || '',
    policy_name: row?.policy_name || '',
    scope: row?.scope || 'prod',
    version: String((row?.desired_version || 0) + 1),
    schema: desired?.schema || '',
    policy: JSON.stringify(desired?.policy || {}, null, 2),
    secret_refs: JSON.stringify(desired?.secret_refs || {}, null, 2),
    items: JSON.stringify(desired?.items || [], null, 2)
  };
}

function parseJSON(raw, label) {
  try {
    return { value: JSON.parse(raw) };
  } catch {
    return { error: `${label} must be valid JSON` };
  }
}

function looksLikeSecretValue(value) {
  const trimmed = String(value || '').trim();
  const lower = trimmed.toLowerCase();
  return lower.startsWith('nsec1')
    || lower.startsWith('bearer ')
    || lower.startsWith('sk-')
    || trimmed.includes('-----BEGIN PRIVATE KEY-----')
    || trimmed.includes('-----BEGIN OPENSSH PRIVATE KEY-----');
}

function secretError(value, path) {
  if (Array.isArray(value)) {
    for (let index = 0; index < value.length; index += 1) {
      const error = secretError(value[index], `${path}[${index}]`);
      if (error) return error;
    }
    return '';
  }
  if (value && typeof value === 'object') {
    for (const [key, child] of Object.entries(value)) {
      const normalized = key.trim().toLowerCase();
      if (SUSPICIOUS_KEY_PATTERN.test(normalized)
        && !normalized.endsWith('_ref')
        && !normalized.endsWith('_file')
        && !normalized.endsWith('_path')) {
        return `${path}.${key} looks like a secret-bearing field; use secret_refs instead`;
      }
      const error = secretError(child, `${path}.${key}`);
      if (error) return error;
    }
    return '';
  }
  return typeof value === 'string' && looksLikeSecretValue(value)
    ? `${path} looks like a secret value; use secret_refs instead`
    : '';
}

function validScope(scope) {
  if (scope === 'prod' || scope === 'staging' || scope === 'fleet') return true;
  if (!scope.startsWith('host:')) return false;
  const host = scope.slice(5);
  return host !== '' && NAME_PATTERN.test(host.toLowerCase());
}

function validateListItems(items) {
  if (!Array.isArray(items) || items.length === 0) {
    return 'NIP-51 config list requires at least one item';
  }
  for (const item of items) {
    if (!item || typeof item !== 'object' || Array.isArray(item)) {
      return 'Each list item must be an object with tag and value';
    }
    if (!['p', 'a', 'r'].includes(item.tag)) {
      return 'List item tag must be one of p, a, or r';
    }
    if (typeof item.value !== 'string') {
      return 'List item value must be a string';
    }
    if (item.tag === 'p' && !HEX_64_PATTERN.test(item.value)) {
      return 'p list item must be a 64-character hex pubkey';
    }
    if (item.tag === 'a' && item.value.split(':').length < 3) {
      return 'a list item must be a Nostr address coordinate';
    }
    if (item.tag === 'r') {
      try {
        const url = new URL(item.value);
        if (!['http:', 'https:', 'ws:', 'wss:'].includes(url.protocol)) throw new Error('scheme');
      } catch {
        return 'r list item must be an http(s) or ws(s) URL';
      }
    }
  }
  return '';
}

function validateSecretRefs(refs) {
  if (!refs || typeof refs !== 'object' || Array.isArray(refs)) {
    return 'Secret references must be a JSON object';
  }
  for (const [name, entry] of Object.entries(refs)) {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) {
      return `secret_refs.${name} must be an object`;
    }
    if (!['signet', 'file', 'service'].includes(entry.provider)) {
      return `secret_refs.${name}.provider must be signet, file, or service`;
    }
    if (typeof entry.ref !== 'string' || entry.ref.trim() === '') {
      return `secret_refs.${name}.ref is required`;
    }
    if (Object.keys(entry).length !== 2 || !Object.hasOwn(entry, 'provider') || !Object.hasOwn(entry, 'ref')) {
      return `secret_refs.${name} accepts only provider and ref`;
    }
    if (looksLikeSecretValue(entry.ref)) {
      return `secret_refs.${name}.ref looks like a secret value`;
    }
  }
  return '';
}

export function validateConfigPublishForm(form = {}, driftRows = []) {
  const kind = Number(form.kind);
  const serviceID = String(form.service_id || '').trim();
  const policyName = String(form.policy_name || '').trim();
  const scope = String(form.scope || '').trim();
  const version = Number(form.version);
  const schema = String(form.schema || '').trim();

  if (kind !== CONFIG_ACL_LIST && kind !== CONFIG_POLICY) {
    return { success: false, error: `Kind must be ${CONFIG_ACL_LIST} (NIP-51 list) or ${CONFIG_POLICY} (NIP-78 policy)` };
  }
  if (!NAME_PATTERN.test(serviceID)) return { success: false, error: 'Service ID has invalid shape' };
  if (!NAME_PATTERN.test(policyName)) return { success: false, error: 'Policy name has invalid shape' };
  if (!validScope(scope)) return { success: false, error: 'Scope must be prod, staging, fleet, or host:<host>' };
  if (!Number.isInteger(version) || version < 1) return { success: false, error: 'Version must be a positive integer' };

  const current = (driftRows || []).find((row) =>
    row.service_id === serviceID && row.policy_name === policyName && row.scope === scope);
  if (current && version <= Number(current.desired_version || 0)) {
    return { success: false, error: `Version must advance monotonically; latest desired version is ${current.desired_version}` };
  }

  const schemaMatch = schema.match(SCHEMA_PATTERN);
  if (!schemaMatch || schemaMatch[1] !== policyName) {
    return { success: false, error: 'Schema must be cascadia.config.<policy_name>.v1' };
  }

  const refsResult = parseJSON(form.secret_refs || '{}', 'Secret references');
  if (refsResult.error) return { success: false, error: refsResult.error };
  const refsError = validateSecretRefs(refsResult.value);
  if (refsError) return { success: false, error: refsError };

  const payload = {
    kind,
    service_id: serviceID,
    policy_name: policyName,
    scope,
    version,
    schema
  };

  if (kind === CONFIG_ACL_LIST) {
    if (schema !== 'cascadia.config.membership.v1') {
      return { success: false, error: 'NIP-51 config lists require cascadia.config.membership.v1' };
    }
    const itemsResult = parseJSON(form.items || '[]', 'List items');
    if (itemsResult.error) return { success: false, error: itemsResult.error };
    const itemsError = validateListItems(itemsResult.value);
    if (itemsError) return { success: false, error: itemsError };
    payload.items = itemsResult.value.map(({ tag, value }) => ({ tag, value }));
  } else {
    const policyResult = parseJSON(form.policy || '{}', 'Policy');
    if (policyResult.error) return { success: false, error: policyResult.error };
    if (!policyResult.value || typeof policyResult.value !== 'object' || Array.isArray(policyResult.value)) {
      return { success: false, error: 'Policy must be a JSON object' };
    }
    const policySecretError = secretError(policyResult.value, 'policy');
    if (policySecretError) return { success: false, error: policySecretError };
    payload.policy = policyResult.value;
    if (Object.keys(refsResult.value).length > 0) payload.secret_refs = refsResult.value;
  }

  return { success: true, payload };
}
