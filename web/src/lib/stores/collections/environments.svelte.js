import { applySimpleReplaceable, getDTag, replaceArray, sortByNameOrId } from './utils.js';

export const environments = $state([]);
export const environmentMap = new Map();

export function refreshEnvironments() {
  replaceArray(environments, Array.from(environmentMap.values()).sort(sortByNameOrId));
}

export function resetEnvironments() {
  environmentMap.clear();
  environments.length = 0;
}

export function applyEnvironmentEvent(event, replaceableEvents) {
  return applySimpleReplaceable(
    event,
    environmentMap,
    replaceableEvents,
    (content, relayEvent) => content.id || getDTag(relayEvent)
  );
}
