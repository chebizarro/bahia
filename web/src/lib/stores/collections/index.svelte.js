import { services, upsertServiceProjection } from './services.svelte.js';
import { environments } from './environments.svelte.js';
import {
  states,
  llmRoutes,
  llmRouteStates,
  artifacts,
  builds,
  deploymentIntents,
  deploymentRuns,
  policies,
  packageRepositories,
  packageArtifacts,
  packagePromotions
} from './deployments.svelte.js';
import {
  workers,
  workerAssignments,
  workerDrainStatuses,
  workerEligibilityPreviews,
  workerCleanupExecutions,
  workerJobs
} from './workers.svelte.js';
import { operations } from './operations.svelte.js';
import {
  backupRepositories,
  backupPolicies,
  backupRecipes,
  backupDefinitions,
  backupRuns,
  backupVerifications,
  backupRestores,
  backupRetentionRuns,
  backupRuntimeObservations,
  backupAttestations
} from './backup.svelte.js';
import { mlModels, mlModelVersions, mlEndpoints, mlEndpointStates } from './ml.svelte.js';
import { events } from './activity.svelte.js';
import { sbomRefs, sbomAvailability, sbomRefsByArtifact, getSBOMRefsForArtifact, hasSBOMForArtifact, sbomArtifactIds } from './sbom.svelte.js';
import { browser } from '$app/environment';
import { createIndexedDBCollectionCacheAdapter } from './indexeddb-cache.js';
import { replaceableKey, shouldAcceptReplaceableEvent } from '../../nostr/client.js';

export { services, upsertServiceProjection } from './services.svelte.js';
export { environments } from './environments.svelte.js';
export {
  states,
  llmRoutes,
  llmRouteStates,
  artifacts,
  builds,
  deploymentIntents,
  deploymentRuns,
  policies,
  packageRepositories,
  packageArtifacts,
  packagePromotions
} from './deployments.svelte.js';
export {
  workers,
  workerAssignments,
  workerDrainStatuses,
  workerEligibilityPreviews,
  workerCleanupExecutions,
  workerJobs,
  workerJobsForPubkey,
  isTerminalLoomJobStatus
} from './workers.svelte.js';
export {
  operations,
  operationsForDomain,
  operationsForEntity,
  isTerminalOperationStatus,
  OPERATION_REQUEST_KINDS,
  OPERATION_STATUS_KINDS,
  OPERATION_RESULT_KINDS,
  HIVE_CI_OPERATION_KINDS
} from './operations.svelte.js';
export {
  backupRepositories,
  backupPolicies,
  backupRecipes,
  backupDefinitions,
  backupRuns,
  backupVerifications,
  backupRestores,
  backupRetentionRuns,
  backupRuntimeObservations,
  backupAttestations
} from './backup.svelte.js';
export { mlModels, mlModelVersions, mlEndpoints, mlEndpointStates } from './ml.svelte.js';
export { events } from './activity.svelte.js';
export { sbomRefs, sbomAvailability, sbomRefsByArtifact, getSBOMRefsForArtifact, hasSBOMForArtifact, sbomArtifactIds } from './sbom.svelte.js';

import { resetServices, refreshServices } from './services.svelte.js';
import { resetEnvironments, refreshEnvironments } from './environments.svelte.js';
import { resetDeployments, refreshDeployments } from './deployments.svelte.js';
import { resetWorkers, refreshWorkers } from './workers.svelte.js';
import { resetOperations, refreshOperations } from './operations.svelte.js';
import { resetBackup, refreshBackup } from './backup.svelte.js';
import { resetML, refreshML } from './ml.svelte.js';
import { resetActivity, refreshActivity } from './activity.svelte.js';
import { resetSBOM, refreshSBOM } from './sbom.svelte.js';

