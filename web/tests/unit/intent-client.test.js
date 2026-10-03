import 'fake-indexeddb/auto';
import { describe, expect, it } from 'vitest';
import { finalizeEvent, getPublicKey } from 'nostr-tools';
import { createIntentClient } from '../../src/lib/nostr/intent-client.svelte.js';

const serviceSecret = new Uint8Array(32).fill(9);
const requesterSecret = new Uint8Array(32).fill(8);
const servicePubkey = getPublicKey(serviceSecret);
const requesterPubkey = getPublicKey(requesterSecret);
const orgId = '3b45458b-2724-4dda-9fc6-66f12249660d';
let sequence = 0;

function fixture() {
  const subscriptions = [];
  const poolSubscriptions = [];
  const store = {
    query: () => [],
    subscribe: (filter, callback) => {
      const sub = { filter, callback }; subscriptions.push(sub);
      return () => subscriptions.splice(subscriptions.indexOf(sub), 1);
    },
    ingest: event => { for (const sub of subscriptions) sub.callback(event); return true; }
  };
  const pool = {
    subscribe: options => { poolSubscriptions.push(options); return { unsubscribe() {} }; },
    getConnectedRelays: () => [],
    onRelayReady: () => () => {},
    publishEvent: async () => ({})
  };
  const signer = { getPublicKey: async () => requesterPubkey,
    signEvent: async event => finalizeEvent(event, requesterSecret) };
  const rows = [];
  const client = createIntentClient({ store, pool, servicePubkey, requesterPubkey,
    relays: [`wss://test-${++sequence}.example`], signer, onPendingChange: pending => rows.push([...pending]) });
  return { client, store, poolSubscriptions, rows };
}

function status(intent, state, reason = '') {
  return finalizeEvent({ kind: 30315, created_at: Math.floor(Date.now() / 1000),
    tags: [['d', `intent-status:${requesterPubkey}:${intent.coordinate}`], ['p', requesterPubkey],
      ['t', 'intent-status'], ['intent_id', intent.intentId], ['status', state]],
    content: JSON.stringify({ reason }) }, serviceSecret);
}

function untilPending(client, predicate) {
  return new Promise(resolve => {
    const unsubscribe = client.pending.subscribe(() => {
      if (predicate(client.pending.query())) { unsubscribe(); resolve(); }
    });
  });
}

describe('intent client wiring', () => {
  it('retains a coordinate-scoped status REQ and clears on accepted', async () => {
    const { client, store, poolSubscriptions } = fixture(); await client.open();
    const intent = await client.submit({ domain: 'service', op: 'create', coordinate: 'service-one',
      orgId, content: { id: 'service-one', name: 'one' } });
    expect(client.pending.query()[0].status).toBe('pending');
    expect(poolSubscriptions).toHaveLength(1);
    expect(poolSubscriptions[0].filters[0]['#d']).toEqual([`intent-status:${requesterPubkey}:service-one`]);
    expect(poolSubscriptions[0].filters[1]).toMatchObject({ kinds: [30900], authors: [servicePubkey],
      '#d': ['service-one'], limit: 1 });
    const resolved = untilPending(client, rows => rows.every(row => row.coordinate !== 'service-one'));
    store.ingest(status(intent, 'accepted'));
    await resolved;
    client.close();
  });

  it('shows conflict until the user re-reads and resubmits', async () => {
    const { client, store } = fixture(); await client.open();
    const intent = await client.submit({ domain: 'environment', op: 'update', coordinate: 'environment-one',
      orgId, content: { id: 'environment-one', name: 'one' }, currentRecord: { updated_at: '2026-10-03T09:12:13Z' } });
    const conflicted = untilPending(client, rows => rows[0]?.status === 'conflict');
    store.ingest(status(intent, 'conflict', 'revision_conflict'));
    await conflicted;
    expect(client.pending.query()[0].reason).toBe('revision_conflict');
    client.close();
  });

  it('clears on newer canonical state without a status', async () => {
    const { client, store } = fixture(); await client.open();
    const intent = await client.submit({ domain: 'service', op: 'create', coordinate: 'service-two',
      orgId, content: { id: 'service-two', name: 'two' } });
    const resolved = untilPending(client, rows => rows.every(row => row.coordinate !== 'service-two'));
    store.ingest(finalizeEvent({ kind: 30900, created_at: intent.event.created_at + 1,
      tags: [['d', 'service-two']], content: '{"id":"service-two"}' }, serviceSecret));
    await resolved;
    client.close();
  });
});
