import { getEventStore, getServicePubkey } from '../../nostr/boot.js';
import { CP_STATE_TOPICS } from '../../nostr/kinds.gen.js';
import { createCoreQuery, contentId } from './core-query.js';

export const services = $state([]);
const query = createCoreQuery({ topic: CP_STATE_TOPICS.SERVICE_REGISTRY, target: services, identity: event => contentId(event, 'id') });

export function initServiceStoreBinding() { query.bind(getEventStore(), getServicePubkey()); }
export function teardownServiceStoreBinding() { query.unbind(); }
export function resetServices() { query.reset(); }
export function refreshServices() { query.flush(); }

// Retained only for the Wave 3 ContextVM mutation acknowledgement path.
export function upsertServiceProjection(service) {
  const id = service?.id;
  if (!id) return;
  if (service.deleted) query.rows.delete(id);
  else query.rows.set(id, { ...(query.rows.get(id) || {}), ...service, id });
  query.flush();
}
