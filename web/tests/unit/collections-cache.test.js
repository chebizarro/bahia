import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { IDBFactory } from 'fake-indexeddb';
import {
  CONTROLPLANE_CACHE_DB_NAME,
  CONTROLPLANE_COLLECTION_STORE,
  createIndexedDBCollectionCacheAdapter
} from '../../src/lib/stores/collections/indexeddb-cache.js';
import { BAHIA_STATE_SCHEMAS, CASCADIA_CONTROLPLANE_STATE, NIP38_STATUS } from '../../src/lib/nostr/client.js';

const LEGACY_SNAPSHOT_KEY = 'bahia_controlplane_snapshot_v1';
const SERVICE_PUBKEY = 'b'.repeat(64);

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

let eventCounter = 0;
function stateEvent({ domain, schema, d, content, created_at = 100, id, tags = [], pubkey = SERVICE_PUBKEY }) {
  eventCounter += 1;
  return {
    id: id || `${d}-${created_at}-${eventCounter}`.padEnd(64, '0'),
    kind: CASCADIA_CONTROLPLANE_STATE,
    pubkey,
    created_at,
    tags: [['domain', domain], ['schema', schema], ['d', d], ...tags],
    content: JSON.stringify(content),
    sig: 'f'.repeat(128)
  };
}

function serviceEvent(serviceId, { name = serviceId, created_at = 100, id, deleted = false } = {}) {
  return stateEvent({
    domain: 'service',
    schema: BAHIA_STATE_SCHEMAS.SERVICE_REGISTRY,
    d: serviceId,
    id,
    created_at,
    tags: [['deleted', String(deleted)]],
    content: { id: serviceId, name, deleted }
  });
}

function environmentEvent(environmentId, { name = environmentId, created_at = 100 } = {}) {
  return stateEvent({
    domain: 'environment',
    schema: BAHIA_STATE_SCHEMAS.ENVIRONMENT_REGISTRY,
    d: environmentId,
    created_at,
    tags: [['deleted', 'false']],
    content: { id: environmentId, name, deleted: false }
  });
}

// Fresh module graph per "page load": module-level Maps, replaceable index and
// seen-id sets start empty exactly as they do after a browser reload.
async function loadStore(adapter) {
  vi.resetModules();
  const collections = await import('../../src/lib/stores/collections/index.svelte.js');
  const routing = await import('../../src/lib/stores/controlplane/events.svelte.js');
  const { controlplaneConnection } = await import('../../src/lib/stores/controlplane/connection.svelte.js');
  collections.setControlplaneCacheStorageAdapter(adapter);
  controlplaneConnection.servicePubkey = SERVICE_PUBKEY;
  return { collections, routing, controlplaneConnection };
}

