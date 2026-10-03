import { beforeEach, describe, expect, it, vi } from 'vitest';
import { matchFilter } from 'nostr-tools/filter';
import { CP_STATE_TOPICS } from '../../src/lib/nostr/kinds.gen.js';

const SERVICE = 'b'.repeat(64);
const boot = vi.hoisted(() => ({ store: null }));
vi.mock('../../src/lib/nostr/boot.js', () => ({
  getEventStore: () => boot.store,
  getServicePubkey: () => SERVICE
}));

function event(topic, { id = 'a'.repeat(64), d = 'entity-1', content = {}, created_at = 100, pubkey = SERVICE, deleted = false } = {}) {
  return {
    id, kind: 30900, pubkey, created_at,
    tags: [['d', d], ['t', topic], ['deleted', String(deleted)]],
    content: JSON.stringify({ id: d, name: d, ...content })
  };
}

function fakeStore(initial = []) {
  const events = [...initial];
  const subscriptions = [];
  return {
    query: vi.fn(filter => events.filter(item => matchFilter(filter, item))),
    subscribe: vi.fn((filter, cb) => {
      const subscription = { filter, cb };
      subscriptions.push(subscription);
      return () => subscriptions.splice(subscriptions.indexOf(subscription), 1);
    }),
    emit(item) {
      events.push(item);
      for (const { filter, cb } of [...subscriptions]) if (matchFilter(filter, item)) cb(item);
    }
  };
}

const domains = [
  { name: 'services', topic: CP_STATE_TOPICS.SERVICE_REGISTRY, module: 'services', array: 'services', bind: 'initServiceStoreBinding', unbind: 'teardownServiceStoreBinding' },
  { name: 'environments', topic: CP_STATE_TOPICS.ENVIRONMENT_REGISTRY, module: 'environments', array: 'environments', bind: 'initEnvironmentStoreBinding', unbind: 'teardownEnvironmentStoreBinding' },
  { name: 'states', topic: CP_STATE_TOPICS.SERVICE_STATE, module: 'deployments', array: 'states', bind: 'initCoreDeploymentStoreBindings', unbind: 'teardownCoreDeploymentStoreBindings', content: { service_id: 'svc', environment_id: 'env' }, rowId: 'svc:env' },
  { name: 'policies', topic: CP_STATE_TOPICS.POLICY_REGISTRY, module: 'deployments', array: 'policies', bind: 'initCoreDeploymentStoreBindings', unbind: 'teardownCoreDeploymentStoreBindings' },
  { name: 'package repositories', topic: CP_STATE_TOPICS.PACKAGE_REPOSITORY, module: 'deployments', array: 'packageRepositories', bind: 'initCoreDeploymentStoreBindings', unbind: 'teardownCoreDeploymentStoreBindings' },
  { name: 'package artifacts', topic: CP_STATE_TOPICS.PACKAGE_ARTIFACT, module: 'deployments', array: 'packageArtifacts', bind: 'initCoreDeploymentStoreBindings', unbind: 'teardownCoreDeploymentStoreBindings' },
  { name: 'package promotions', topic: CP_STATE_TOPICS.PACKAGE_PROMOTION, module: 'deployments', array: 'packagePromotions', bind: 'initCoreDeploymentStoreBindings', unbind: 'teardownCoreDeploymentStoreBindings' }
];

beforeEach(() => {
  vi.resetModules();
  const callbacks = [];
  vi.stubGlobal('requestAnimationFrame', cb => { callbacks.push(cb); return callbacks.length; });
  vi.stubGlobal('cancelAnimationFrame', () => {});
  globalThis.flushCoreFrame = () => {
    for (const cb of callbacks.splice(0)) cb();
  };
});

