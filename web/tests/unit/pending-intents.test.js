import 'fake-indexeddb/auto';
import { describe, expect, it } from 'vitest';
import { createPendingIntents } from '../../src/lib/stores/pending-intents.svelte.js';

const service = 'a'.repeat(64);
const requester = 'b'.repeat(64);
let sequence = 0;
const makeStore = () => createPendingIntents({ namespace: `test-${++sequence}`, servicePubkey: service,
  requesterPubkey: requester, now: () => 1_000_000 });
const intent = { id: 'event-id', kind: 30900, created_at: 100, tags: [['d', 'service:one'], ['intent_id', 'intent-one']] };
const status = (name, reason = '') => ({ kind: 30315, pubkey: service,
  tags: [['d', `intent-status:${requester}:service:one`], ['p', requester],
    ['intent_id', 'intent-one'], ['status', name]], content: JSON.stringify({ reason }) });

describe('pending intent overlay', () => {
  it('persists pending state and age without timeout failure', async () => {
    const store = makeStore(); await store.open();
    const row = await store.add({ event: intent, domain: 'service', op: 'create', desiredState: { name: 'one' } });
    expect(store.age(row)).toBe(900_000);
    expect(store.query()[0].status).toBe('pending');
    await store.handleCanonical({ kind: 30900, pubkey: service, created_at: 99, tags: [['d', 'service:one']] });
    expect(store.query()[0].status).toBe('pending');
    store.close();
  });

  it('resolves by matching 30315, retaining conflict and rejection reasons', async () => {
    const store = makeStore(); await store.open();
    await store.add({ event: intent, domain: 'service' });
    expect(await store.handleStatus({ ...status('accepted'), pubkey: requester })).toBe(false);
    expect(await store.handleStatus(status('conflict', 'stale revision'))).toBe(true);
    expect(store.query()[0]).toMatchObject({ status: 'conflict', reason: 'stale revision' });
    store.close();
  });

  it('clears on accepted status or newer canonical state at the same coordinate', async () => {
    const store = makeStore(); await store.open();
    await store.add({ event: intent, domain: 'service' });
    await store.handleStatus(status('accepted'));
    expect(store.query()).toEqual([]);
    await store.add({ event: intent, domain: 'service' });
    expect(await store.handleCanonical({ kind: 30900, pubkey: service, created_at: 100,
      tags: [['d', 'service:one']] })).toBe(true);
    expect(store.query()).toEqual([]);
    store.close();
  });
});