export const LEGACY_CONTROLPLANE_SNAPSHOT_KEY = 'bahia_controlplane_snapshot_v1';
// v3 persists the winning raw relay events of each stable collection instead of
// projected snapshots. Hydration replays them through the live
// applyControlplaneEvent path, so the backing Maps, the NIP-01 replaceable
// index and newer-wins rules are identical to a live session and a later relay
// event merges into cached state instead of wiping it. v2 projection records
// carry no event coordinates and are discarded on read.
export const CONTROLPLANE_COLLECTION_CACHE_SCHEMA = 'bahia_controlplane_event_cache_v3';
export const CONTROLPLANE_CACHE_TTL_MS = 15 * 60 * 1000;
const PERSISTED_COLLECTION_DEFAULT_CAP = 250;
const PERSISTED_COLLECTION_MIN_CAP = 10;
const PERSISTED_COLLECTION_CAPS = Object.freeze({
  states: 150,
  artifacts: 200,
  deploymentIntents: 150,
  packageArtifacts: 200,
  // Worker rows are merged from an advertisement and a worker-state event.
  workers: 500,
  workerAssignments: 150,
  workerDrainStatuses: 150,
  sbomRefs: 200,
  mlModelVersions: 200
});

export const PERSISTED_CONTROLPLANE_COLLECTIONS = Object.freeze([
  'llmRoutes',
  'artifacts',
  'deploymentIntents',
  'workers',
  'workerAssignments',
  'workerDrainStatuses',
  'backupRepositories',
  'backupPolicies',
  'backupRecipes',
  'backupDefinitions',
  'mlModels',
  'mlModelVersions',
  'mlEndpoints',
  'sbomRefs',
  'sbomAvailability'
]);

export const SKIPPED_CONTROLPLANE_COLLECTIONS = Object.freeze([
  'events',
  'builds',
  'deploymentRuns',
  'packagePromotions',
  'llmRouteStates',
  'workerEligibilityPreviews',
  'workerCleanupExecutions',
  'workerJobs',
  'operations',
  'backupRuns',
  'backupVerifications',
  'backupRestores',
  'backupRetentionRuns',
  'backupRuntimeObservations',
  'backupAttestations',
  'mlEndpointStates'
]);

// Relay events arrive one per WebSocket task, so a microtask batch would still
// rebuild per event. Coalesce streamed events into one rebuild per frame.
const REFRESH_BATCH_MS = 16;

let persistTimer = null;
let refreshTimer = null;
let collectionCacheStorage = createIndexedDBCollectionCacheAdapter();
// collection name -> Map<replaceable coordinate, newest raw event>
const persistedEvents = new Map();

// A loading flag means "nothing to render yet": it is cleared as soon as its
// collection holds data (from cache or relay). EOSE drives the connection
// status (syncing -> live), not rendering.
export const loading = $state({
  services: false,
  environments: false,
  states: false,
  artifacts: false,
  builds: false,
  deploymentIntents: false,
  deploymentRuns: false,
  policies: false
});

export function setAllLoading(value) {
  loading.services = value;
  loading.environments = value;
  loading.states = value;
}

export function clearLoadingForPopulatedCollections() {
  for (const key of Object.keys(loading)) {
    if (loading[key] && COLLECTION_TARGETS[key]?.length > 0) loading[key] = false;
  }
}

function cancelScheduledRefresh() {
  if (!refreshTimer) return;
  clearTimeout(refreshTimer);
  refreshTimer = null;
}

export function resetCollections() {
  cancelScheduledRefresh();
  persistedEvents.clear();
  resetServices();
  resetEnvironments();
  resetDeployments();
  resetWorkers();
  resetOperations();
  resetBackup();
  resetML();
  resetActivity();
  resetSBOM();
  setAllLoading(false);
}

export function refreshCollections() {
  cancelScheduledRefresh();
  refreshServices();
  refreshEnvironments();
  refreshDeployments();
  refreshWorkers();
  refreshOperations();
  refreshBackup();
  refreshML();
  refreshActivity();
  refreshSBOM();
  clearLoadingForPopulatedCollections();
}

export function scheduleRefreshCollections(delayMs = REFRESH_BATCH_MS) {
  if (refreshTimer) return;
  refreshTimer = setTimeout(() => {
    refreshTimer = null;
    refreshCollections();
  }, delayMs);
}

export function flushCollectionRefresh() {
  if (!refreshTimer) return false;
  refreshCollections();
  return true;
}

