import {
  applyProjectedEntity,
  contentWithEventMeta,
  getDTag,
  getTagValue,
  isReplaceableTombstone,
  parseJsonContent,
  replaceArray,
  sortByNameOrId,
  sortByNewestField
} from './utils.js';
import { upsertReplaceableEvent } from '../../nostr/client.js';

export const workers = $state([]);
export const workerAssignments = $state([]);
export const workerDrainStatuses = $state([]);
export const workerEligibilityPreviews = $state([]);

const workerMap = new Map();
const workerAssignmentMap = new Map();
const workerDrainStatusMap = new Map();
const workerEligibilityPreviewMap = new Map();

export function resetWorkers() {
  [workerMap, workerAssignmentMap, workerDrainStatusMap, workerEligibilityPreviewMap]
    .forEach((map) => map.clear());
  [workers, workerAssignments, workerDrainStatuses, workerEligibilityPreviews]
    .forEach((array) => { array.length = 0; });
}

export function refreshWorkers() {
  replaceArray(workers, Array.from(workerMap.values()).sort(sortByNameOrId));
  replaceArray(workerAssignments, Array.from(workerAssignmentMap.values()).sort(sortByNameOrId));
  replaceArray(workerDrainStatuses, Array.from(workerDrainStatusMap.values()).sort(sortByNameOrId));
  replaceArray(workerEligibilityPreviews, Array.from(workerEligibilityPreviewMap.values()).sort(sortByNewestField(['updated_at', 'nostr_created_at'])));
}

export function hasWorkerReadModelTag(event) {
  return Boolean(getTagValue(event, 'worker'));
}

export function hasWorkerEligibilityPreviewShape(event) {
  const content = parseJsonContent(event, {});
  return Boolean(content.preview_id || getTagValue(event, 'preview'));
}

export function applyWorkerEvent(event, replaceableEvents) {
  const { accepted } = upsertReplaceableEvent(replaceableEvents, event);
  if (!accepted) return false;

  const content = contentWithEventMeta(event);
  const pubkey = event.pubkey;
  if (!pubkey) return false;

  if (isReplaceableTombstone(event)) {
    workerMap.delete(pubkey);
  } else {
    workerMap.set(pubkey, {
      ...(workerMap.get(pubkey) || {}),
      ...content,
      pubkey,
      status: content.status || 'online',
      last_advertisement_at: new Date((event.created_at || 0) * 1000).toISOString()
    });
  }
  return true;
}

export function applyWorkerStateEvent(event, replaceableEvents) {
  const { accepted } = upsertReplaceableEvent(replaceableEvents, event);
  if (!accepted) return false;

  const content = contentWithEventMeta(event);
  const pubkey = content.worker_pubkey || getTagValue(event, 'worker') || getDTag(event);
  if (!pubkey) return false;

  if (isReplaceableTombstone(event) || content.deleted === true) {
    workerMap.delete(pubkey);
  } else {
    workerMap.set(pubkey, {
      ...(workerMap.get(pubkey) || {}),
      ...content,
      pubkey,
      status: content.status || workerMap.get(pubkey)?.status || 'online'
    });
  }
  return true;
}

export const workerApplicators = {
  assignment: (event, replaceableEvents) => applyProjectedEntity(event, workerAssignmentMap, replaceableEvents, ['worker_pubkey']),
  drainStatus: (event, replaceableEvents) => applyProjectedEntity(event, workerDrainStatusMap, replaceableEvents, ['worker_pubkey']),
  eligibilityPreview: (event, replaceableEvents) => applyProjectedEntity(event, workerEligibilityPreviewMap, replaceableEvents, ['preview_id'])
};
