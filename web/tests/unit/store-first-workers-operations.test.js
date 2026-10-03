import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import 'fake-indexeddb/auto';
import { finalizeEvent, generateSecretKey, getPublicKey } from 'nostr-tools';
import { MockAdapter } from '@welshman/net';
import { createBahiaEventStore } from '../../src/lib/nostr/store.js';
import { createBahiaPool } from '../../src/lib/nostr/pool-welshman.js';
import {
  CASCADIA_CONTROLPLANE_STATE,
  LOOM_WORKER_ADVERTISEMENT,
  LOOM_JOB_REQUEST,
  LOOM_JOB_STATUS_UPDATE,
  LOOM_JOB_RESULT,
  DEPLOYMENT_STATUS,
  DEPLOYMENT_RESULT,
  ML_RECIPE_RUN_REQUEST,
  WORKER_STATE_TOPIC
} from '../../src/lib/nostr/kinds.gen.js';
import { createBoundedEventIdSet } from '../../src/lib/nostr/pool-utils.js';

const context = vi.hoisted(() => ({ store: null, pool: null, servicePubkey: '', relays: ['wss://workers.test'] }));
vi.mock('../../src/lib/nostr/boot.js', () => ({
  getEventStore: () => context.store,
  getPool: () => context.pool,
  getRelayUrls: () => context.relays,
  getServicePubkey: () => context.servicePubkey
}));

const workerView = await import('../../src/lib/stores/collections/workers.svelte.js');
const operationView = await import('../../src/lib/stores/collections/operations.svelte.js');
const serviceKey = generateSecretKey();
const servicePubkey = getPublicKey(serviceKey);
let prefixCounter = 0;

function signed(key, kind, tags, content = '', created_at = Math.floor(Date.now() / 1000)) {
  return finalizeEvent({ kind, tags, content: typeof content === 'string' ? content : JSON.stringify(content), created_at }, key);
}

function state(key, workerPubkey, status, createdAt, deleted = false) {
  return signed(key, CASCADIA_CONTROLPLANE_STATE, [
    ['d', `worker:state:${workerPubkey}`], ['t', WORKER_STATE_TOPIC], ['deleted', String(deleted)]
  ], { worker_pubkey: workerPubkey, name: 'Worker One', status, deleted }, createdAt);
}

beforeEach(async () => {
  context.servicePubkey = servicePubkey;
  context.store = createBahiaEventStore({ servicePubkeyPrefix: `w2s2${++prefixCounter}` });
  await context.store.open();
  context.pool = { subscribe: vi.fn(() => ({ unsubscribe: vi.fn() })) };
  workerView.resetWorkers();
  operationView.resetOperations();
});

afterEach(async () => {
  workerView.teardownWorkerStoreBinding();
  operationView.teardownOperationStoreBinding();
  context.pool.destroy?.();
  await context.store.close();
});

