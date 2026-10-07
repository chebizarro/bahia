import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import 'fake-indexeddb/auto';
import { finalizeEvent, generateSecretKey, getPublicKey } from 'nostr-tools';
import { createBahiaEventStore } from '../../src/lib/nostr/store.js';
import { CAS_CONTROL_STATE, CP_STATE_TOPICS } from '../../src/lib/nostr/kinds.gen.js';

const context = vi.hoisted(() => ({ store: null, pool: null, servicePubkey: '', relays: ['wss://deployments.test'] }));
vi.mock('../../src/lib/nostr/boot.js', () => ({
  getEventStore: () => context.store,
  getPool: () => context.pool,
  getRelayUrls: () => context.relays,
  getServicePubkey: () => context.servicePubkey
}));

const view = await import('../../src/lib/stores/collections/deployments.svelte.js');
const subscriptions = await import('../../src/lib/stores/collections/store-first-subscriptions.js');
const serviceKey = generateSecretKey();
const servicePubkey = getPublicKey(serviceKey);
let counter = 0;

function record(topic, id, content = {}, created_at = Math.floor(Date.now() / 1000), key = serviceKey, d = `${topic}:${id}`) {
  return finalizeEvent({ kind: CAS_CONTROL_STATE, created_at,
    tags: [['d', d], ['t', topic]], content: JSON.stringify({ id, ...content }) }, key);
}

beforeEach(async () => {
  context.servicePubkey = servicePubkey;
  context.store = createBahiaEventStore({ servicePubkeyPrefix: `w4s1${++counter}` });
  await context.store.open();
  context.pool = { subscribe: vi.fn(() => ({ unsubscribe: vi.fn() })) };
  view.resetDeployments();
});

afterEach(async () => {
  view.teardownCoreDeploymentStoreBindings();
  subscriptions.teardownStoreFirstSubscriptions();
  await context.store.close();
});

describe('Deployment views from BahiaEventStore', () => {
  it('hydrates the six store-first families from persisted events before any relay REQ', () => {
    const families = [
      [CP_STATE_TOPICS.LLM_ROUTE, view.llmRoutes],
      [CP_STATE_TOPICS.LLM_STATE, view.llmRouteStates],
      [CP_STATE_TOPICS.ARTIFACT_REGISTRY, view.artifacts],
      [CP_STATE_TOPICS.BUILD_REGISTRY, view.builds],
      [CP_STATE_TOPICS.DEPLOYMENT_INTENT, view.deploymentIntents],
      [CP_STATE_TOPICS.DEPLOYMENT_RUN, view.deploymentRuns]
    ];
    for (const [topic] of families) expect(context.store.ingest(record(topic, `${topic}-1`))).toBe(true);
    view.initCoreDeploymentStoreBindings();
    for (const [, rows] of families) expect(rows).toHaveLength(1);
    expect(context.pool.subscribe).not.toHaveBeenCalled();
  });

  it('updates live records and removes a deleted coordinate without a collection refresh cascade', async () => {
    const now = Math.floor(Date.now() / 1000);
    const first = record(CP_STATE_TOPICS.ARTIFACT_REGISTRY, 'artifact-1', { name: 'old' }, now - 2);
    expect(context.store.ingest(first)).toBe(true);
    view.initCoreDeploymentStoreBindings();
    expect(view.artifacts[0].name).toBe('old');
    const newer = record(CP_STATE_TOPICS.ARTIFACT_REGISTRY, 'artifact-1', { name: 'new' }, now - 1);
    expect(context.store.ingest(newer)).toBe(true);
    await vi.waitFor(() => expect(view.artifacts[0].name).toBe('new'));
    const deletion = finalizeEvent({ kind: 5, created_at: now,
      tags: [['a', `${CAS_CONTROL_STATE}:${servicePubkey}:artifact-registry:artifact-1`]], content: '' }, serviceKey);
    expect(context.store.ingest(deletion)).toBe(true);
    await vi.waitFor(() => expect(view.artifacts).toHaveLength(0));
  });

  it('ignores state from a pubkey other than the trusted service', () => {
    view.initCoreDeploymentStoreBindings();
    const attacker = generateSecretKey();
    expect(context.store.ingest(record(CP_STATE_TOPICS.LLM_ROUTE, 'rogue', {}, Math.floor(Date.now() / 1000), attacker))).toBe(true);
    expect(view.llmRoutes).toHaveLength(0);
  });

  it('prefers the newer domain version across distinct deployment-intent coordinates', async () => {
    const now = Math.floor(Date.now() / 1000);
    const newerDomain = record(CP_STATE_TOPICS.DEPLOYMENT_INTENT, 'intent-1', { updated_at: '2026-10-03T12:00:00Z', status: 'approved' }, now - 2, serviceKey, 'intent:original');
    const olderDomain = record(CP_STATE_TOPICS.DEPLOYMENT_INTENT, 'intent-1', { updated_at: '2026-10-02T12:00:00Z', status: 'pending' }, now - 1, serviceKey, 'intent:republished');
    view.initCoreDeploymentStoreBindings();
    expect(context.store.ingest(newerDomain)).toBe(true);
    expect(context.store.ingest(olderDomain)).toBe(true);
    await vi.waitFor(() => expect(view.deploymentIntents).toEqual([expect.objectContaining({ id: 'intent-1', status: 'approved' })]));
  });

  it('uses one public service-scoped read-model REQ that includes migrated topics (plus the protected REQ)', () => {
    subscriptions.initStoreFirstSubscriptions();
    expect(context.pool.subscribe).toHaveBeenCalledTimes(2);
    const [{ filters }] = context.pool.subscribe.mock.calls[0];
    const stateFilter = filters.find((filter) => filter.kinds.includes(CAS_CONTROL_STATE));
    expect(stateFilter.authors).toEqual([servicePubkey]);
    expect(stateFilter['#t']).toEqual(expect.arrayContaining([
      CP_STATE_TOPICS.LLM_ROUTE, CP_STATE_TOPICS.LLM_STATE,
      CP_STATE_TOPICS.ARTIFACT_REGISTRY, CP_STATE_TOPICS.BUILD_REGISTRY,
      CP_STATE_TOPICS.DEPLOYMENT_INTENT, CP_STATE_TOPICS.DEPLOYMENT_RUN
    ]));
  });
});
