import { getEventStore, getPool, getRelayUrls, getServicePubkey } from '../../nostr/boot.js';
import { toWebSocketUrl } from '../../nostr/pool-utils.js';
import {
  CASCADIA_CONTROLPLANE_STATE,
  LOOM_WORKER_ADVERTISEMENT,
  LOOM_JOB_REQUEST,
  LOOM_JOB_STATUS_UPDATE,
  LOOM_JOB_RESULT,
  WORKER_STATE_TOPIC,
  WORKER_ASSIGNMENT_STATE_TOPIC,
  WORKER_DRAIN_STATUS_TOPIC,
  WORKER_ELIGIBILITY_PREVIEW_TOPIC,
  WORKER_CLEANUP_EXECUTION_TOPIC,
  WORKER_STATE_TOPICS,
  WORKER_STATE_D_PREFIX,
  WORKER_ASSIGNMENT_STATE_D_PREFIX,
  WORKER_DRAIN_STATUS_D_PREFIX,
  WORKER_ELIGIBILITY_PREVIEW_D_PREFIX,
  WORKER_CLEANUP_EXECUTION_D_PREFIX
} from '../../nostr/kinds.gen.js';
import {
  contentWithEventMeta,
  getDTag,
  getTagValue,
  isReplaceableTombstone,
  replaceArray,
  sortByNameOrId,
  sortByNewestField
} from './utils.js';

export const workers = $state([]);
export const workerAssignments = $state([]);
export const workerDrainStatuses = $state([]);
export const workerEligibilityPreviews = $state([]);
export const workerCleanupExecutions = $state([]);
export const workerJobs = $state([]);

const workerMap = new Map();
const advertisements = new Map();
const workerStates = new Map();
const familyMaps = new Map([
  [WORKER_ASSIGNMENT_STATE_TOPIC, new Map()],
  [WORKER_DRAIN_STATUS_TOPIC, new Map()],
  [WORKER_ELIGIBILITY_PREVIEW_TOPIC, new Map()],
  [WORKER_CLEANUP_EXECUTION_TOPIC, new Map()]
]);
const familyVersions = new Map([...familyMaps.keys()].map((topic) => [topic, new Map()]));
const workerJobMap = new Map();
const jobOutcomeEvents = new Map();
const jobEvents = new Map();
const eventIndex = new Map();
const coordinateIndex = new Map();
let unsubscribeStore = [];
let relayHandles = [];
let boundStore = null;
let renderQueued = false;

const jobKinds = [LOOM_JOB_REQUEST, LOOM_JOB_STATUS_UPDATE, LOOM_JOB_RESULT];
const workerKinds = [CASCADIA_CONTROLPLANE_STATE, LOOM_WORKER_ADVERTISEMENT, ...jobKinds];
const workerPrefixByTopic = new Map([
  [WORKER_STATE_TOPIC, WORKER_STATE_D_PREFIX],
  [WORKER_ASSIGNMENT_STATE_TOPIC, WORKER_ASSIGNMENT_STATE_D_PREFIX],
  [WORKER_DRAIN_STATUS_TOPIC, WORKER_DRAIN_STATUS_D_PREFIX],
  [WORKER_ELIGIBILITY_PREVIEW_TOPIC, WORKER_ELIGIBILITY_PREVIEW_D_PREFIX],
  [WORKER_CLEANUP_EXECUTION_TOPIC, WORKER_CLEANUP_EXECUTION_D_PREFIX]
]);

function eventCoordinate(event) {
  if (event.kind === LOOM_WORKER_ADVERTISEMENT) return `${event.kind}:${event.pubkey}`;
  const d = getDTag(event);
  return d ? `${event.kind}:${event.pubkey}:${d}` : '';
}

function newer(event, previous) {
  if (!previous) return true;
  if (event.created_at !== previous.created_at) return event.created_at > previous.created_at;
  return event.id < previous.id;
}

function scheduleRender() {
  if (renderQueued) return;
  renderQueued = true;
  if (typeof requestAnimationFrame === 'function') requestAnimationFrame(refreshWorkers);
  else queueMicrotask(refreshWorkers);
}

export function refreshWorkers() {
  if (!renderQueued) return;
  renderQueued = false;
  replaceArray(workers, [...workerMap.values()].sort(sortByNameOrId));
  replaceArray(workerAssignments, [...familyMaps.get(WORKER_ASSIGNMENT_STATE_TOPIC).values()].sort(sortByNameOrId));
  replaceArray(workerDrainStatuses, [...familyMaps.get(WORKER_DRAIN_STATUS_TOPIC).values()].sort(sortByNameOrId));
  replaceArray(workerEligibilityPreviews, [...familyMaps.get(WORKER_ELIGIBILITY_PREVIEW_TOPIC).values()].sort(sortByNewestField(['updated_at', 'nostr_created_at'])));
  replaceArray(workerCleanupExecutions, [...familyMaps.get(WORKER_CLEANUP_EXECUTION_TOPIC).values()].sort(sortByNewestField(['updated_at', 'completed_at', 'started_at', 'nostr_created_at'])));
  replaceArray(workerJobs, [...workerJobMap.values()].sort(sortByNewestField(['updated_at', 'requested_at'])));
}

