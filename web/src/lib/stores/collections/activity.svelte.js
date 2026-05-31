import { KINDS, BAHIA_STATUS_KINDS } from '../../nostr/client.js';
import { getDTag, getTagValue, parseJsonContent, replaceArray } from './utils.js';

const MAX_ACTIVITY = 100;

export const events = $state([]);
const activityMap = new Map();

export function resetActivity() {
  activityMap.clear();
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

function activityType(event, content) {
  if (content.event_type) return content.event_type;
  if (event.kind === KINDS.BAHIA_LLM_DEPLOYMENT_STATUS) return 'llm_deployment.status';
  if (event.kind === KINDS.BAHIA_DEPLOYMENT_STATUS || event.kind === KINDS.BAHIA_SERVICE_STATUS) return 'controlplane.status';
  if (event.kind === KINDS.BAHIA_LLM_ROUTE_CREATE_RESULT ||
      event.kind === KINDS.BAHIA_LLM_RELEASE_REGISTER_RESULT ||
      event.kind === KINDS.BAHIA_LLM_DEPLOYMENT_RESULT) return 'llm_deployment.result';
  if (BAHIA_STATUS_KINDS.includes(event.kind)) return 'controlplane.result';
  return `nostr.kind.${event.kind}`;
}

function activityEntityId(event, content) {
  return content.entity_id || content.service_id || content.route_id || content.release_id ||
    getTagValue(event, 'service') || getTagValue(event, 'route') || getTagValue(event, 'release') ||
    getTagValue(event, 'environment') || getTagValue(event, 'intent') || getTagValue(event, 'run') ||
    getTagValue(event, 'artifact') || getDTag(event) || event.id;
}

export function applyActivityEvent(event) {
  if (!event?.id || activityMap.has(event.id)) return false;
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
    nostr_event: event
  });

  if (activityMap.size > MAX_ACTIVITY * 2) {
    const trimmed = Array.from(activityMap.values())
      .sort((a, b) => String(b.time || '').localeCompare(String(a.time || '')))
      .slice(0, MAX_ACTIVITY);
    activityMap.clear();
    for (const activity of trimmed) activityMap.set(activity.id, activity);
  }
  return true;
}