describe('controlplane collection cold-start cache', () => {
  let collections;
  let routing;
  let adapter;

  beforeEach(async () => {
    vi.restoreAllMocks();
    localStorage.clear();
    adapter = createMemoryCollectionCacheAdapter();
    ({ collections, routing } = await loadStore(adapter));
  });

  afterEach(() => {
    collections?.resetCollections();
    collections?.resetControlplaneCacheStorageAdapter();
    routing?.resetEventRouting();
    localStorage.clear();
    vi.restoreAllMocks();
  });

  it('maps every persisted collection to at least one relay route', () => {
    expect([...routing.persistedRouteCollections].sort()).toEqual([...collections.PERSISTED_CONTROLPLANE_COLLECTIONS].sort());
  });

  it('persists winning raw events per stable collection and skips high-churn streams', async () => {
    const service = serviceEvent('svc-1', { name: 'Relay Service' });
    routing.applyControlplaneEvent(service);
    routing.applyControlplaneEvent(environmentEvent('env-1', { name: 'Production' }));
    routing.applyControlplaneEvent(stateEvent({
      domain: 'deployment',
      schema: BAHIA_STATE_SCHEMAS.DEPLOYMENT_RUN_REGISTRY,
      d: 'run-1',
      content: { id: 'run-1' }
    }));
    routing.applyControlplaneEvent({
      id: 'a'.repeat(64),
      kind: NIP38_STATUS,
      pubkey: SERVICE_PUBKEY,
      created_at: 100,
      tags: [['domain', 'llm'], ['schema', 'bahia.status.llm.v1'], ['route', 'route-1'], ['status', 'processing']],
      content: JSON.stringify({ status: 'processing', route_id: 'route-1' })
    });
    expect(collections.deploymentRuns).toHaveLength(1);
    expect(collections.events).toHaveLength(1);

    await expect(collections.persistCachedCollections()).resolves.toBe(true);

    const persistedNames = Array.from(adapter.records.keys()).sort();
    expect(persistedNames).toEqual([...collections.PERSISTED_CONTROLPLANE_COLLECTIONS].sort());
    for (const record of adapter.records.values()) {
      expect(record.schema).toBe(collections.CONTROLPLANE_COLLECTION_CACHE_SCHEMA);
    }
    expect(adapter.records.get('services').items).toEqual([service]);
    expect(adapter.records.get('environments').items).toHaveLength(1);
    expect(adapter.records.get('events')).toBeUndefined();
    expect(adapter.records.get('deploymentRuns')).toBeUndefined();
  });

  it('keeps only the newest event per coordinate, including tombstones', async () => {
    const newest = serviceEvent('svc-1', { name: 'v2', created_at: 200 });
    routing.applyControlplaneEvent(serviceEvent('svc-1', { name: 'v1', created_at: 100 }));
    routing.applyControlplaneEvent(newest);
    routing.applyControlplaneEvent(serviceEvent('svc-1', { name: 'older', created_at: 150 }));
    const tombstone = serviceEvent('svc-2', { created_at: 300, deleted: true });
    routing.applyControlplaneEvent(serviceEvent('svc-2', { name: 'gone', created_at: 250 }));
    routing.applyControlplaneEvent(tombstone);

    const persisted = collections.persistedControlplaneCollections().services;
    expect(persisted).toHaveLength(2);
    expect(persisted).toEqual(expect.arrayContaining([newest, tombstone]));
    expect(collections.services.map((service) => service.name)).toEqual(['v2']);
  });

  it('persists raw events through the browser structured-clone boundary', async () => {
    const indexedDB = new IDBFactory();
    const indexedDBAdapter = createIndexedDBCollectionCacheAdapter({ indexedDB });
    collections.setControlplaneCacheStorageAdapter(indexedDBAdapter);
    const route = stateEvent({
      domain: 'llm',
      schema: BAHIA_STATE_SCHEMAS.LLM_ROUTE_REGISTRY,
      d: 'route-1',
      tags: [['route', 'route-1'], ['deleted', 'false']],
      content: { id: 'route-1', config: { route_name: 'routstr', metadata: { backend_class: 'routstrd' } } }
    });
    routing.applyControlplaneEvent(route);

    await expect(collections.persistCachedCollections()).resolves.toBe(true);

    const records = await indexedDBAdapter.getAll();
    const llmRoutes = records.find((record) => record.name === 'llmRoutes');
    expect(llmRoutes.schema).toBe(collections.CONTROLPLANE_COLLECTION_CACHE_SCHEMA);
    expect(llmRoutes.items).toEqual([route]);
  });

  it('caps persisted collections and keeps the newest events', async () => {
    for (let index = 0; index < 260; index += 1) {
      routing.applyControlplaneEvent(serviceEvent(`svc-${index}`, { created_at: 1000 + index }));
    }
    routing.applyControlplaneEvent(serviceEvent('svc-newest', { created_at: 9999 }));

    const persisted = collections.persistedControlplaneSnapshot();
    await expect(collections.persistCachedCollections()).resolves.toBe(true);

    expect(collections.services).toHaveLength(261);
    expect(persisted.collections.services).toHaveLength(250);
    expect(persisted.collections.services[0].tags).toContainEqual(['d', 'svc-11']);
    expect(persisted.collections.services.at(-1).tags).toContainEqual(['d', 'svc-newest']);
    expect(adapter.records.get('services').items).toHaveLength(250);
  });

  it('hydrates by replaying cached events and tolerates missing collections', async () => {
    routing.applyControlplaneEvent(serviceEvent('svc-1', { name: 'Relay Service' }));
    routing.applyControlplaneEvent(environmentEvent('env-1', { name: 'Production' }));
    await collections.persistCachedCollections();
    adapter.records.delete('environments');

    ({ collections, routing } = await loadStore(adapter));
    expect(collections.services).toHaveLength(0);

    await expect(routing.hydrateCachedControlplane()).resolves.toBe(true);
    expect(collections.services).toEqual([expect.objectContaining({ id: 'svc-1', name: 'Relay Service' })]);
    expect(collections.environments).toEqual([]);
    expect(collections.events).toEqual([]);
  });

  it('ignores skipped high-churn records during hydrate', async () => {
    const now = Date.now();
    const schema = collections.CONTROLPLANE_COLLECTION_CACHE_SCHEMA;
    await adapter.putMany([
      { name: 'deploymentRuns', schema, cachedAt: now, items: [stateEvent({ domain: 'deployment', schema: BAHIA_STATE_SCHEMAS.DEPLOYMENT_RUN_REGISTRY, d: 'run-1', content: { id: 'run-1' } })] },
      { name: 'services', schema, cachedAt: now, items: [serviceEvent('svc-cached')] }
    ]);

    await expect(routing.hydrateCachedControlplane({ now })).resolves.toBe(true);
    expect(collections.services).toEqual([expect.objectContaining({ id: 'svc-cached' })]);
    expect(collections.deploymentRuns).toEqual([]);
  });

  it('discards v2 projection-snapshot records instead of rendering them', async () => {
    const now = Date.now();
    await adapter.putMany([
      { name: 'services', cachedAt: now, items: [{ id: 'svc-projection', updated_at: now }] }
    ]);

    await expect(routing.hydrateCachedControlplane({ now })).resolves.toBe(false);
    expect(collections.services).toEqual([]);
    expect(adapter.records.get('services')).toBeUndefined();
  });

  it('drops stale records past the cache TTL', async () => {
    const now = 10_000_000;
    await adapter.putMany([
      {
        name: 'services',
        schema: collections.CONTROLPLANE_COLLECTION_CACHE_SCHEMA,
        cachedAt: now - collections.CONTROLPLANE_CACHE_TTL_MS - 1,
        items: [serviceEvent('stale-service')]
      }
    ]);

    await expect(routing.hydrateCachedControlplane({ now })).resolves.toBe(false);
    expect(collections.services).toEqual([]);
    expect(adapter.records.get('services')).toBeUndefined();
  });

  it('rejects cached canonical events not authored by the trusted service pubkey', async () => {
    const now = Date.now();
    await adapter.putMany([
      {
        name: 'services',
        schema: collections.CONTROLPLANE_COLLECTION_CACHE_SCHEMA,
        cachedAt: now,
        items: [serviceEvent('svc-foreign', { id: 'e'.repeat(64) }), { ...serviceEvent('svc-spoof'), pubkey: 'f'.repeat(64) }]
      }
    ]);

    await expect(routing.hydrateCachedControlplane({ now })).resolves.toBe(true);
    expect(collections.services.map((service) => service.id)).toEqual(['svc-foreign']);
  });

  it('clears the legacy localStorage snapshot key during migration hydrate', async () => {
    localStorage.setItem(LEGACY_SNAPSHOT_KEY, JSON.stringify({ schema: LEGACY_SNAPSHOT_KEY, cachedAt: Date.now(), collections: {} }));

    await expect(routing.hydrateCachedControlplane()).resolves.toBe(false);

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
    routing.applyControlplaneEvent(serviceEvent('svc-1'));

    await expect(collections.persistCachedCollections()).resolves.toBe(false);
    ({ collections, routing } = await loadStore(unavailableAdapter));
    await expect(routing.hydrateCachedControlplane()).resolves.toBe(false);
    expect(collections.services).toEqual([]);
  });

  it('drops the v2 projection object store when upgrading the IndexedDB schema', async () => {
    const indexedDB = new IDBFactory();
    await new Promise((resolve, reject) => {
      const request = indexedDB.open(CONTROLPLANE_CACHE_DB_NAME, 2);
      request.onupgradeneeded = () => {
        request.result.createObjectStore(CONTROLPLANE_COLLECTION_STORE, { keyPath: 'name' })
          .put({ name: 'services', cachedAt: Date.now(), items: [{ id: 'svc-v2-projection' }] });
      };
      request.onsuccess = () => { request.result.close(); resolve(); };
      request.onerror = () => reject(request.error);
    });

    await expect(createIndexedDBCollectionCacheAdapter({ indexedDB }).getAll()).resolves.toEqual([]);
  });
});

