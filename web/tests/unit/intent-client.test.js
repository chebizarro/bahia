import 'fake-indexeddb/auto';
import { describe, expect, it } from 'vitest';
import { finalizeEvent, getPublicKey } from 'nostr-tools';
import { createIntentClient } from '../../src/lib/nostr/intent-client.svelte.js';
import { intentPrivateStatusCoordinate } from '../../src/lib/nostr/intent-envelope.js';

const serviceSecret = new Uint8Array(32).fill(9);
const requesterSecret = new Uint8Array(32).fill(8);
const servicePubkey = getPublicKey(serviceSecret);
const requesterPubkey = getPublicKey(requesterSecret);
const orgId = '3b45458b-2724-4dda-9fc6-66f12249660d';
let sequence = 0;

function fixture() {
  const subscriptions = [];
  const poolSubscriptions = [];
  const poolUnsubscribed = [];
  const store = {
    query: () => [],
    subscribe: (filter, callback) => {
      const sub = { filter, callback }; subscriptions.push(sub);
      return () => subscriptions.splice(subscriptions.indexOf(sub), 1);
    },
    ingest: event => { for (const sub of subscriptions) sub.callback(event); return true; }
  };
  const pool = {
    subscribe: options => { poolSubscriptions.push(options); return { unsubscribe() { poolUnsubscribed.push(options); } }; },
    getConnectedRelays: () => [],
    onRelayReady: () => () => {},
    publishEvent: async () => ({})
  };
  const signer = { getPublicKey: async () => requesterPubkey,
    signEvent: async event => finalizeEvent(event, requesterSecret) };
  const rows = [];
  const client = createIntentClient({ store, pool, servicePubkey, requesterPubkey,
    relays: [`wss://test-${++sequence}.example`], signer, onPendingChange: pending => rows.push([...pending]) });
  return { client, store, poolSubscriptions, poolUnsubscribed, rows };
}

