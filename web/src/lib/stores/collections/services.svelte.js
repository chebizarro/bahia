import { applySimpleReplaceable, getDTag, replaceArray, sortByNameOrId } from './utils.js';

export const services = $state([]);
export const serviceMap = new Map();

export function refreshServices() {
  replaceArray(services, Array.from(serviceMap.values()).sort(sortByNameOrId));
}

export function resetServices() {
  serviceMap.clear();
  services.length = 0;
}

export function applyServiceEvent(event, replaceableEvents) {
  return applySimpleReplaceable(
    event,
    serviceMap,
    replaceableEvents,
    (content, relayEvent) => content.id || getDTag(relayEvent)
  );
}

export function upsertServiceProjection(service) {
  const id = service?.id;
  if (!id) return;

  if (service.deleted) {
    serviceMap.delete(id);
  } else {
    serviceMap.set(id, { ...(serviceMap.get(id) || {}), ...service, id });
  }

  refreshServices();
}
