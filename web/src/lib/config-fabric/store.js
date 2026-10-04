import { getEventStore, getServicePubkey } from '$lib/nostr/boot.js';
import { CAS_CONTROL_STATE, CONFIG_ACL_LIST, CONFIG_POLICY } from '$lib/nostr/kinds.gen.js';
import { getDTag, getTagValue, parseJsonContent } from '$lib/nostr/client.js';

const SCHEMA = /^cascadia\.config\.([a-z0-9][a-z0-9._-]*)\.v1$/;
const HEX = /^[0-9a-f]{64}$/i;
const keyOf = (service, policy, scope) => `${service}~${policy}~${scope}`;
const time = event => new Date(event.created_at * 1000).toISOString();

export function configFabricDrift(events = null) {
  if (!events) {
    const store = getEventStore();
    if (!store) return [];
    const service = getServicePubkey();
    events = [...store.query({ kinds: [CONFIG_ACL_LIST, CONFIG_POLICY], '#t': ['config-fabric'] }),
      ...store.query({ kinds: [CAS_CONTROL_STATE], '#t': ['config-status'], ...(service ? { authors: [service] } : {}) })];
  }
  const desired = new Map(), statuses = new Map();
  for (const event of events) {
    const content = parseJsonContent(event, {});
    if (event.kind === CONFIG_ACL_LIST || event.kind === CONFIG_POLICY) {
      const d = getDTag(event), service = String(content.service_id || ''), scope = String(content.scope || '');
      if (!d?.startsWith(`service:${service}:`) || !service || !scope) continue;
      const policy = d.slice(`service:${service}:`.length), version = Number(content.version);
      if (!SCHEMA.test(content.schema) || SCHEMA.exec(content.schema)[1] !== policy || !Number.isInteger(version) || version < 1
        || getTagValue(event, 'service') !== service || getTagValue(event, 'scope') !== scope
        || getTagValue(event, 'version') !== String(version) || getTagValue(event, 'schema') !== content.schema) continue;
      const items = event.kind === CONFIG_ACL_LIST ? event.tags.filter(t => ['p', 'a', 'r'].includes(t[0])).map(t => ({ tag: t[0], value: t[1] })) : undefined;
      const item = { event_id: event.id, pubkey: event.pubkey, kind: event.kind, version, schema: content.schema,
        created_at: time(event), policy: content.policy, secret_refs: content.secret_refs, items };
      const key = keyOf(service, policy, scope);
      if (!desired.has(key)) desired.set(key, []);
      desired.get(key).push(item);
    } else if (event.kind === CAS_CONTROL_STATE && getTagValue(event, 'domain') === 'config-status') {
      const schema = getTagValue(event, 'schema');
      if (!['cascadia.config.status.v1', 'cascadia.config.status.v2', 'cascadia.config.status.v3'].includes(schema)) continue;
      const policy = SCHEMA.exec(content.policy_schema)?.[1], service = String(content.service_id || ''), scope = String(content.scope || '');
      if (!policy || !service || !scope || !HEX.test(content.config_event_id || '') ||
        getTagValue(event, 'service') !== service || getTagValue(event, 'scope') !== scope ||
        getTagValue(event, 'status') !== content.status || getTagValue(event, 'version') !== String(content.version) ||
        getTagValue(event, 'e') !== content.config_event_id) continue;
      const key = keyOf(service, policy, scope);
      if (!statuses.has(key)) statuses.set(key, []);
      statuses.get(key).push({ event_id: event.id, pubkey: event.pubkey, config_event_id: content.config_event_id,
        version: content.version, status: content.status, effective_version: content.effective_version || 0,
        last_applied_event_id: content.last_applied_event_id || '', reason: content.reason || '', created_at: time(event) });
    }
  }
  return [...desired.entries()].map(([key, versions]) => {
    const [service_id, policy_name, scope] = key.split('~');
    versions.sort((a, b) => b.version - a.version || String(b.created_at).localeCompare(String(a.created_at)));
    const desiredVersion = versions[0], history = (statuses.get(key) || []).sort((a, b) => String(b.created_at).localeCompare(String(a.created_at)));
    let applied_version = 0, applied_event_id = '', last_rejection_reason = '', withdrawn = false, withdrawn_reason = '';
    for (const item of history) {
      if (item.effective_version > applied_version && HEX.test(item.last_applied_event_id)) {
        applied_version = item.effective_version; applied_event_id = item.last_applied_event_id;
      }
      if (!last_rejection_reason && item.status === 'rejected') last_rejection_reason = item.reason;
      if (!withdrawn && item.status === 'withdrawn' && item.config_event_id === desiredVersion.event_id) {
        withdrawn = true; withdrawn_reason = item.reason;
      }
    }
    return { service_id, policy_name, scope, desired_event_id: desiredVersion.event_id,
      desired_version: desiredVersion.version, applied_event_id, applied_version,
      drift: applied_event_id !== desiredVersion.event_id || applied_version !== desiredVersion.version,
      last_rejection_reason, withdrawn, withdrawn_reason,
      desired: desiredVersion, effective: versions.find(v => v.event_id === applied_event_id) || null,
      versions, status_history: history };
  }).sort((a, b) => keyOf(a.service_id, a.policy_name, a.scope).localeCompare(keyOf(b.service_id, b.policy_name, b.scope)));
}
