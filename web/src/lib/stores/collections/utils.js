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
  return left.eventId === right.eventId ? 0 : (left.eventId < right.eventId ? 1 : -1);
}

// Reduce NIP-01 winners first. Domain timestamps only order distinct relay
// coordinates that project onto the same logical entity (e.g. legacy d-tags).
export function selectProjectedEvent(event, replaceableEvents, id, watermarks) {
  const { accepted, key } = upsertReplaceableEvent(replaceableEvents, event);
  if (!accepted) return null;
  if (!watermarks) return event;
  const state = watermarks.get(id) || { candidates: new Map(), winner: null };
  const previousWinner = state.winner;
  state.candidates.set(key, event);
  watermarks.set(id, state);
  let winner = null;
  for (const candidate of state.candidates.values()) {
    if (!winner || compareProjectionVersions(
      projectionVersion(parseJsonContent(candidate, {}), candidate),
      projectionVersion(parseJsonContent(winner, {}), winner)
    ) > 0) winner = candidate;
  }
  state.winner = winner;
  return winner === previousWinner ? null : winner;
}

export function applyProjectedEntity(event, targetMap, replaceableEvents, idKeys = ['id'], watermarks = null) {
  let content = contentWithEventMeta(event);
  let id = getDTag(event);
  for (const key of idKeys) {
    if (content[key]) {
      id = content[key];
      break;
    }
  }
  if (!id) return false;

  const winner = selectProjectedEvent(event, replaceableEvents, id, watermarks);
  if (!winner) return false;
  event = winner;
  content = contentWithEventMeta(winner);
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
