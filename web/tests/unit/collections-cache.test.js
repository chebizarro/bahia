import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { IDBFactory } from 'fake-indexeddb';
import { createIndexedDBCollectionCacheAdapter } from '../../src/lib/stores/collections/indexeddb-cache.js';

const LEGACY_SNAPSHOT_KEY = 'bahia_controlplane_snapshot_v1';

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

function createMemoryCollectionCacheAdapter() {
  const records = new Map();

  return {
    records,
    async getAll() {
      return Array.from(records.values()).map(clone);
    },
    async putMany(nextRecords) {
      for (const record of nextRecords) {
        records.set(record.name, clone(record));
      }
      return true;
    },
    async delete(name) {
      records.delete(name);
      return true;
    }
  };
}

describe('controlplane collection cold-start cache', () => {
  let collections;
  let adapter;

  beforeEach(async () => {
    vi.resetModules();
    vi.restoreAllMocks();
    localStorage.clear();
    adapter = createMemoryCollectionCacheAdapter();
    collections = await import('../../src/lib/stores/collections/index.svelte.js');
    collections.setControlplaneCacheStorageAdapter(adapter);
    collections.resetCollections();
  });

  afterEach(() => {
    collections?.resetCollections();
    collections?.resetControlplaneCacheStorageAdapter();
    localStorage.clear();
    vi.restoreAllMocks();
  });

  it('persists stable collections as separate records and skips high-churn streams', async () => {
    collections.services.push({ id: 'svc-1', name: 'Relay Service', updated_at: 100 });
    collections.environments.push({ id: 'env-1', name: 'Production', cachedAt: 101 });
    collections.workers.push({ id: 'worker-1', name: 'GPU Worker', updated_at: 102 });
    collections.workerAssignments.push({ id: 'assignment-1', worker_id: 'worker-1', updated_at: 103 });
    collections.backupPolicies.push({ id: 'policy-1', name: 'Daily', updated_at: 104 });
    collections.mlEndpoints.push({ id: 'endpoint-1', name: 'embeddings', updated_at: 105 });
    collections.sbomRefs.push({ id: 'sbom-1', artifact_id: 'artifact-1', created_at: 106 });
    collections.events.push({ id: 'evt-1', created_at: 107 });
    collections.deploymentRuns.push({ id: 'run-1', created_at: 108 });
    collections.workerEligibilityPreviews.push({ id: 'preview-1', updated_at: 109 });
    collections.backupRuns.push({ id: 'backup-run-1', created_at: 110 });
    collections.packagePromotions.push({ id: 'promotion-1', promoted_at: 111 });
    collections.mlEndpointStates.push({ id: 'endpoint-state-1', updated_at: 112 });

    await expect(collections.persistCachedCollections()).resolves.toBe(true);

    const persistedNames = Array.from(adapter.records.keys()).sort();
    expect(persistedNames).toEqual([...collections.PERSISTED_CONTROLPLANE_COLLECTIONS].sort());
    expect(adapter.records.get('services').items).toEqual([{ id: 'svc-1', name: 'Relay Service', updated_at: 100 }]);
    expect(adapter.records.get('workers').items).toEqual([{ id: 'worker-1', name: 'GPU Worker', updated_at: 102 }]);
    expect(adapter.records.get('events')).toBeUndefined();
    expect(adapter.records.get('deploymentRuns')).toBeUndefined();
    expect(adapter.records.get('workerEligibilityPreviews')).toBeUndefined();
    expect(adapter.records.get('backupRuns')).toBeUndefined();
    expect(adapter.records.get('packagePromotions')).toBeUndefined();
    expect(adapter.records.get('mlEndpointStates')).toBeUndefined();
  });

  it('persists deeply reactive collection entries through the browser structured-clone boundary', async () => {
    const indexedDB = new IDBFactory();
    const indexedDBAdapter = createIndexedDBCollectionCacheAdapter({ indexedDB });
    collections.setControlplaneCacheStorageAdapter(indexedDBAdapter);
    collections.llmRoutes.push({
      id: 'route-1',
      config: {
        route_name: 'routstr',
        metadata: { backend_class: 'routstrd' }
      }
    });

    await expect(collections.persistCachedCollections()).resolves.toBe(true);

    const records = await indexedDBAdapter.getAll();
    expect(records.find((record) => record.name === 'llmRoutes')?.items).toEqual([
      {
        id: 'route-1',
        config: {
          route_name: 'routstr',
          metadata: { backend_class: 'routstrd' }
        }
      }
    ]);
  });

  it('caps persisted collections and keeps the newest timestamped items', async () => {
    for (let index = 0; index < 260; index += 1) {
      collections.services.push({ id: `svc-${index}`, updated_at: index });
    }
    collections.services.push({ id: 'svc-newest', updated_at: 999 });

    const persisted = collections.persistedControlplaneSnapshot();
    await expect(collections.persistCachedCollections()).resolves.toBe(true);

    expect(collections.services).toHaveLength(261);
    expect(persisted.collections.services).toHaveLength(250);
    expect(persisted.collections.services[0].id).toBe('svc-11');
    expect(persisted.collections.services.at(-1).id).toBe('svc-newest');
    expect(adapter.records.get('services').items).toHaveLength(250);
    expect(adapter.records.get('services').items[0].id).toBe('svc-11');
  });

  it('hydrates persisted per-collection records asynchronously and tolerates missing collections', async () => {
    collections.services.push({ id: 'svc-1', name: 'Relay Service', updated_at: 100 });
    collections.environments.push({ id: 'env-1', name: 'Production', cachedAt: 101 });
    collections.events.push({ id: 'evt-1', created_at: 102 });

    await collections.persistCachedCollections();
    adapter.records.delete('environments');

    collections.resetCollections();
    expect(collections.services).toHaveLength(0);
    expect(collections.environments).toHaveLength(0);
    expect(collections.events).toHaveLength(0);

    await expect(collections.hydrateCachedCollections()).resolves.toBe(true);
    expect(collections.services).toEqual([{ id: 'svc-1', name: 'Relay Service', updated_at: 100 }]);
    expect(collections.environments).toEqual([]);
    expect(collections.events).toEqual([]);
  });

  it('ignores skipped high-churn records during hydrate', async () => {
    const now = Date.now();
    await adapter.putMany([
      { name: 'events', cachedAt: now, items: [{ id: 'evt-cached', created_at: now }] },
      { name: 'deploymentRuns', cachedAt: now, items: [{ id: 'run-cached', created_at: now }] },
      { name: 'services', cachedAt: now, items: [{ id: 'svc-cached', updated_at: now }] }
    ]);

    await expect(collections.hydrateCachedCollections({ now })).resolves.toBe(true);
    expect(collections.services).toEqual([{ id: 'svc-cached', updated_at: now }]);
    expect(collections.events).toEqual([]);
    expect(collections.deploymentRuns).toEqual([]);
  });

  it('drops stale records past the cache TTL', async () => {
    const now = 10_000_000;
    await adapter.putMany([
      {
        name: 'services',
        cachedAt: now - collections.CONTROLPLANE_CACHE_TTL_MS - 1,
        items: [{ id: 'stale-service', updated_at: 1 }]
      }
    ]);

    await expect(collections.hydrateCachedCollections({ now })).resolves.toBe(false);
    expect(collections.services).toEqual([]);
    expect(adapter.records.get('services')).toBeUndefined();
  });

  it('clears the legacy localStorage snapshot key during migration hydrate', async () => {
    localStorage.setItem(LEGACY_SNAPSHOT_KEY, JSON.stringify({ schema: LEGACY_SNAPSHOT_KEY, cachedAt: Date.now(), collections: {} }));

    await expect(collections.hydrateCachedCollections()).resolves.toBe(false);

    expect(localStorage.getItem(LEGACY_SNAPSHOT_KEY)).toBeNull();
  });

  it('warns when IndexedDB operations degrade to cache fallbacks', async () => {
    const openError = new Error('storage denied');
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const failingAdapter = createIndexedDBCollectionCacheAdapter({
      indexedDB: { open: vi.fn(() => { throw openError; }) }
    });

    await expect(failingAdapter.getAll()).resolves.toEqual([]);
    await expect(failingAdapter.putMany([{ name: 'services', items: [] }])).resolves.toBe(false);
    await expect(failingAdapter.delete('services')).resolves.toBe(false);

    expect(warn).toHaveBeenCalledTimes(3);
    expect(warn).toHaveBeenCalledWith('[controlplane-cache] IndexedDB open failed:', openError);
  });

  it('no-ops without throwing when IndexedDB is unavailable', async () => {
    const unavailableAdapter = createIndexedDBCollectionCacheAdapter({ indexedDB: undefined });
    collections.setControlplaneCacheStorageAdapter(unavailableAdapter);
    collections.services.push({ id: 'svc-1', name: 'Relay Service' });

    await expect(collections.persistCachedCollections()).resolves.toBe(false);
    collections.resetCollections();
    await expect(collections.hydrateCachedCollections()).resolves.toBe(false);
    expect(collections.services).toEqual([]);
  });
});
