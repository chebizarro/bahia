import {
  getDTag,
  getTagValue,
  isReplaceableTombstone,
  parseJsonContent,
  upsertReplaceableEvent
} from '../../nostr/client.js';

export { getDTag, getTagValue, isReplaceableTombstone, parseJsonContent };

export function replaceArray(target, values) {
  target.length = 0;
  target.push(...values);
}

export function sortByNameOrId(a, b) {
  const left = String(a.name || a.id || a.pubkey || '');
  const right = String(b.name || b.id || b.pubkey || '');
  return left.localeCompare(right);
}

export function sortByNewestField(fields) {
  return (a, b) => {
    const pick = (item) => fields.map((field) => item?.[field]).find(Boolean) || '';
    return String(pick(b)).localeCompare(String(pick(a)));
  };
}

export function contentWithEventMeta(event) {
  const content = parseJsonContent(event, {});
  return {
    ...content,
    nostr_event_id: event.id,
    nostr_pubkey: event.pubkey,
    nostr_created_at: event.created_at
  };
}

export function projectionVersion(content, event) {
  const updatedAt = Date.parse(content?.updated_at || content?.observed_at || content?.created_at || '');
  return {
    domainTime: Number.isFinite(updatedAt) ? updatedAt : 0,
    relayTime: Number(event?.created_at || 0),
    eventId: String(event?.id || '')
  };
}

export function compareProjectionVersions(left, right) {
  if (!right) return 1;
  if (left.domainTime !== right.domainTime) return left.domainTime > right.domainTime ? 1 : -1;
  if (left.relayTime !== right.relayTime) return left.relayTime > right.relayTime ? 1 : -1;
  return left.eventId === right.eventId ? 0 : (left.eventId > right.eventId ? 1 : -1);
}

export function applyProjectedEntity(event, targetMap, replaceableEvents, idKeys = ['id'], watermarks = null) {
  const content = contentWithEventMeta(event);
  let id = getDTag(event);
  for (const key of idKeys) {
    if (content[key]) {
      id = content[key];
      break;
    }
  }
  if (!id) return false;

  const incomingVersion = projectionVersion(content, event);
  if (watermarks && compareProjectionVersions(incomingVersion, watermarks.get(id)) <= 0) {
    return false;
  }
  const { accepted } = upsertReplaceableEvent(replaceableEvents, event);
  // A corrected logical coordinate can coexist with a legacy d-tag. Domain
  // updated_at is authoritative across those replaceable coordinates.
  if (!accepted && !watermarks) return false;

  if (watermarks) watermarks.set(id, incomingVersion);
  if (isReplaceableTombstone(event) || content.deleted === true) {
    targetMap.delete(id);
  } else {
    targetMap.set(id, { ...content, id });
  }
  return true;
}

export function applySimpleReplaceable(event, targetMap, replaceableEvents, idFromContent) {
  const { accepted } = upsertReplaceableEvent(replaceableEvents, event);
  if (!accepted) return false;

  const content = contentWithEventMeta(event);
  const id = idFromContent(content, event);
  if (!id) return false;

  if (isReplaceableTombstone(event)) {
    targetMap.delete(id);
  } else {
    targetMap.set(id, { ...content, id });
  }
  return true;
}
