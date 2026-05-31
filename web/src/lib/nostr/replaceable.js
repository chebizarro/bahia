import { parseJsonContent } from './content.js';
import { getDTag, getTagValue } from './tags.js';

export function replaceableKey(event) {
  if (!event || !event.kind || !event.pubkey) return '';
  const d = getDTag(event);
  return d ? `${event.kind}:${event.pubkey}:${d}` : `${event.kind}:${event.pubkey}`;
}

export function isReplaceableTombstone(event) {
  const content = parseJsonContent(event, {});
  if (content?.deleted === true) return true;
  return getTagValue(event, 'deleted') === 'true';
}

export function shouldAcceptReplaceableEvent(existing, incoming) {
  if (!incoming?.id) return false;
  if (!existing) return true;
  if (existing.id === incoming.id) return false;
  const incomingCreated = Number(incoming.created_at || 0);
  const existingCreated = Number(existing.created_at || 0);
  if (incomingCreated > existingCreated) return true;
  if (incomingCreated < existingCreated) return false;
  return String(incoming.id) > String(existing.id);
}

export function upsertReplaceableEvent(map, event) {
  const key = replaceableKey(event);
  if (!key) return { accepted: false, key: '', deleted: false };
  const existing = map.get(key);
  if (!shouldAcceptReplaceableEvent(existing, event)) {
    return { accepted: false, key, deleted: false };
  }
  if (isReplaceableTombstone(event)) {
    map.set(key, event);
    return { accepted: true, key, deleted: true };
  }
  map.set(key, event);
  return { accepted: true, key, deleted: false };
}

export function dedupeReplaceableEvents(events = []) {
  const map = new Map();
  for (const event of events || []) {
    upsertReplaceableEvent(map, event);
  }
  return Array.from(map.values())
    .filter((event) => !isReplaceableTombstone(event))
    .sort((a, b) => Number(b.created_at || 0) - Number(a.created_at || 0));
}
