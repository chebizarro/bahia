import { CP_STATE_TOPICS } from '../../nostr/kinds.gen.js';
import { onStoreRefresh } from '../../nostr/boot.js';
import { projectTopic } from './store-query.js';
import { replaceArray, sortByNameOrId, sortByNewestField } from './utils.js';

export const mlModels = $state([]);
export const mlModelVersions = $state([]);
export const mlEndpoints = $state([]);
export const mlEndpointStates = $state([]);
let unsubscribe = null;

export function resetML() {
  for (const rows of [mlModels, mlModelVersions, mlEndpoints, mlEndpointStates]) rows.length = 0;
}

export function refreshML() {
  replaceArray(mlModels, projectTopic(CP_STATE_TOPICS.ML_MODEL, ['id', 'slug']).sort(sortByNameOrId));
  replaceArray(mlModelVersions, projectTopic(CP_STATE_TOPICS.ML_MODEL_VERSION, ['id', 'version_id']).sort(sortByNewestField(['created_at'])));
  replaceArray(mlEndpoints, projectTopic(CP_STATE_TOPICS.ML_ENDPOINT, ['id', 'endpoint_id']).sort(sortByNameOrId));
  replaceArray(mlEndpointStates, projectTopic(CP_STATE_TOPICS.ML_ENDPOINT_STATE, ['id', 'endpoint_id']).sort(sortByNameOrId));
}

export function initMLStoreBinding() { if (unsubscribe) return; refreshML(); unsubscribe = onStoreRefresh(refreshML); }
export function teardownMLStoreBinding() { unsubscribe?.(); unsubscribe = null; }
