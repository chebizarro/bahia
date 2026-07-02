import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

const SNAPSHOT_KEY = 'bahia_controlplane_snapshot_v1';
const originalLocalStorage = window.localStorage;

describe('controlplane collection cold-start cache', () => {
  let collections;

  beforeEach(async () => {
    vi.resetModules();
    vi.restoreAllMocks();
    localStorage.clear();
    collections = await import('../../src/lib/stores/collections/index.svelte.js');
    collections.resetCollections();
  });

  afterEach(() => {
    collections?.resetCollections();
    Object.defineProperty(window, 'localStorage', { value: originalLocalStorage, configurable: true });
    localStorage.clear();
    vi.restoreAllMocks();
  });

  it('caps high-churn persisted collections and keeps the most recent timestamped items', () => {
    for (let index = 0; index < 210; index += 1) {
      collections.events.push({ id: `evt-${index}`, created_at: index });
    }
    collections.events.push({ id: 'evt-newest', created_at: 999 });

    const persisted = collections.persistedControlplaneSnapshot();

    expect(collections.events).toHaveLength(211);
    expect(persisted.collections.events).toHaveLength(200);
    expect(persisted.collections.events[0].id).toBe('evt-11');
    expect(persisted.collections.events.at(-1).id).toBe('evt-newest');
  });

  it('shrinks caps on quota errors and removes the stale cache when bounded retries keep failing', () => {
    const quotaError = new DOMException('The quota has been exceeded.', 'QuotaExceededError');
    const setItemSpy = vi.fn(() => {
      throw quotaError;
    });
    const removeItemSpy = vi.fn();
    Object.defineProperty(window, 'localStorage', {
      value: { setItem: setItemSpy, getItem: vi.fn(), removeItem: removeItemSpy, clear: vi.fn() },
      configurable: true
    });
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
    for (let index = 0; index < 210; index += 1) {
      collections.events.push({ id: `evt-${index}`, created_at: index });
    }

    const result = collections.persistCachedCollections();
    const attemptedEventCounts = setItemSpy.mock.calls.map(([, value]) => JSON.parse(value).collections.events.length);

    expect(result).toBe(false);
    expect(setItemSpy).toHaveBeenCalledTimes(5);
    expect(attemptedEventCounts).toEqual([200, 100, 50, 25, 12]);
    expect(removeItemSpy).toHaveBeenCalledWith(SNAPSHOT_KEY);
    expect(warnSpy).toHaveBeenCalledTimes(1);
  });

  it('persists and hydrates a capped cold-start snapshot round-trip', () => {
    collections.services.push({ id: 'svc-1', name: 'Relay Service', updated_at: 100 });
    collections.environments.push({ id: 'env-1', name: 'Production', cachedAt: 101 });
    collections.events.push({ id: 'evt-1', created_at: 102 });

    expect(collections.persistCachedCollections()).toBe(true);

    collections.resetCollections();
    expect(collections.services).toHaveLength(0);
    expect(collections.environments).toHaveLength(0);
    expect(collections.events).toHaveLength(0);

    expect(collections.hydrateCachedCollections()).toBe(true);
    expect(collections.services).toEqual([{ id: 'svc-1', name: 'Relay Service', updated_at: 100 }]);
    expect(collections.environments).toEqual([{ id: 'env-1', name: 'Production', cachedAt: 101 }]);
    expect(collections.events).toEqual([{ id: 'evt-1', created_at: 102 }]);
  });
});