describe('worker and operation store queries', () => {
  it('hydrates workers before network, follows live latest-wins, and removes a tombstone', () => {
    const workerPubkey = getPublicKey(generateSecretKey());
    const now = Math.floor(Date.now() / 1000);
    expect(context.store.ingest(state(serviceKey, workerPubkey, 'online', now - 3))).toBe(true);
    workerView.initWorkerStoreBinding();
    expect(workerView.workers).toHaveLength(1);
    expect(workerView.workers[0].status).toBe('online');

    expect(context.store.ingest(state(serviceKey, workerPubkey, 'cordoned', now - 1))).toBe(true);
    expect(context.store.ingest(state(serviceKey, workerPubkey, 'stale', now - 2))).toBe(false);
    workerView.refreshWorkers();
    expect(workerView.workers[0].status).toBe('cordoned');

    expect(context.store.ingest(state(serviceKey, workerPubkey, 'cordoned', now, true))).toBe(true);
    workerView.refreshWorkers();
    expect(workerView.workers).toHaveLength(0);
  });

  it('projects worker-authored adverts and Loom job timeline from the store', () => {
    const workerKey = generateSecretKey();
    const workerPubkey = getPublicKey(workerKey);
    const clientKey = generateSecretKey();
    const now = Math.floor(Date.now() / 1000);
    workerView.initWorkerStoreBinding();
    expect(context.store.ingest(signed(workerKey, LOOM_WORKER_ADVERTISEMENT, [], { name: 'GPU Worker' }, now - 3))).toBe(true);
    const request = signed(clientKey, LOOM_JOB_REQUEST, [['p', workerPubkey], ['cmd', 'deploy']], '', now - 2);
    expect(context.store.ingest(request)).toBe(true);
    expect(context.store.ingest(signed(workerKey, LOOM_JOB_STATUS_UPDATE, [['d', request.id], ['e', request.id], ['status', 'running']], 'started', now - 1))).toBe(true);
    expect(context.store.ingest(signed(workerKey, LOOM_JOB_RESULT, [['e', request.id], ['success', 'true']], '', now))).toBe(true);
    workerView.refreshWorkers();
    expect(workerView.workers).toEqual([expect.objectContaining({ pubkey: workerPubkey, name: 'GPU Worker' })]);
    expect(workerView.workerJobsForPubkey(workerView.workerJobs, workerPubkey)).toEqual([
      expect.objectContaining({ job_id: request.id, status: 'completed', terminal: true })
    ]);
  });

  it('removes service-authored worker state on a NIP-09 coordinate deletion', () => {
    const workerPubkey = getPublicKey(generateSecretKey());
    const now = Math.floor(Date.now() / 1000);
    workerView.initWorkerStoreBinding();
    const event = state(serviceKey, workerPubkey, 'online', now - 1);
    expect(context.store.ingest(event)).toBe(true);
    workerView.refreshWorkers();
    expect(workerView.workers).toHaveLength(1);
    const deletion = signed(serviceKey, 5, [['a', `${event.kind}:${servicePubkey}:worker:state:${workerPubkey}`]], '', now);
    expect(context.store.ingest(deletion)).toBe(true);
    workerView.refreshWorkers();
    expect(workerView.workers).toHaveLength(0);
  });

  it('removes a worker-authored advert only when that worker signs the deletion', () => {
    const workerKey = generateSecretKey();
    const workerPubkey = getPublicKey(workerKey);
    const now = Math.floor(Date.now() / 1000);
    workerView.initWorkerStoreBinding();
    const advert = signed(workerKey, LOOM_WORKER_ADVERTISEMENT, [], { name: 'Ephemeral Worker' }, now - 1);
    expect(context.store.ingest(advert)).toBe(true);
    workerView.refreshWorkers();
    expect(workerView.workers).toHaveLength(1);
    expect(context.store.ingest(signed(serviceKey, 5, [['e', advert.id]], '', now))).toBe(true);
    workerView.refreshWorkers();
    expect(workerView.workers).toHaveLength(1);
    expect(context.store.ingest(signed(workerKey, 5, [['e', advert.id]], '', now + 1))).toBe(true);
    workerView.refreshWorkers();
    expect(workerView.workers).toHaveLength(0);
    expect(workerPubkey).toBe(advert.pubkey);
  });

  it('keeps assignment and drain on distinct family coordinates and ignores legacy bare d', () => {
    const workerPubkey = getPublicKey(generateSecretKey());
    const now = Math.floor(Date.now() / 1000);
    workerView.initWorkerStoreBinding();
    const assignment = signed(serviceKey, CASCADIA_CONTROLPLANE_STATE,
      [['d', `worker:assignment:${workerPubkey}`], ['t', 'worker-assignment']],
      { worker_pubkey: workerPubkey, active_assignments: [{ workload_id: 'svc-1' }] }, now - 3);
    const drain = signed(serviceKey, CASCADIA_CONTROLPLANE_STATE,
      [['d', `worker:drain:${workerPubkey}`], ['t', 'worker-drain']],
      { worker_pubkey: workerPubkey, scheduling_state: 'draining' }, now - 2);
    const legacy = signed(serviceKey, CASCADIA_CONTROLPLANE_STATE,
      [['d', workerPubkey], ['t', 'worker-assignment']],
      { worker_pubkey: workerPubkey, active_assignments: [] }, now - 1);
    for (const event of [drain, assignment, legacy]) expect(context.store.ingest(event)).toBe(true);
    workerView.refreshWorkers();
    expect(workerView.workerAssignments).toEqual([expect.objectContaining({ active_assignments: [{ workload_id: 'svc-1' }] })]);
    expect(workerView.workerDrainStatuses).toEqual([expect.objectContaining({ scheduling_state: 'draining' })]);
  });

  it('derives an operation timeline and replays it after a kind-5 result deletion', () => {
    operationView.initOperationStoreBinding();
    const now = Math.floor(Date.now() / 1000);
    const requestId = '1'.repeat(64);
    const status = signed(serviceKey, DEPLOYMENT_STATUS, [['e', requestId], ['status', 'running']], { message: 'starting' }, now - 2);
    const result = signed(serviceKey, DEPLOYMENT_RESULT, [['e', requestId], ['status', 'success']], { message: 'done' }, now - 1);
    expect(context.store.ingest(status)).toBe(true);
    expect(context.store.ingest(result)).toBe(true);
    operationView.refreshOperations();
    expect(operationView.operations).toEqual([expect.objectContaining({ request_event_id: requestId, status: 'success', terminal: true })]);
    expect(context.store.ingest(signed(serviceKey, 5, [['e', result.id]], '', now))).toBe(true);
    operationView.refreshOperations();
    expect(operationView.operations).toEqual([expect.objectContaining({ request_event_id: requestId, status: 'running' })]);
  });

  it('replaces an addressable operation request without leaving its old timeline row', () => {
    operationView.initOperationStoreBinding();
    const now = Math.floor(Date.now() / 1000);
    const tags = [['d', 'recipe-run-1']];
    const older = signed(serviceKey, ML_RECIPE_RUN_REQUEST, tags, { message: 'first' }, now - 2);
    const newer = signed(serviceKey, ML_RECIPE_RUN_REQUEST, tags, { message: 'second' }, now - 1);
    expect(context.store.ingest(older)).toBe(true);
    expect(context.store.ingest(newer)).toBe(true);
    operationView.refreshOperations();
    expect(operationView.operations).toEqual([expect.objectContaining({ request_event_id: newer.id, message: 'second' })]);
  });

  it('uses one open-author advertisement REQ for any number of workers', async () => {
    const sent = [];
    let resolveRequests;
    const requests = new Promise((resolve) => { resolveRequests = resolve; });
    context.pool = createBahiaPool({
      store: context.store,
      getAdapter: (url) => new MockAdapter(url, (message) => {
        sent.push(message);
        if (sent.filter((item) => item[0] === 'REQ').length === 4) resolveRequests();
      })
    });
    workerView.initWorkerStoreBinding();
    workerView.initWorkerStoreBinding();
    await requests;
    for (let index = 0; index < 12; index++) {
      const key = generateSecretKey();
      context.store.ingest(signed(key, LOOM_WORKER_ADVERTISEMENT, [], { name: `Worker ${index}` }));
    }
    workerView.refreshWorkers();
    expect(workerView.workers).toHaveLength(12);
    const advertReqs = sent.filter((item) => item[0] === 'REQ' && item.slice(2).some((filter) => filter.kinds?.includes(LOOM_WORKER_ADVERTISEMENT)));
    expect(advertReqs).toHaveLength(1);
    expect(sent.filter((item) => item[0] === 'REQ')).toHaveLength(4);
    expect(advertReqs[0][2].authors).toBeUndefined();
  });
});

it('bounds the seen-event LRU and refreshes recency on duplicate IDs', () => {
  const ids = createBoundedEventIdSet(3);
  ids.add('a'); ids.add('b'); ids.add('c');
  expect(ids.has('a')).toBe(true);
  ids.add('d');
  expect(ids.size).toBe(3);
  expect(ids.has('b')).toBe(false);
  expect(ids.has('a')).toBe(true);
});
