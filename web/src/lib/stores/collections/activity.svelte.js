import { BAHIA_STATUS_KINDS, CP_AUDIT_TAG_FACT, CP_AUDIT_TAG_STATE } from '../../nostr/client.js';
import { getDTag, getTagValue, parseJsonContent, replaceArray } from './utils.js';

const MAX_ACTIVITY = 100;

export const events = $state([]);
const activityMap = new Map();
// Audit facts carry a deterministic fact id; a republished fact is a new
// event id for the same fact, so it is recorded once.
const seenFacts = new Set();

export function resetActivity() {
  activityMap.clear();
  seenFacts.clear();
  events.length = 0;
}

export function refreshActivity() {
  replaceArray(
    events,
    Array.from(activityMap.values())
      .sort((a, b) => String(b.time || '').localeCompare(String(a.time || '')))
      .slice(0, MAX_ACTIVITY)
  );
}

function eventSchema(event, content) {
  return getTagValue(event, 'schema', content.schema || '');
}

function eventDomain(event, content) {
  return getTagValue(event, 'domain', content.domain || '');
}

function activityType(event, content) {
  if (content.event_type) return content.event_type;
  const schema = eventSchema(event, content);
  const domain = eventDomain(event, content);
  const op = getTagValue(event, 'op', content.operation || content.op || '');
  if (schema.startsWith('bahia.status.')) return `${domain || 'controlplane'}.status`;
  if (schema.startsWith('bahia.result.')) return `${domain || 'controlplane'}.result`;
  if (schema === 'bahia.audit.v1') return content.type || content.action || 'controlplane.audit';
  if (op) return `${domain || 'controlplane'}.${op}`;
  if (BAHIA_STATUS_KINDS.includes(event.kind)) return `${domain || 'controlplane'}.status`;
  return `nostr.kind.${event.kind}`;
}

function activityEntityId(event, content) {
  return content.entity_id || content.service_id || content.route_id || content.release_id ||
    getTagValue(event, 'service') || getTagValue(event, 'route') || getTagValue(event, 'release') ||
    getTagValue(event, 'environment') || getTagValue(event, 'intent') || getTagValue(event, 'run') ||
    getTagValue(event, 'artifact') || getTagValue(event, CP_AUDIT_TAG_STATE) || getDTag(event) || event.id;
}

export function applyActivityEvent(event) {
  if (!event?.id || activityMap.has(event.id)) return false;
  const fact = getTagValue(event, CP_AUDIT_TAG_FACT);
  if (fact) {
    if (seenFacts.has(fact)) return false;
    seenFacts.add(fact);
  }
  const content = parseJsonContent(event, {});
  const time = new Date((event.created_at || 0) * 1000).toISOString();
  activityMap.set(event.id, {
    id: event.id,
    kind: event.kind,
    type: activityType(event, content),
    entity_id: activityEntityId(event, content),
    data: content.data ?? content,
    time,
    pubkey: event.pubkey,
    fact,
    nostr_event: event
  });

  if (activityMap.size > MAX_ACTIVITY * 2) {
    const trimmed = Array.from(activityMap.values())
      .sort((a, b) => String(b.time || '').localeCompare(String(a.time || '')))
      .slice(0, MAX_ACTIVITY);
    activityMap.clear();
    seenFacts.clear();
    for (const activity of trimmed) {
      activityMap.set(activity.id, activity);
      if (activity.fact) seenFacts.add(activity.fact);
    }
  }
  return true;
}
