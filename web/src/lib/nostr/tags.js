export function getTagValues(event, name) {
  if (!event || !Array.isArray(event.tags)) return [];
  return event.tags
    .filter(tag => Array.isArray(tag) && tag[0] === name && tag[1])
    .map(tag => tag[1]);
}

export function getTagValue(event, name, fallback = '') {
  const values = getTagValues(event, name);
  return values.length > 0 ? values[values.length - 1] : fallback;
}

export function getDTag(event) {
  return getTagValue(event, 'd', '');
}

export function eventCoordinate(event) {
  const d = getDTag(event);
  if (!event?.kind || !event?.pubkey || !d) return '';
  return `${event.kind}:${event.pubkey}:${d}`;
}
