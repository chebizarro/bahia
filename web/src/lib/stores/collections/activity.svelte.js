import { BAHIA_AUDIT_KINDS, BAHIA_STATUS_KINDS, CP_AUDIT_TAG_FACT, CP_AUDIT_TAG_STATE } from '../../nostr/client.js';
import { CP_AUDIT_TOPIC, SBOM_REFERENCE, SBOM_AVAILABILITY_LIST, SBOM_REFERENCE_TOPIC, SBOM_AVAILABILITY_TOPIC } from '../../nostr/kinds.gen.js';
import { getEventStore, getServicePubkey, onStoreRefresh } from '../../nostr/boot.js';
import { getDTag, getTagValue, parseJsonContent, replaceArray } from './utils.js';

const MAX_ACTIVITY = 100;
export const events = $state([]);
let unsubscribe = null;

export function resetActivity() { events.length = 0; }

function activityType(event, content) {
  if (content.event_type) return content.event_type;
  const schema = getTagValue(event, 'schema', content.schema || '');
  const domain = getTagValue(event, 'domain', content.domain || '');
  const op = getTagValue(event, 'op', content.operation || content.op || '');
  if (schema.startsWith('bahia.status.')) return `${domain || 'controlplane'}.status`;
  if (schema.startsWith('bahia.result.')) return `${domain || 'controlplane'}.result`;
  if (schema === 'bahia.audit.v1') return content.type || content.action || 'controlplane.audit';
  if (op) return `${domain || 'controlplane'}.${op}`;
  if (BAHIA_STATUS_KINDS.includes(event.kind)) return `${domain || 'controlplane'}.status`;
  return `nostr.kind.${event.kind}`;
}

export function refreshActivity() {
  const store = getEventStore();
  if (!store) return;
  const author = getServicePubkey();
  const filter = { kinds: [...BAHIA_AUDIT_KINDS, ...BAHIA_STATUS_KINDS, SBOM_REFERENCE, SBOM_AVAILABILITY_LIST], ...(author ? { authors: [author] } : {}) };
  const seenFacts = new Set();
  const rows = [];
  for (const event of store.query(filter).sort((a, b) => b.created_at - a.created_at || a.id.localeCompare(b.id))) {
    const hasTopic = (topic) => event.tags?.some(([name, value]) => name === 't' && value === topic);
    if (BAHIA_AUDIT_KINDS.includes(event.kind) && !hasTopic(CP_AUDIT_TOPIC)) continue;
    if ((event.kind === SBOM_REFERENCE || event.kind === SBOM_AVAILABILITY_LIST) &&
      !hasTopic(event.kind === SBOM_REFERENCE ? SBOM_REFERENCE_TOPIC : SBOM_AVAILABILITY_TOPIC)) continue;
    if (getTagValue(event, 'deleted') === 'true') continue;
    const fact = getTagValue(event, CP_AUDIT_TAG_FACT);
    if (fact && seenFacts.has(fact)) continue;
    if (fact) seenFacts.add(fact);
    const content = parseJsonContent(event, {});
    rows.push({
      id: event.id, kind: event.kind, type: activityType(event, content),
      entity_id: content.entity_id || content.service_id || content.route_id || content.release_id ||
        ['service', 'route', 'release', 'environment', 'intent', 'run', 'artifact', CP_AUDIT_TAG_STATE]
          .map((tag) => getTagValue(event, tag)).find(Boolean) || getDTag(event) || event.id,
      data: content.data ?? content,
      time: new Date((event.created_at || 0) * 1000).toISOString(),
      pubkey: event.pubkey, fact, nostr_event: event
    });
    if (rows.length === MAX_ACTIVITY) break;
  }
  replaceArray(events, rows);
}

export function initActivityStoreBinding() { if (unsubscribe) return; refreshActivity(); unsubscribe = onStoreRefresh(refreshActivity); }
export function teardownActivityStoreBinding() { unsubscribe?.(); unsubscribe = null; }