export function resetWorkers() {
  for (const map of [workerMap, advertisements, workerStates, ...familyMaps.values(), ...familyVersions.values(), workerJobMap, jobOutcomeEvents, jobEvents, eventIndex, coordinateIndex]) map.clear();
  for (const array of [workers, workerAssignments, workerDrainStatuses, workerEligibilityPreviews, workerCleanupExecutions, workerJobs]) array.length = 0;
  renderQueued = false;
}

function normalizeResources(resources) {
  if (!resources || typeof resources !== 'object') return resources;
  const rounded = (value) => Number.isFinite(Number(value)) ? Math.round(Number(value)) : undefined;
  return {
    cpu_cores: resources.cpu_cores ?? resources.CPUCores ?? undefined,
    memory_gb: rounded(resources.memory_gb ?? resources.MemoryGB),
    disk_gb: rounded(resources.disk_gb ?? resources.DiskGB)
  };
}

function normalizeAdvertisement(content) {
  const normalized = { ...content };
  if (normalized.resources) normalized.resources = normalizeResources(normalized.resources);
  if (Array.isArray(normalized.accelerators)) {
    normalized.accelerators = normalized.accelerators.map((item) => ({
      vendor: item.vendor ?? item.Vendor,
      model: item.model ?? item.Model,
      count: item.count ?? item.Count,
      memory_gb: normalizeResources({ memory_gb: item.memory_gb ?? item.MemoryGB }).memory_gb,
      driver: item.driver ?? item.Driver
    }));
  }
  return normalized;
}

function renderWorker(pubkey) {
  const advert = advertisements.get(pubkey);
  const state = workerStates.get(pubkey);
  if (state && (isReplaceableTombstone(state) || contentWithEventMeta(state).deleted === true)) {
    workerMap.delete(pubkey);
    return;
  }
  if (!advert && !state) {
    workerMap.delete(pubkey);
    return;
  }
  const advertContent = advert ? normalizeAdvertisement(contentWithEventMeta(advert)) : {};
  const stateContent = state ? contentWithEventMeta(state) : {};
  workerMap.set(pubkey, {
    ...advertContent,
    ...stateContent,
    pubkey,
    status: stateContent.status || advertContent.status || 'online',
    ...(advert ? { last_advertisement_at: new Date(advert.created_at * 1000).toISOString() } : {})
  });
}

function familyEntityId(topic, event, content) {
  const fields = {
    [WORKER_ASSIGNMENT_STATE_TOPIC]: ['worker_pubkey'],
    [WORKER_DRAIN_STATUS_TOPIC]: ['worker_pubkey'],
    [WORKER_ELIGIBILITY_PREVIEW_TOPIC]: ['preview_id'],
    [WORKER_CLEANUP_EXECUTION_TOPIC]: ['cleanup_id', 'idempotency_key', 'loom_job_id', 'worker_pubkey']
  }[topic] || [];
  return fields.map((key) => content[key]).find(Boolean) || getDTag(event);
}

function jobIdFor(event) {
  return event.kind === LOOM_JOB_REQUEST ? event.id : getTagValue(event, 'e') || (event.kind === LOOM_JOB_STATUS_UPDATE ? getDTag(event) : '');
}

function eventIso(event) {
  return new Date((event?.created_at || 0) * 1000).toISOString();
}

export const LOOM_JOB_TERMINAL_STATUSES = Object.freeze(['completed', 'failed', 'cancelled', 'timeout']);
export function isTerminalLoomJobStatus(status) {
  return LOOM_JOB_TERMINAL_STATUSES.includes(String(status || '').toLowerCase());
}

function mergeJob(jobId, patch, event) {
  const previous = workerJobMap.get(jobId) || { job_id: jobId };
  const next = { ...patch };
  if ((previous.terminal && !next.terminal) || (previous.result_event_id && !next.result_event_id)) {
    delete next.status;
    delete next.message;
    delete next.terminal;
  }
  const merged = { ...previous, ...Object.fromEntries(Object.entries(next).filter(([, value]) => value !== undefined && value !== '')) };
  const updatedAt = eventIso(event);
  if (!merged.updated_at || updatedAt > merged.updated_at || next.terminal) merged.updated_at = updatedAt;
  workerJobMap.set(jobId, merged);
}

