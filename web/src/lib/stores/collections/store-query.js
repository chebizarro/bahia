import { getEventStore, getServicePubkey } from '../../nostr/boot.js';
import { CAS_CONTROL_STATE } from '../../nostr/kinds.gen.js';
import { getDTag, getTagValue, isReplaceableTombstone, parseJsonContent } from './utils.js';

export function queryTopic(topic, kinds = [CAS_CONTROL_STATE]) {
  const store = getEventStore();
  if (!store) return [];
  const servicePubkey = getServicePubkey();
  return store.query({ kinds, '#t': [topic], ...(servicePubkey ? { authors: [servicePubkey] } : {}) });
}

export function projectRecord(event, idKeys = ['id']) {
  if (isReplaceableTombstone(event)) return null;
  const content = parseJsonContent(event, {});
  if (content.deleted === true) return null;
  const id = idKeys.map((key) => content[key]).find(Boolean) || getDTag(event);
  if (!id) return null;
  return {
    ...content,
    id,
    nostr_event_id: event.id,
    nostr_pubkey: event.pubkey,
    nostr_created_at: event.created_at
  };
}

export function projectTopic(topic, idKeys) {
  return queryTopic(topic).map((event) => projectRecord(event, idKeys)).filter(Boolean);
}

export { getDTag, getTagValue, parseJsonContent };
