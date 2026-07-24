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
  workerCleanupExecutions
} from './workers.svelte.js';
import {
  backupRepositories,
  backupPolicies,
  backupRecipes,
  backupDefinitions,
  backupRuns,
  backupVerifications,
  backupRestores,
  backupRetentionRuns,
  backupRuntimeObservations
} from './backup.svelte.js';
import { mlModels, mlModelVersions, mlEndpoints, mlEndpointStates } from './ml.svelte.js';
import { events } from './activity.svelte.js';
import { sbomRefs, sbomAvailability, sbomRefsByArtifact, getSBOMRefsForArtifact, hasSBOMForArtifact, sbomArtifactIds } from './sbom.svelte.js';
import { browser } from '$app/environment';
import { createIndexedDBCollectionCacheAdapter } from './indexeddb-cache.js';

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
  workerCleanupExecutions
} from './workers.svelte.js';
export {
  backupRepositories,
  backupPolicies,
  backupRecipes,
  backupDefinitions,
  backupRuns,
  backupVerifications,
  backupRestores,
  backupRetentionRuns,
  backupRuntimeObservations
} from './backup.svelte.js';
export { mlModels, mlModelVersions, mlEndpoints, mlEndpointStates } from './ml.svelte.js';
export { events } from './activity.svelte.js';
export { sbomRefs, sbomAvailability, sbomRefsByArtifact, getSBOMRefsForArtifact, hasSBOMForArtifact, sbomArtifactIds } from './sbom.svelte.js';

import { resetServices, refreshServices } from './services.svelte.js';
import { resetEnvironments, refreshEnvironments } from './environments.svelte.js';
import { resetDeployments, refreshDeployments } from './deployments.svelte.js';
import { resetWorkers, refreshWorkers } from './workers.svelte.js';
import { resetBackup, refreshBackup } from './backup.svelte.js';
import { resetML, refreshML } from './ml.svelte.js';
import { resetActivity, refreshActivity } from './activity.svelte.js';
import { resetSBOM, refreshSBOM } from './sbom.svelte.js';

export const LEGACY_CONTROLPLANE_SNAPSHOT_KEY = 'bahia_controlplane_snapshot_v1';
export const CONTROLPLANE_COLLECTION_CACHE_SCHEMA = 'bahia_controlplane_collection_cache_v2';
export const CONTROLPLANE_CACHE_TTL_MS = 15 * 60 * 1000;
const PERSISTED_COLLECTION_DEFAULT_CAP = 250;
const PERSISTED_COLLECTION_MIN_CAP = 10;
const PERSISTED_COLLECTION_CAPS = Object.freeze({
  states: 150,
  artifacts: 200,
  deploymentIntents: 150,
  packageArtifacts: 200,
  workerAssignments: 150,
  workerDrainStatuses: 150,
  sbomRefs: 200,
  mlModelVersions: 200
});

export const PERSISTED_CONTROLPLANE_COLLECTIONS = Object.freeze([
  'services',
  'environments',
  'states',
  'llmRoutes',
  'artifacts',
  'deploymentIntents',
  'policies',
  'packageRepositories',
  'packageArtifacts',
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
  'backupRuns',
  'backupVerifications',
  'backupRestores',
  'backupRetentionRuns',
  'backupRuntimeObservations',
  'mlEndpointStates'
]);

let persistTimer = null;
let collectionCacheStorage = createIndexedDBCollectionCacheAdapter();

export const loading = $state({
  services: false,
  environments: false,
  states: false,
  artifacts: false,
  builds: false,
  deploymentIntents: false,
  deploymentRuns: false,
  policies: false,
  workers: false
});

export function setAllLoading(value) {
  loading.services = value;
  loading.environments = value;
  loading.states = value;
  loading.workers = value;
}

export function resetCollections() {
  resetServices();
  resetEnvironments();
  resetDeployments();
  resetWorkers();
  resetBackup();
  resetML();
  resetActivity();
  resetSBOM();
  setAllLoading(false);
}

export function refreshCollections() {
  refreshServices();
  refreshEnvironments();
  refreshDeployments();
  refreshWorkers();
  refreshBackup();
  refreshML();
  refreshActivity();
  refreshSBOM();
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

function replaceSnapshotArray(target, values) {
  target.length = 0;
  if (Array.isArray(values)) target.push(...values);
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

function snapshotPersistedCollection(collectionName) {
  const values = COLLECTION_TARGETS[collectionName];
  if (!Array.isArray(values)) return [];

  // IndexedDB uses the structured-clone algorithm, which cannot clone the
  // reactive Proxy objects produced by Svelte's deeply reactive $state arrays.
  // Snapshot at the persistence boundary so the cache contains plain data
  // transfer objects rather than live application state.
  return $state.snapshot(values);
}

export function persistedControlplaneCollections(scale = 1) {
  return Object.fromEntries(
    PERSISTED_CONTROLPLANE_COLLECTIONS.map((collectionName) => [
      collectionName,
      capPersistedCollection(collectionName, snapshotPersistedCollection(collectionName), scale)
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

export async function hydrateCachedCollections({ adapter = collectionCacheStorage, now = Date.now() } = {}) {
  if (!browser) return false;
  clearLegacyControlplaneSnapshot();

  try {
    const records = await adapter.getAll();
    if (!Array.isArray(records) || records.length === 0) return false;

    let hydrated = false;
    for (const record of records) {
      const collectionName = record?.name;
      const target = COLLECTION_TARGETS[collectionName];
      if (!target) continue;
      if (!PERSISTED_CONTROLPLANE_COLLECTIONS.includes(collectionName)) continue;
      if (!Array.isArray(record?.items)) continue;

      if (!isFreshCacheRecord(record, now)) {
        await adapter.delete?.(collectionName);
        continue;
      }

      replaceSnapshotArray(target, capPersistedCollection(collectionName, record.items));
      hydrated = true;
    }

    return hydrated;
  } catch (error) {
    console.warn('Failed to hydrate cached controlplane collections:', error);
    return false;
  }
}

export async function persistCachedCollections({ adapter = collectionCacheStorage } = {}) {
  if (!browser) return false;

  const cachedAt = Date.now();
  const collections = persistedControlplaneCollections();
  const records = Object.entries(collections).map(([name, items]) => ({ name, cachedAt, items }));

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