export function setControlplaneCacheStorageAdapter(adapter) {
  collectionCacheStorage = adapter || createNoopCollectionCacheAdapter();
}

export function resetControlplaneCacheStorageAdapter() {
  collectionCacheStorage = createIndexedDBCollectionCacheAdapter();
}

function createNoopCollectionCacheAdapter() {
  return {
    async getAll() { return []; },
    async putMany() { return false; },
    async delete() { return false; }
  };
}

const COLLECTION_TARGETS = Object.freeze({
  services,
  environments,
  states,
  llmRoutes,
  llmRouteStates,
  artifacts,
  builds,
  deploymentIntents,
  deploymentRuns,
  policies,
  packageRepositories,
  packageArtifacts,
  packagePromotions,
  workers,
  workerAssignments,
  workerDrainStatuses,
  workerEligibilityPreviews,
  workerCleanupExecutions,
  workerJobs,
  operations,
  events,
  sbomRefs,
  sbomAvailability,
  backupRepositories,
  backupPolicies,
  backupRecipes,
  backupDefinitions,
  backupRuns,
  backupVerifications,
  backupRestores,
  backupRetentionRuns,
  backupRuntimeObservations,
  backupAttestations,
  mlModels,
  mlModelVersions,
  mlEndpoints,
  mlEndpointStates
});

function collectionEntries() {
  return Object.fromEntries(
    Object.entries(COLLECTION_TARGETS).map(([collectionName, values]) => [collectionName, Array.from(values)])
  );
}

function isReplaceableOrAddressableKind(kind) {
  return kind === 0 || kind === 3 || (kind >= 10000 && kind < 20000) || (kind >= 30000 && kind < 40000);
}

function isCachedEvent(value) {
  return typeof value?.id === 'string'
    && Number.isInteger(value.kind)
    && typeof value.pubkey === 'string'
    && Array.isArray(value.tags);
}

// Copy only NIP-01 fields so the cache holds structured-clone-safe plain data.
function plainEvent(event) {
  return {
    id: event.id,
    kind: event.kind,
    pubkey: event.pubkey,
    created_at: Number(event.created_at || 0),
    tags: event.tags.map((tag) => (Array.isArray(tag) ? tag.map(String) : [])),
    content: typeof event.content === 'string' ? event.content : '',
    sig: typeof event.sig === 'string' ? event.sig : ''
  };
}

/**
 * Remember an accepted relay event that feeds a persisted collection. Events
 * are reduced by NIP-01 coordinate with the same newer-wins rule as the live
 * path, so the cache holds exactly the winners (tombstones included).
 */
export function recordPersistedEvent(collectionName, event) {
  if (!PERSISTED_CONTROLPLANE_COLLECTIONS.includes(collectionName) || !isCachedEvent(event)) return false;
  const key = isReplaceableOrAddressableKind(event.kind) ? replaceableKey(event) : event.id;
  if (!key) return false;

  let log = persistedEvents.get(collectionName);
  if (!log) {
    log = new Map();
    persistedEvents.set(collectionName, log);
  }
  if (!shouldAcceptReplaceableEvent(log.get(key), event)) return false;
  log.set(key, plainEvent(event));
  return true;
}

function entryTimestamp(entry) {
  const timestamp = entry?.updated_at ?? entry?.created_at ?? entry?.cachedAt;
  const numeric = Number(timestamp);
  return Number.isFinite(numeric) ? numeric : null;
}

function persistedCollectionCap(collectionName, scale = 1) {
  const baseCap = PERSISTED_COLLECTION_CAPS[collectionName] ?? PERSISTED_COLLECTION_DEFAULT_CAP;
  return Math.max(PERSISTED_COLLECTION_MIN_CAP, Math.floor(baseCap * scale));
}

