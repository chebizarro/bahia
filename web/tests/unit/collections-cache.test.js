import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { IDBFactory } from 'fake-indexeddb';
import { CONTROLPLANE_CACHE_DB_NAME, CONTROLPLANE_COLLECTION_STORE, createIndexedDBCollectionCacheAdapter } from '../../src/lib/stores/collections/indexeddb-cache.js';
import { BAHIA_STATE_SCHEMAS, CASCADIA_CONTROLPLANE_STATE } from '../../src/lib/nostr/client.js';

const SERVICE = 'b'.repeat(64);
function adapter() {
  const records = new Map();
  return {
    records,
    async getAll() { return structuredClone([...records.values()]); },
    async putMany(items) { for (const item of items) records.set(item.name, structuredClone(item)); return true; },
    async delete(name) { records.delete(name); return true; }
  };
}
function route(id, { name = id, created_at = 100, deleted = false, pubkey = SERVICE } = {}) {
  return {
    id: `${id}-${created_at}-${deleted}`.padEnd(64, '0'), kind: CASCADIA_CONTROLPLANE_STATE, pubkey, created_at,
    tags: [['d', id], ['domain', 'llm'], ['schema', BAHIA_STATE_SCHEMAS.LLM_ROUTE_REGISTRY], ['deleted', String(deleted)]],
    content: JSON.stringify({ id, name, deleted }), sig: 'f'.repeat(128)
  };
}
async function load(cache) {
  vi.resetModules();
  const collections = await import('../../src/lib/stores/collections/index.svelte.js');
  const routing = await import('../../src/lib/stores/controlplane/events.svelte.js');
  const { controlplaneConnection } = await import('../../src/lib/stores/controlplane/connection.svelte.js');
  collections.setControlplaneCacheStorageAdapter(cache);
  controlplaneConnection.servicePubkey = SERVICE;
  return { collections, routing };
}

describe('legacy collection cache for unmigrated domains', () => {
  let cache, collections, routing;
  beforeEach(async () => {
    cache = adapter();
    ({ collections, routing } = await load(cache));
  });
  afterEach(() => {
    collections?.resetCollections();
    collections?.resetControlplaneCacheStorageAdapter();
    routing?.resetEventRouting();
  });

  it('persists only remaining router-owned domains', async () => {
    expect([...routing.persistedRouteCollections].sort()).toEqual([...collections.PERSISTED_CONTROLPLANE_COLLECTIONS].sort());
    for (const name of ['services', 'environments', 'states', 'policies', 'packageRepositories', 'packageArtifacts', 'workers', 'workerAssignments', 'workerDrainStatuses', 'backupRepositories', 'mlModels', 'sbomRefs']) {
      expect(collections.PERSISTED_CONTROLPLANE_COLLECTIONS).not.toContain(name);
    }
    routing.applyControlplaneEvent(route('route-1'));
    expect(collections.llmRoutes).toHaveLength(1);
    await expect(collections.persistCachedCollections()).resolves.toBe(true);
    expect(cache.records.get('llmRoutes').items).toEqual([route('route-1')]);
    expect(cache.records.has('services')).toBe(false);
  });

  it('keeps newest coordinate, tombstones, and bounded snapshots for remaining domains', async () => {
    routing.applyControlplaneEvent(route('route-1', { created_at: 100 }));
    routing.applyControlplaneEvent(route('route-1', { created_at: 200, name: 'new' }));
    routing.applyControlplaneEvent(route('route-1', { created_at: 150 }));
    expect(collections.llmRoutes[0].name).toBe('new');
    expect(collections.persistedControlplaneCollections().llmRoutes).toHaveLength(1);
    routing.applyControlplaneEvent(route('route-1', { created_at: 300, deleted: true }));
    expect(collections.llmRoutes).toHaveLength(0);
    expect(collections.persistedControlplaneCollections().llmRoutes[0].tags).toContainEqual(['deleted', 'true']);
  });

  it('hydrates remaining domains after reload and rejects a foreign canonical author', async () => {
    const original = route('route-1');
    routing.applyControlplaneEvent(original);
    await collections.persistCachedCollections();
    cache.records.get('llmRoutes').items.push(route('spoof', { pubkey: 'f'.repeat(64) }));
    ({ collections, routing } = await load(cache));
    await expect(routing.hydrateCachedControlplane()).resolves.toBe(true);
    expect(collections.llmRoutes.map(row => row.id)).toEqual(['route-1']);
  });

  it('expires stale records and discards old projection-only cache records', async () => {
    const now = Date.now();
    await cache.putMany([{ name: 'llmRoutes', schema: collections.CONTROLPLANE_COLLECTION_CACHE_SCHEMA, cachedAt: now - collections.CONTROLPLANE_CACHE_TTL_MS - 1, items: [route('stale')] }]);
    await expect(routing.hydrateCachedControlplane({ now })).resolves.toBe(false);
    expect(cache.records.has('llmRoutes')).toBe(false);
    await cache.putMany([{ name: 'llmRoutes', cachedAt: now, items: [{ id: 'projection' }] }]);
    await expect(routing.hydrateCachedControlplane({ now })).resolves.toBe(false);
    expect(cache.records.has('llmRoutes')).toBe(false);
  });

  it('round-trips raw events through IndexedDB structured clone', async () => {
    const idb = new IDBFactory();
    const storage = createIndexedDBCollectionCacheAdapter({ indexedDB: idb });
    collections.setControlplaneCacheStorageAdapter(storage);
    const original = route('route-1');
    routing.applyControlplaneEvent(original);
    await expect(collections.persistCachedCollections()).resolves.toBe(true);
    const records = await storage.getAll();
    expect(records.find(record => record.name === 'llmRoutes').items).toEqual([original]);
  });

  it('degrades explicitly when IndexedDB is unavailable', async () => {
    const unavailable = createIndexedDBCollectionCacheAdapter({ indexedDB: undefined });
    collections.setControlplaneCacheStorageAdapter(unavailable);
    routing.applyControlplaneEvent(route('route-1'));
    await expect(collections.persistCachedCollections()).resolves.toBe(false);
  });

  it('drops the v2 projection object store on schema upgrade', async () => {
    const idb = new IDBFactory();
    await new Promise((resolve, reject) => {
      const request = idb.open(CONTROLPLANE_CACHE_DB_NAME, 2);
      request.onupgradeneeded = () => request.result.createObjectStore(CONTROLPLANE_COLLECTION_STORE, { keyPath: 'name' }).put({ name: 'llmRoutes', items: [{ id: 'old-projection' }] });
      request.onsuccess = () => { request.result.close(); resolve(); };
      request.onerror = () => reject(request.error);
    });
    await expect(createIndexedDBCollectionCacheAdapter({ indexedDB: idb }).getAll()).resolves.toEqual([]);
  });
});
