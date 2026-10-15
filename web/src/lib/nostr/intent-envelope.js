export const INTENT_TOPIC = 'bahia-intent';
export const INTENT_STATUS_TOPIC = 'intent-status';

const PRIVATE_REJECTION_DOMAINS = new Set(['backup', 'dns', 'worker', 'loom', 'notification', 'sbom', 'security', 'relay']);

export function intentStatusCoordinate(requester, coordinate) {
  return `intent-status:${requester}:${coordinate}`;
}

export function intentPrivateStatusCoordinate(requester, domain, correlation) {
  return `intent-status:${requester}:${domain}:${correlation}`;
}

export function intentStatusCoordinates(requester, { domain, coordinate, intentId }) {
  const legacy = intentStatusCoordinate(requester, coordinate);
  if (domain === 'secret' || PRIVATE_REJECTION_DOMAINS.has(domain)) {
    return [intentPrivateStatusCoordinate(requester, domain, intentId), legacy];
  }
  return [legacy];
}

/** Match the signed status envelope to one request; backup-run outcomes require
 * the exact signed request event reference as well as the public d shape. */
export function intentStatusMatches(event, requester, request) {
  const tag = name => event?.tags?.find(item => item[0] === name)?.[1];
  if (event?.kind !== 30315 || tag('p') !== requester || tag('intent_id') !== request.intentId ||
      !intentStatusCoordinates(requester, request).includes(tag('d'))) return false;
  if (request.domain !== 'backup' || !request.coordinate?.startsWith('backup-run:')) return true;
  if (!request.eventId || tag('e') !== request.eventId) return false;
  const status = tag('status');
  let body;
  try { body = JSON.parse(event.content || '{}'); } catch { return false; }
  if (!body || typeof body !== 'object' || Array.isArray(body) || body.intent_id !== request.intentId) return false;
  if (status === 'rejected' || status === 'conflict') {
    return tag('d') === intentPrivateStatusCoordinate(requester, 'backup', request.intentId) &&
      !Object.hasOwn(body, 'coordinate');
  }
  return status === 'accepted' && tag('d') === intentStatusCoordinate(requester, request.coordinate) &&
    body.coordinate === request.coordinate;
}
