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
export const workerCleanupExecutions = $state([]);

const workerMap = new Map();
const workerAssignmentMap = new Map();
const workerDrainStatusMap = new Map();
const workerEligibilityPreviewMap = new Map();
const workerCleanupExecutionMap = new Map();

export function resetWorkers() {
  [workerMap, workerAssignmentMap, workerDrainStatusMap, workerEligibilityPreviewMap, workerCleanupExecutionMap]
    .forEach((map) => map.clear());
  [workers, workerAssignments, workerDrainStatuses, workerEligibilityPreviews, workerCleanupExecutions]
    .forEach((array) => { array.length = 0; });
}

export function refreshWorkers() {
  replaceArray(workers, Array.from(workerMap.values()).sort(sortByNameOrId));
  replaceArray(workerAssignments, Array.from(workerAssignmentMap.values()).sort(sortByNameOrId));
  replaceArray(workerDrainStatuses, Array.from(workerDrainStatusMap.values()).sort(sortByNameOrId));
  replaceArray(workerEligibilityPreviews, Array.from(workerEligibilityPreviewMap.values()).sort(sortByNewestField(['updated_at', 'nostr_created_at'])));
  replaceArray(workerCleanupExecutions, Array.from(workerCleanupExecutionMap.values()).sort(sortByNewestField(['updated_at', 'completed_at', 'started_at', 'nostr_created_at'])));
}

export function hasWorkerReadModelTag(event) {
  return Boolean(getTagValue(event, 'worker'));
}

export function hasWorkerEligibilityPreviewShape(event) {
  const content = parseJsonContent(event, {});
  return Boolean(content.preview_id || getTagValue(event, 'preview'));
}

/**
 * Normalize resource/accelerator fields from legacy PascalCase (Go-style) to
 * canonical snake_case, and round decimal GB values to integers.
 */
function normalizeResources(resources) {
  if (!resources || typeof resources !== 'object') return resources;
  return {
    cpu_cores: resources.cpu_cores ?? resources.CPUCores ?? undefined,
    memory_gb: roundIfNumber(resources.memory_gb ?? resources.MemoryGB),
    disk_gb: roundIfNumber(resources.disk_gb ?? resources.DiskGB)
  };
}

function normalizeAccelerators(accelerators) {
  if (!Array.isArray(accelerators)) return accelerators;
  return accelerators.map((a) => ({
    vendor: a.vendor ?? a.Vendor ?? undefined,
    model: a.model ?? a.Model ?? undefined,
    count: a.count ?? a.Count ?? undefined,
    memory_gb: roundIfNumber(a.memory_gb ?? a.MemoryGB),
    driver: a.driver ?? a.Driver ?? undefined
  }));
}

function roundIfNumber(value) {
  const n = Number(value);
  return Number.isFinite(n) ? Math.round(n) : undefined;
}

function normalizeAdvertisementContent(content) {
  const normalized = { ...content };
  if (normalized.resources) normalized.resources = normalizeResources(normalized.resources);
  if (normalized.accelerators) normalized.accelerators = normalizeAccelerators(normalized.accelerators);
  return normalized;
}

export function applyWorkerEvent(event, replaceableEvents) {
  const { accepted } = upsertReplaceableEvent(replaceableEvents, event);
  if (!accepted) return false;

  const content = normalizeAdvertisementContent(contentWithEventMeta(event));
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
  eligibilityPreview: (event, replaceableEvents) => applyProjectedEntity(event, workerEligibilityPreviewMap, replaceableEvents, ['preview_id']),
  cleanupExecution: (event, replaceableEvents) => applyProjectedEntity(event, workerCleanupExecutionMap, replaceableEvents, ['cleanup_id', 'idempotency_key', 'loom_job_id', 'worker_pubkey'])
};