function status(intent, state, reason = '', extra = {}, coordinate = `intent-status:${requesterPubkey}:${intent.coordinate}`) {
  return finalizeEvent({ kind: 30315, created_at: Math.floor(Date.now() / 1000),
    tags: [['d', coordinate], ['p', requesterPubkey],
      ['t', 'intent-status'], ['intent_id', intent.intentId], ['status', state],
      ...(intent.domain === 'backup' ? [['e', intent.event.id]] : [])],
    content: JSON.stringify({ reason, ...(intent.domain === 'backup' ? { intent_id: intent.intentId } : {}),
      ...(intent.domain === 'backup' && state === 'accepted' ? { coordinate: intent.coordinate } : {}), ...extra }) }, serviceSecret);
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

  it('subscribes to and resolves confidential rejection by request-correlated d tag', async () => {
    const { client, store, poolSubscriptions } = fixture(); await client.open();
    const submitted = await client.submit({ domain: 'backup', op: 'run', coordinate: 'backup-run:one',
      orgId, content: { id: 'one' } });
    const privateCoordinate = intentPrivateStatusCoordinate(requesterPubkey, 'backup', submitted.intentId);
    expect(poolSubscriptions[0].filters[0]['#d']).toEqual([privateCoordinate,
      `intent-status:${requesterPubkey}:backup-run:one`]);
    const waiting = client.waitForStatus(submitted);
    expect(await client.pending.handleStatus(status({ ...submitted, event: { id: 'wrong-request' } },
      'rejected', 'access_denied', {}, privateCoordinate))).toBe(false);
    expect(await client.pending.handleStatus(status(submitted, 'rejected', 'access_denied'))).toBe(false);
    expect(await client.pending.handleStatus(status(submitted, 'accepted', '', {}, privateCoordinate))).toBe(false);
    expect(await client.pending.handleStatus(status(submitted, 'rejected', 'access_denied',
      { coordinate: submitted.coordinate }, privateCoordinate))).toBe(false);
    store.ingest(status(submitted, 'rejected', 'access_denied', {}, privateCoordinate));
    await expect(waiting).rejects.toThrow('access_denied');
    expect(client.pending.query()[0]).toMatchObject({ status: 'rejected', reason: 'access_denied' });
    await client.pending.handleStatus(status(submitted, 'accepted'));
    expect(client.pending.query()).toEqual([]);
    client.close();
  });

  it('accepts a backup-run status after the broad reader removes its pending row', async () => {
    const { client, store } = fixture(); await client.open();
    const submitted = await client.submit({ domain: 'backup', op: 'run', coordinate: 'backup-run:accepted',
      orgId, content: { id: 'accepted' } });
    const waiting = client.waitForStatus(submitted);
    store.ingest(status(submitted, 'accepted'));
    await expect(waiting).resolves.toMatchObject({ coordinate: submitted.coordinate });
    expect(client.pending.query()).toEqual([]);
    client.close();
  });

  it('binds a pre-submit backup-run waiter to the signed event once submitted', async () => {
    const { client, store } = fixture(); await client.open();
    const request = { domain: 'backup', op: 'run', coordinate: 'backup-run:pre-submit', intentId: 'intent-pre-submit',
      orgId, content: { id: 'pre-submit' } };
    const waiting = client.waitForStatus(request);
    const submitted = await client.submit(request);
    store.ingest(status(submitted, 'accepted'));
    await expect(waiting).resolves.toMatchObject({ coordinate: request.coordinate });
    client.close();
  });

  it('resolves request data and evaluation only from a correlated accepted 30315', async () => {
    const { client, store } = fixture(); await client.open();
    const preview = await client.submit({ domain: 'deployment', op: 'preview', coordinate: 'deployment-preview:svc:env',
      orgId, content: { service_id: 'svc', environment_id: 'env' } });
    const waiting = client.waitForStatus(preview);
    store.ingest(status({ ...preview, intentId: 'other' }, 'accepted', '', { data: { desired_state_hash: 'wrong' } }));
    const expected = { desired_state_hash: 'sha256:reviewed', desired_state_summary: { image_ref: 'registry/api@sha256:abc' } };
    store.ingest(status(preview, 'accepted', '', { data: expected }));
    await expect(waiting).resolves.toMatchObject({ data: expected });
    const evaluation = await client.submit({ domain: 'policy', op: 'evaluate', coordinate: 'evaluation:artifact:env',
      orgId, content: { artifact_id: 'artifact', environment_id: 'env' } });
    const result = client.waitForStatus(evaluation);
    store.ingest(status(evaluation, 'accepted', '', { evaluation: { allowed: false, blockers: 1 } }));
    await expect(result).resolves.toMatchObject({ evaluation: { allowed: false, blockers: 1 } });
    client.close();
  });

  it('keeps the status REQ open when canonical state arrives before request status', async () => {
    const { client, store, poolUnsubscribed } = fixture(); await client.open();
    const intent = await client.submit({ domain: 'artifact', op: 'register', coordinate: 'artifact:one',
      orgId, content: { id: 'one' } });
    const waiting = client.waitForStatus(intent);
    const canonical = untilPending(client, rows => rows.length === 0);
    store.ingest(finalizeEvent({ kind: 30900, created_at: intent.event.created_at + 1,
      tags: [['d', intent.coordinate]], content: '{"id":"one"}' }, serviceSecret));
    await canonical;
    expect(poolUnsubscribed).toHaveLength(0);
    store.ingest(status(intent, 'accepted', '', { data: { artifact_id: 'one' } }));
    await expect(waiting).resolves.toMatchObject({ data: { artifact_id: 'one' } });
    expect(poolUnsubscribed).toHaveLength(1);
    client.close();
  });

  it('rejects a request status wait when relay publication fails', async () => {
    const { client } = fixture(); await client.open();
    const intent = await client.submit({ domain: 'deployment', op: 'preview',
      coordinate: 'deployment-preview:failed:env', orgId,
      content: { service_id: 'failed', environment_id: 'env' } });
    const waiting = client.waitForStatus(intent);
    await client.pending.setFailed(intent.intentId, 'relay rejected publication');
    await expect(waiting).rejects.toThrow('relay rejected publication');
    client.close();
  });

  it('shows conflict until the user re-reads and resubmits', async () => {
    const { client, store } = fixture(); await client.open();
    const intent = await client.submit({ domain: 'environment', op: 'update', coordinate: 'environment-one',
      orgId, content: { id: 'environment-one', name: 'one' }, currentRecord: { updated_at: '2026-10-03T09:12:13Z' } });
    const conflicted = untilPending(client, rows => rows.some(row =>
      row.coordinate === 'environment-one' && row.status === 'conflict'));
    store.ingest(status(intent, 'conflict', 'revision_conflict'));
    await conflicted;
    expect(client.pending.query({ coordinate: 'environment-one' })[0].reason).toBe('revision_conflict');
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
