import { getEventStore, getServicePubkey } from '$lib/nostr/boot.js';
import { CAS_CONTROL_STATE, CAS_AUDIT, CP_STATE_TOPICS } from '$lib/nostr/kinds.gen.js';
import { getDTag, getTagValue, parseJsonContent } from '$lib/nostr/client.js';
import { readConfidentialTopic } from './collections/confidential-records.js';
import { BLOSSOM_ADMIN_RECORD, BLOSSOM_BLOB_RECORD } from '$lib/nostr/kinds.gen.js';

function scopedEvents(topic, kinds, store = getEventStore(), servicePubkey = getServicePubkey()) {
  if (!store) return [];
  return store.query({ kinds, '#t': [topic], ...(servicePubkey ? { authors: [servicePubkey] } : {}) });
}

function newestFirst(a, b) { return b.created_at - a.created_at || a.id.localeCompare(b.id); }
function sameTag(event, tag, value) { return getTagValue(event, tag) === String(value || ''); }
function isDeleted(event) { return getTagValue(event, 'deleted') === 'true'; }

export function instanceHealthRows(store, servicePubkey) {
  const states = scopedEvents(CP_STATE_TOPICS.MANAGED_INSTANCE_HEALTH, [CAS_CONTROL_STATE], store, servicePubkey);
  const latest = new Map();
  for (const event of states) {
    if (isDeleted(event)) continue;
    const coordinate = getDTag(event);
    if (!coordinate || (latest.has(coordinate) && newestFirst(latest.get(coordinate).event, event) <= 0)) continue;
    const content = parseJsonContent(event, {});
    if (content.health) latest.set(coordinate, { event, content });
  }
  const audits = scopedEvents(CP_STATE_TOPICS.MANAGED_INSTANCE_HEALTH, [CAS_AUDIT], store, servicePubkey);
  return [...latest.values()].map(({ content }) => {
    const health = content.health;
    const maintenance = audits.filter(e => sameTag(e, 'service', health.service_id) && sameTag(e, 'environment', health.environment_id)
      && sameTag(e, 'deployment_unit', health.deployment_unit_id) && sameTag(e, 'target', health.runtime_target_name))
      .map(e => ({ event: e, content: parseJsonContent(e, {}) }))
      .filter(x => x.content.type === 'maintenance_enabled' || x.content.type === 'maintenance_cleared')
      .sort((a, b) => newestFirst(a.event, b.event))[0];
    return { ...health, maintenance_override: maintenance ? (maintenance.content.active ? maintenance.content.override : null) : (content.maintenance_override || null) };
  }).sort((a, b) => String(a.runtime_target_name).localeCompare(String(b.runtime_target_name)));
}

export function instanceHealthDetail(row, store, servicePubkey) {
  const audits = scopedEvents(CP_STATE_TOPICS.MANAGED_INSTANCE_HEALTH, [CAS_AUDIT], store, servicePubkey)
    .filter(e => sameTag(e, 'service', row.service_id) && sameTag(e, 'environment', row.environment_id)
      && sameTag(e, 'deployment_unit', row.deployment_unit_id) && sameTag(e, 'target', row.runtime_target_name))
    .sort(newestFirst).slice(0, 100);
  const events = [], attemptsByID = new Map();
  for (const event of audits) {
    const content = parseJsonContent(event, {});
    if (content.type === 'health_transition' || content.type === 'health_observation') events.push({ status: content.status, observed_at: content.observed_at, reason: content.reason });
    if (content.attempt && typeof content.attempt === 'object') {
      const key = content.attempt.id || content.attempt.correlation_id || event.id;
      if (!attemptsByID.has(key)) attemptsByID.set(key, content.attempt);
    }
  }
  return { detail: { health: row, maintenance_override: row.maintenance_override || null }, events: events.slice(0, 50), attempts: [...attemptsByID.values()].slice(0, 50) };
}

export function routeCanaryRows(store, servicePubkey) {
  const latest = new Map();
  for (const event of scopedEvents(CP_STATE_TOPICS.ROUTE_CANARY, [CAS_CONTROL_STATE], store, servicePubkey)) {
    if (isDeleted(event)) continue;
    const d = getDTag(event);
    if (!d || (latest.has(d) && newestFirst(latest.get(d), event) <= 0)) continue;
    latest.set(d, event);
  }
  return [...latest.values()].map(event => {
    const content = parseJsonContent(event, {});
    return { ...content.route_canary, observed_instance_status: content.observed_instance_status,
      service_healthy_route_broken: content.service_healthy_route_broken };
  }).filter(row => row.service_id && row.hostname).sort((a, b) => String(a.hostname).localeCompare(String(b.hostname)));
}

export function routeCanaryEvents(row, store, servicePubkey) {
  const same = event => sameTag(event, 'service', row.service_id) && sameTag(event, 'environment', row.environment_id)
    && sameTag(event, 'hostname', row.hostname) && sameTag(event, 'deployment_unit', row.deployment_unit_id);
  return scopedEvents(CP_STATE_TOPICS.ROUTE_CANARY, [CAS_AUDIT], store, servicePubkey).filter(same).sort(newestFirst).slice(0, 50)
    .map(event => { const content = parseJsonContent(event, {}); return {
      transition: content.transition, classification: content.classification,
      observed_at: content.occurred_at, reason: content.reason, evidence: content.evidence
    }; });
}

export function soulRuntimePolicy(store, servicePubkey) {
  const event = scopedEvents(CP_STATE_TOPICS.SOUL_RUNTIME_POLICY, [CAS_CONTROL_STATE], store, servicePubkey)
    .filter(e => !isDeleted(e)).sort(newestFirst)[0];
  if (!event) return null;
  const content = parseJsonContent(event, {});
  return Array.isArray(content.agent_runtimes) ? content.agent_runtimes : null;
}

export function blossomAdminSnapshot() {
  const admin = readConfidentialTopic(CP_STATE_TOPICS.BLOSSOM_ADMIN, BLOSSOM_ADMIN_RECORD);
  const blobs = readConfidentialTopic(CP_STATE_TOPICS.BLOSSOM_BLOB, BLOSSOM_BLOB_RECORD);
  return { admin: admin.rows.sort((a, b) => b.nostr_created_at - a.nostr_created_at)[0] || null,
    blobs: blobs.rows, unreadable: admin.unreadable + blobs.unreadable };
}
