import { describe, it, expect, beforeEach, vi } from 'vitest';
import { CAS_CONTROL_STATE, CP_STATE_TOPICS, BAHIA_STATE_SCHEMAS } from '../../src/lib/nostr/kinds.gen.js';

// Mock boot.js to return a controllable store
const mockEvents = vi.hoisted(() => ({ events: [] }));

const bootMock = vi.hoisted(() => ({
  getEventStore: vi.fn(() => ({
    query: vi.fn((filter) => {
      // Return events matching the topic filter
      const topics = filter['#t'] || [];
      return mockEvents.events.filter(e => {
        const eventTopics = (e.tags || []).filter(t => t[0] === 't').map(t => t[1]);
        return topics.some(t => eventTopics.includes(t));
      });
    }),
  })),
  onStoreRefresh: vi.fn(() => () => {}),
}));

vi.mock('../../src/lib/nostr/boot.js', () => bootMock);

function makeEvent({ id, schema, topic, content, created_at = 100, pubkey = 'b'.repeat(64), deleted = false }) {
  const tags = [
    ['d', id],
    ['domain', 'service'],
    ['schema', schema],
    ['t', topic],
    ['deleted', String(deleted)],
  ];
  return {
    id: `evt-${id}`,
    kind: CAS_CONTROL_STATE,
    pubkey,
    created_at,
    tags,
    content: JSON.stringify({ ...content, id }),
  };
}

describe('store-first services', () => {
  let services, serviceMap, rebuildServicesFromStore, initServiceStoreBinding, teardownServiceStoreBinding, resetServices;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    mockEvents.events = [];

    const mod = await import('../../src/lib/stores/collections/services.svelte.js');
    services = mod.services;
    serviceMap = mod.serviceMap;
    rebuildServicesFromStore = mod.rebuildServicesFromStore;
    initServiceStoreBinding = mod.initServiceStoreBinding;
    teardownServiceStoreBinding = mod.teardownServiceStoreBinding;
    resetServices = mod.resetServices;
    resetServices();
  });

  it('rebuilds services from BahiaEventStore events', () => {
    mockEvents.events = [
      makeEvent({ id: 'svc-1', schema: BAHIA_STATE_SCHEMAS.SERVICE_REGISTRY, topic: CP_STATE_TOPICS.SERVICE_REGISTRY, content: { name: 'Alpha' } }),
      makeEvent({ id: 'svc-2', schema: BAHIA_STATE_SCHEMAS.SERVICE_REGISTRY, topic: CP_STATE_TOPICS.SERVICE_REGISTRY, content: { name: 'Beta' } }),
    ];

    rebuildServicesFromStore();

    expect(services).toHaveLength(2);
    expect(services[0].name).toBe('Alpha');
    expect(services[1].name).toBe('Beta');
    expect(serviceMap.size).toBe(2);
  });

  it('renders empty when store has no matching events', () => {
    mockEvents.events = [];

    rebuildServicesFromStore();

    expect(services).toHaveLength(0);
  });

  it('does nothing when store is null', () => {
    bootMock.getEventStore.mockReturnValueOnce(null);

    rebuildServicesFromStore();

    expect(services).toHaveLength(0);
  });

  it('initServiceStoreBinding calls rebuild and registers for refresh', () => {
    mockEvents.events = [
      makeEvent({ id: 'svc-init', schema: BAHIA_STATE_SCHEMAS.SERVICE_REGISTRY, topic: CP_STATE_TOPICS.SERVICE_REGISTRY, content: { name: 'Init Service' } }),
    ];

    initServiceStoreBinding();

    expect(services).toHaveLength(1);
    expect(services[0].name).toBe('Init Service');
    expect(bootMock.onStoreRefresh).toHaveBeenCalledOnce();
  });

  it('teardown unsubscribes from refresh', () => {
    const unsub = vi.fn();
    bootMock.onStoreRefresh.mockReturnValueOnce(unsub);

    initServiceStoreBinding();
    teardownServiceStoreBinding();

    expect(unsub).toHaveBeenCalledOnce();
  });
});

describe('store-first environments', () => {
  let environments, environmentMap, rebuildEnvironmentsFromStore, initEnvironmentStoreBinding, teardownEnvironmentStoreBinding, resetEnvironments;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    mockEvents.events = [];

    const mod = await import('../../src/lib/stores/collections/environments.svelte.js');
    environments = mod.environments;
    environmentMap = mod.environmentMap;
    rebuildEnvironmentsFromStore = mod.rebuildEnvironmentsFromStore;
    initEnvironmentStoreBinding = mod.initEnvironmentStoreBinding;
    teardownEnvironmentStoreBinding = mod.teardownEnvironmentStoreBinding;
    resetEnvironments = mod.resetEnvironments;
    resetEnvironments();
  });

  it('rebuilds environments from BahiaEventStore events', () => {
    mockEvents.events = [
      makeEvent({ id: 'env-1', schema: BAHIA_STATE_SCHEMAS.ENVIRONMENT_REGISTRY, topic: CP_STATE_TOPICS.ENVIRONMENT_REGISTRY, content: { name: 'production' } }),
      makeEvent({ id: 'env-2', schema: BAHIA_STATE_SCHEMAS.ENVIRONMENT_REGISTRY, topic: CP_STATE_TOPICS.ENVIRONMENT_REGISTRY, content: { name: 'staging' } }),
    ];

    rebuildEnvironmentsFromStore();

    expect(environments).toHaveLength(2);
    expect(environments.map(e => e.name).sort()).toEqual(['production', 'staging']);
    expect(environmentMap.size).toBe(2);
  });

  it('initEnvironmentStoreBinding calls rebuild and registers for refresh', () => {
    mockEvents.events = [
      makeEvent({ id: 'env-init', schema: BAHIA_STATE_SCHEMAS.ENVIRONMENT_REGISTRY, topic: CP_STATE_TOPICS.ENVIRONMENT_REGISTRY, content: { name: 'dev' } }),
    ];

    initEnvironmentStoreBinding();

    expect(environments).toHaveLength(1);
    expect(environments[0].name).toBe('dev');
    expect(bootMock.onStoreRefresh).toHaveBeenCalledOnce();
  });
});