function projectJob(event) {
  const jobId = jobIdFor(event);
  if (!jobId) return false;
  if (event.kind === LOOM_JOB_REQUEST) {
    mergeJob(jobId, {
      request_event_id: jobId,
      worker_pubkey: getTagValue(event, 'p') || workerJobMap.get(jobId)?.worker_pubkey,
      client_pubkey: event.pubkey,
      cmd: getTagValue(event, 'cmd'),
      status: workerJobMap.get(jobId)?.status || 'queued',
      requested_at: eventIso(event)
    }, event);
  } else if (event.kind === LOOM_JOB_STATUS_UPDATE) {
    const status = String(getTagValue(event, 'status') || '').toLowerCase();
    if (!status) return false;
    const previousStatus = jobOutcomeEvents.get(`status:${jobId}`);
    if (!newer(event, previousStatus)) return false;
    jobOutcomeEvents.set(`status:${jobId}`, event);
    mergeJob(jobId, {
      request_event_id: jobId,
      worker_pubkey: event.pubkey,
      status,
      message: event.content,
      terminal: isTerminalLoomJobStatus(status) || undefined,
      started_at: status === 'running' ? eventIso(event) : undefined
    }, event);
  } else {
    const previousResult = jobOutcomeEvents.get(`result:${jobId}`);
    if (!newer(event, previousResult)) return false;
    jobOutcomeEvents.set(`result:${jobId}`, event);
    const exitCode = Number.parseInt(getTagValue(event, 'exit_code'), 10);
    const duration = Number.parseInt(getTagValue(event, 'duration'), 10);
    const success = getTagValue(event, 'success') === 'true';
    mergeJob(jobId, {
      request_event_id: jobId,
      result_event_id: event.id,
      worker_pubkey: event.pubkey,
      status: success ? 'completed' : 'failed',
      success,
      exit_code: Number.isFinite(exitCode) ? exitCode : undefined,
      duration_seconds: Number.isFinite(duration) ? duration : undefined,
      stdout_url: getTagValue(event, 'stdout'),
      stderr_url: getTagValue(event, 'stderr'),
      error: getTagValue(event, 'error'),
      terminal: true,
      completed_at: eventIso(event)
    }, event);
  }
  return true;
}

function replayJob(jobId) {
  workerJobMap.delete(jobId);
  jobOutcomeEvents.delete(`status:${jobId}`);
  jobOutcomeEvents.delete(`result:${jobId}`);
  const events = [...(jobEvents.get(jobId)?.values() || [])].sort((a, b) => a.created_at - b.created_at || b.id.localeCompare(a.id));
  for (const event of events) projectJob(event);
}

function forgetIndexed(id) {
  const indexed = eventIndex.get(id);
  if (!indexed) return false;
  eventIndex.delete(id);
  if (indexed.coordinate) coordinateIndex.delete(indexed.coordinate);
  if (indexed.jobId) {
    const events = jobEvents.get(indexed.jobId);
    events?.delete(id);
    if (events?.size === 0) jobEvents.delete(indexed.jobId);
    replayJob(indexed.jobId);
  } else if (indexed.topic === 'advert') {
    if (advertisements.get(indexed.key)?.id === id) advertisements.delete(indexed.key);
    renderWorker(indexed.key);
  } else if (indexed.topic === WORKER_STATE_TOPIC) {
    if (workerStates.get(indexed.key)?.id === id) workerStates.delete(indexed.key);
    renderWorker(indexed.key);
  } else {
    const map = familyMaps.get(indexed.topic);
    if (map?.get(indexed.key)?.nostr_event_id === id) map.delete(indexed.key);
    const versions = familyVersions.get(indexed.topic);
    if (versions?.get(indexed.key)?.id === id) versions.delete(indexed.key);
  }
  return true;
}

