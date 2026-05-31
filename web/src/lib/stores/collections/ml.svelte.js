import { applyProjectedEntity, replaceArray, sortByNameOrId, sortByNewestField } from './utils.js';

export const mlModels = $state([]);
export const mlModelVersions = $state([]);
export const mlEndpoints = $state([]);
export const mlEndpointStates = $state([]);

const mlModelMap = new Map();
const mlModelVersionMap = new Map();
const mlEndpointMap = new Map();
const mlEndpointStateMap = new Map();

export function resetML() {
  [mlModelMap, mlModelVersionMap, mlEndpointMap, mlEndpointStateMap]
    .forEach((map) => map.clear());
  [mlModels, mlModelVersions, mlEndpoints, mlEndpointStates]
    .forEach((array) => { array.length = 0; });
}

export function refreshML() {
  replaceArray(mlModels, Array.from(mlModelMap.values()).sort(sortByNameOrId));
  replaceArray(mlModelVersions, Array.from(mlModelVersionMap.values()).sort(sortByNewestField(['created_at'])));
  replaceArray(mlEndpoints, Array.from(mlEndpointMap.values()).sort(sortByNameOrId));
  replaceArray(mlEndpointStates, Array.from(mlEndpointStateMap.values()).sort(sortByNameOrId));
}

export const mlApplicators = {
  model: (event, replaceableEvents) => applyProjectedEntity(event, mlModelMap, replaceableEvents, ['id', 'slug']),
  modelVersion: (event, replaceableEvents) => applyProjectedEntity(event, mlModelVersionMap, replaceableEvents, ['id', 'version_id']),
  endpoint: (event, replaceableEvents) => applyProjectedEntity(event, mlEndpointMap, replaceableEvents, ['id', 'endpoint_id']),
  endpointState: (event, replaceableEvents) => applyProjectedEntity(event, mlEndpointStateMap, replaceableEvents, ['id', 'endpoint_id'])
};