function capPersistedCollection(collectionName, values, scale = 1) {
  if (!Array.isArray(values)) return values;

  const cap = persistedCollectionCap(collectionName, scale);
  if (values.length <= cap) return values.slice();

  const timestamped = values.map((value, index) => ({ value, index, timestamp: entryTimestamp(value) }));
  if (timestamped.some((entry) => entry.timestamp !== null)) {
    return timestamped
      .sort((left, right) => {
        const leftTimestamp = left.timestamp ?? Number.NEGATIVE_INFINITY;
        const rightTimestamp = right.timestamp ?? Number.NEGATIVE_INFINITY;
        if (leftTimestamp !== rightTimestamp) return leftTimestamp - rightTimestamp;
        return left.index - right.index;
      })
      .slice(-cap)
      .map((entry) => entry.value);
  }

  return values.slice(-cap);
}

export function persistedControlplaneCollections(scale = 1) {
  return Object.fromEntries(
    PERSISTED_CONTROLPLANE_COLLECTIONS.map((collectionName) => [
      collectionName,
      capPersistedCollection(collectionName, Array.from(persistedEvents.get(collectionName)?.values() || []), scale)
    ])
  );
}

export function persistedControlplaneSnapshot(scale = 1) {
  return {
    schema: CONTROLPLANE_COLLECTION_CACHE_SCHEMA,
    cachedAt: Date.now(),
    collections: persistedControlplaneCollections(scale)
  };
}

export function controlplaneSnapshot() {
  return {
    schema: CONTROLPLANE_COLLECTION_CACHE_SCHEMA,
    cachedAt: Date.now(),
    collections: collectionEntries()
  };
}

function clearLegacyControlplaneSnapshot() {
  if (!browser || typeof globalThis.localStorage?.removeItem !== 'function') return;

  try {
    globalThis.localStorage.removeItem(LEGACY_CONTROLPLANE_SNAPSHOT_KEY);
  } catch (error) {
    console.warn('Failed to clear legacy controlplane snapshot cache:', error);
  }
}

function isFreshCacheRecord(record, now = Date.now()) {
  const cachedAt = Number(record?.cachedAt);
  return Number.isFinite(cachedAt) && now - cachedAt <= CONTROLPLANE_CACHE_TTL_MS;
}

/**
 * Read cached raw events for the persisted collections. Records from an older
 * schema or past the TTL are deleted. Projection into collections is done by
 * the caller (controlplane/events hydrateCachedControlplane) through the live
 * event-application path.
 */
export async function readCachedControlplaneEvents({ adapter = collectionCacheStorage, now = Date.now() } = {}) {
  if (!browser) return [];
  clearLegacyControlplaneSnapshot();

  try {
    const records = await adapter.getAll();
    if (!Array.isArray(records) || records.length === 0) return [];

    const cachedEvents = [];
    for (const record of records) {
      const collectionName = record?.name;
      if (!PERSISTED_CONTROLPLANE_COLLECTIONS.includes(collectionName)) continue;

      if (record?.schema !== CONTROLPLANE_COLLECTION_CACHE_SCHEMA || !Array.isArray(record?.items) || !isFreshCacheRecord(record, now)) {
        await adapter.delete?.(collectionName);
        continue;
      }

      cachedEvents.push(...capPersistedCollection(collectionName, record.items.filter(isCachedEvent)));
    }
    return cachedEvents;
  } catch (error) {
    console.warn('Failed to read cached controlplane events:', error);
    return [];
  }
}

export async function persistCachedCollections({ adapter = collectionCacheStorage } = {}) {
  if (!browser) return false;

  const cachedAt = Date.now();
  const collections = persistedControlplaneCollections();
  const records = Object.entries(collections).map(([name, items]) => ({
    name,
    schema: CONTROLPLANE_COLLECTION_CACHE_SCHEMA,
    cachedAt,
    items
  }));

  try {
    return await adapter.putMany(records);
  } catch (error) {
    console.warn('Failed to persist controlplane collection cache:', error);
    return false;
  }
}

export function schedulePersistCachedCollections(delayMs = 150) {
  if (!browser) return;
  if (persistTimer) clearTimeout(persistTimer);
  persistTimer = setTimeout(() => {
    persistTimer = null;
    persistCachedCollections().catch((error) => {
      console.warn('Failed to persist scheduled controlplane collection cache:', error);
    });
  }, delayMs);
}