function projectEvent(event, servicePubkey, { initial = false } = {}) {
  if (event.kind === 5) {
    let changed = false;
    for (const [tag, target] of event.tags || []) {
      const id = tag === 'e' ? target : (tag === 'a' ? coordinateIndex.get(target) : null);
      const indexed = id && eventIndex.get(id);
      if (indexed && indexed.event.pubkey === event.pubkey && indexed.event.created_at <= event.created_at) changed = forgetIndexed(id) || changed;
    }
    if (changed && !initial) scheduleRender();
    return changed;
  }
  if (!workerKinds.includes(event.kind)) return false;
  const topic = event.kind === CASCADIA_CONTROLPLANE_STATE ? getTagValue(event, 't') : '';
  if (event.kind === CASCADIA_CONTROLPLANE_STATE && (event.pubkey !== servicePubkey || !WORKER_STATE_TOPICS.includes(topic))) return false;
  if (event.kind === CASCADIA_CONTROLPLANE_STATE && !getDTag(event).startsWith(workerPrefixByTopic.get(topic))) return false;
  const jobId = jobKinds.includes(event.kind) ? jobIdFor(event) : '';
  const content = contentWithEventMeta(event);
  const key = event.kind === LOOM_WORKER_ADVERTISEMENT ? event.pubkey
    : topic === WORKER_STATE_TOPIC ? (content.worker_pubkey || content.pubkey || getTagValue(event, 'worker') || getDTag(event).replace(/^worker:state:/, ''))
      : familyMaps.has(topic) ? familyEntityId(topic, event, content) : jobId;
  if (!key) return false;
  const coordinate = eventCoordinate(event);
  const previousId = coordinate && coordinateIndex.get(coordinate);
  if (previousId && previousId !== event.id) {
    const previous = eventIndex.get(previousId);
    if (previous && !newer(event, previous.event)) return false;
    forgetIndexed(previousId);
  }
  const indexed = { event, coordinate, jobId, key, topic: event.kind === LOOM_WORKER_ADVERTISEMENT ? 'advert' : topic };
  eventIndex.set(event.id, indexed);
  if (coordinate) coordinateIndex.set(coordinate, event.id);
  if (jobId) {
    if (!jobEvents.has(jobId)) jobEvents.set(jobId, new Map());
    jobEvents.get(jobId).set(event.id, event);
    projectJob(event);
  } else if (event.kind === LOOM_WORKER_ADVERTISEMENT) {
    advertisements.set(key, event);
    renderWorker(key);
  } else if (topic === WORKER_STATE_TOPIC) {
    workerStates.set(key, event);
    renderWorker(key);
  } else {
    const versions = familyVersions.get(topic);
    if (!newer(event, versions.get(key))) return false;
    versions.set(key, event);
    const map = familyMaps.get(topic);
    if (isReplaceableTombstone(event) || content.deleted === true) map.delete(key);
    else map.set(key, { ...content, id: key });
  }
  if (!initial) scheduleRender();
  return true;
}

export function rebuildWorkersFromStore() {
  const store = getEventStore();
  const servicePubkey = getServicePubkey();
  if (!store || !servicePubkey) return;
  resetWorkers();
  const events = [
    ...store.query({ kinds: [CASCADIA_CONTROLPLANE_STATE], authors: [servicePubkey], '#t': WORKER_STATE_TOPICS }),
    ...store.query({ kinds: [LOOM_WORKER_ADVERTISEMENT] }),
    ...store.query({ kinds: jobKinds })
  ].sort((a, b) => a.created_at - b.created_at || b.id.localeCompare(a.id));
  for (const event of events) projectEvent(event, servicePubkey, { initial: true });
  renderQueued = true;
  refreshWorkers();
}

export function initWorkerStoreBinding() {
  if (boundStore) return;
  const store = getEventStore();
  const pool = getPool();
  const servicePubkey = getServicePubkey();
  if (!store || !servicePubkey) return;
  boundStore = store;
  rebuildWorkersFromStore();
  unsubscribeStore = [
    store.subscribe({ kinds: [CASCADIA_CONTROLPLANE_STATE], authors: [servicePubkey], '#t': WORKER_STATE_TOPICS }, (event) => projectEvent(event, servicePubkey)),
    store.subscribe({ kinds: [LOOM_WORKER_ADVERTISEMENT, ...jobKinds] }, (event) => projectEvent(event, servicePubkey)),
    store.subscribe({ kinds: [5] }, (event) => projectEvent(event, servicePubkey))
  ];
  const relays = [...new Set(getRelayUrls().map(toWebSocketUrl).filter(Boolean))];
  if (pool && relays.length) relayHandles = [
    pool.subscribe({ relays, filters: [{ kinds: [CASCADIA_CONTROLPLANE_STATE], authors: [servicePubkey], '#t': WORKER_STATE_TOPICS }], filterKey: 'worker-state' }),
    pool.subscribe({ relays, filters: [{ kinds: [LOOM_WORKER_ADVERTISEMENT] }, { kinds: [5], limit: 1000 }] }),
    pool.subscribe({ relays, filters: [{ kinds: jobKinds, since: Math.floor(Date.now() / 1000) - 7 * 24 * 60 * 60, limit: 500 }], filterKey: 'loom-jobs' })
  ];
}

export function teardownWorkerStoreBinding() {
  for (const unsubscribe of unsubscribeStore) unsubscribe();
  unsubscribeStore = [];
  for (const handle of relayHandles) handle.unsubscribe();
  relayHandles = [];
  boundStore = null;
  renderQueued = false;
}

export function workerJobsForPubkey(jobs, pubkey) {
  return pubkey ? (jobs || []).filter((job) => job.worker_pubkey === pubkey) : [];
}