describe.each(domains)('$name store query', ({ topic, module, array, bind, unbind, content = {}, rowId = 'entity-1' }) => {
  it('hydrates by topic, updates in one frame, rejects older and wrong-author events, and removes tombstones', async () => {
    const seed = event(topic, { content: { ...content, name: 'cached' } });
    boot.store = fakeStore([seed, event(CP_STATE_TOPICS.BACKUP_POLICY, { d: 'other' })]);
    const collection = await import(`../../src/lib/stores/collections/${module}.svelte.js`);
    collection[bind]();
    expect(collection[array]).toHaveLength(1);
    expect(collection[array][0]).toMatchObject({ id: rowId, name: 'cached' });
    expect(boot.store.query).toHaveBeenCalledWith(expect.objectContaining({ kinds: [30900], '#t': [topic], authors: [SERVICE] }));

    boot.store.emit(event(topic, { id: 'c'.repeat(64), content: { ...content, name: 'live' }, created_at: 101 }));
    globalThis.flushCoreFrame();
    expect(collection[array][0].name).toBe('live');
    boot.store.emit(event(topic, { id: 'd'.repeat(64), content: { ...content, name: 'stale' }, created_at: 100 }));
    boot.store.emit(event(topic, { id: 'e'.repeat(64), content: { ...content, name: 'wrong author' }, created_at: 200, pubkey: 'f'.repeat(64) }));
    globalThis.flushCoreFrame();
    expect(collection[array][0].name).toBe('live');

    boot.store.emit(event(topic, { id: 'b'.repeat(64), content: { ...content, name: 'tie winner' }, created_at: 101 }));
    globalThis.flushCoreFrame();
    expect(collection[array][0].name).toBe('tie winner');
    boot.store.emit(event(topic, { id: 'f'.repeat(64), content, created_at: 102, deleted: true }));
    globalThis.flushCoreFrame();
    expect(collection[array]).toHaveLength(0);
    collection[unbind]();
  }, 15000);

  it('removes a live coordinate on kind-5 deletion without a collection cascade', async () => {
    boot.store = fakeStore([event(topic, { content })]);
    const collection = await import(`../../src/lib/stores/collections/${module}.svelte.js`);
    collection[bind]();
    expect(collection[array]).toHaveLength(1);
    boot.store.emit({ id: '5'.repeat(64), kind: 5, pubkey: SERVICE, created_at: 103, tags: [['a', `30900:${SERVICE}:entity-1`]], content: '' });
    globalThis.flushCoreFrame();
    expect(collection[array]).toHaveLength(0);
    boot.store.emit(event(topic, { id: '1'.repeat(64), content, created_at: 102 }));
    globalThis.flushCoreFrame();
    expect(collection[array]).toHaveLength(0);
    boot.store.emit(event(topic, { id: '2'.repeat(64), content: { ...content, name: 'recreated' }, created_at: 104 }));
    globalThis.flushCoreFrame();
    expect(collection[array][0].name).toBe('recreated');
    collection[unbind]();
  }, 15000);
});

describe('exact e deletion', () => {
  it('does not delete a newer revision when an old event id is deleted', async () => {
    const topic = CP_STATE_TOPICS.SERVICE_REGISTRY;
    boot.store = fakeStore([event(topic, { id: 'a'.repeat(64), content: { name: 'old' } })]);
    const collection = await import('../../src/lib/stores/collections/services.svelte.js');
    collection.initServiceStoreBinding();
    boot.store.emit(event(topic, { id: 'b'.repeat(64), content: { name: 'new' }, created_at: 101 }));
    globalThis.flushCoreFrame();
    boot.store.emit({ id: '5'.repeat(64), kind: 5, pubkey: SERVICE, created_at: 102, tags: [['e', 'a'.repeat(64)]], content: '' });
    globalThis.flushCoreFrame();
    expect(collection.services[0].name).toBe('new');
    boot.store.emit({ id: '6'.repeat(64), kind: 5, pubkey: SERVICE, created_at: 99, tags: [['e', 'b'.repeat(64)]], content: '' });
    globalThis.flushCoreFrame();
    expect(collection.services).toHaveLength(0);
    collection.teardownServiceStoreBinding();
  }, 15000);
});