describe('controlplane cache hydration across a reload (IndexedDB)', () => {
  let indexedDB;
  let collections;
  let routing;

  async function reloadFromIndexedDB() {
    ({ collections, routing } = await loadStore(createIndexedDBCollectionCacheAdapter({ indexedDB })));
    await expect(routing.hydrateCachedControlplane()).resolves.toBe(true);
  }

  beforeEach(async () => {
    localStorage.clear();
    indexedDB = new IDBFactory();
    // Session 1: live relay events populate the store and are persisted.
    ({ collections, routing } = await loadStore(createIndexedDBCollectionCacheAdapter({ indexedDB })));
    routing.applyControlplaneEvent(serviceEvent('svc-cached', { name: 'Cached API', created_at: 200, id: '5'.repeat(64) }));
    routing.applyControlplaneEvent(environmentEvent('env-cached', { name: 'Cached Prod', created_at: 200 }));
    await expect(collections.persistCachedCollections()).resolves.toBe(true);
  });

  afterEach(() => {
    collections?.resetCollections();
    routing?.resetEventRouting();
  });

  it('keeps hydrated entities when the first relay event is for a different entity', async () => {
    await reloadFromIndexedDB();
    expect(collections.services.map((service) => service.id)).toEqual(['svc-cached']);
    expect(collections.environments.map((environment) => environment.id)).toEqual(['env-cached']);

    expect(routing.applyControlplaneEvent(serviceEvent('svc-live', { name: 'Live API', created_at: 300 }))).toBe(true);

    expect(collections.services.map((service) => service.id).sort()).toEqual(['svc-cached', 'svc-live']);
    expect(collections.environments.map((environment) => environment.id)).toEqual(['env-cached']);
  });

  it('replaces a cached entity only with a newer relay event', async () => {
    await reloadFromIndexedDB();

    // Older event for the cached coordinate: ignored.
    expect(routing.applyControlplaneEvent(serviceEvent('svc-cached', { name: 'Older API', created_at: 150 }))).toBe(false);
    // Same created_at, lexically larger id: loses the NIP-01 tie-break.
    expect(routing.applyControlplaneEvent(serviceEvent('svc-cached', { name: 'Tie API', created_at: 200, id: '9'.repeat(64) }))).toBe(false);
    // The exact cached event re-delivered by a relay: no change.
    expect(routing.applyControlplaneEvent(serviceEvent('svc-cached', { name: 'Cached API', created_at: 200, id: '5'.repeat(64) }))).toBe(false);
    expect(collections.services).toEqual([expect.objectContaining({ id: 'svc-cached', name: 'Cached API' })]);

    // Newer event: replaces the cached entity.
    expect(routing.applyControlplaneEvent(serviceEvent('svc-cached', { name: 'Newer API', created_at: 400 }))).toBe(true);
    expect(collections.services).toEqual([expect.objectContaining({ id: 'svc-cached', name: 'Newer API' })]);
  });

  it('applies a newer relay tombstone to a cached entity', async () => {
    await reloadFromIndexedDB();

    expect(routing.applyControlplaneEvent(serviceEvent('svc-cached', { created_at: 400, deleted: true }))).toBe(true);
    expect(collections.services).toEqual([]);
  });

  it('carries hydrated events forward into the next persisted snapshot', async () => {
    await reloadFromIndexedDB();
    routing.applyControlplaneEvent(serviceEvent('svc-live', { created_at: 300 }));
    await expect(collections.persistCachedCollections()).resolves.toBe(true);

    await reloadFromIndexedDB();
    expect(collections.services.map((service) => service.id).sort()).toEqual(['svc-cached', 'svc-live']);
  });
});
