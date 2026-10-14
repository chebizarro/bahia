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
