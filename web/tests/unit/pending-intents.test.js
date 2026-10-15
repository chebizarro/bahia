import 'fake-indexeddb/auto';
import { describe, expect, it } from 'vitest';
import { createPendingIntents, intentStatusCoordinates } from '../../src/lib/stores/pending-intents.svelte.js';
import { INTENT_STATUS_TOPIC } from '../../src/lib/nostr/intent-envelope.js';
import envelopes from '../fixtures/intent-status-envelopes.json';

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
    const desiredState = new Proxy({ name: 'one' }, {}); // Svelte state proxies are not structured-cloneable.
    const row = await store.add({ event: intent, domain: 'service', op: 'create', desiredState });
    expect(store.age(row)).toBe(900_000);
    expect(store.query()[0].desiredState).toEqual({ name: 'one' });
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

  it('correlates secret statuses by intent id and still reads earlier coordinate statuses', async () => {
    const store = makeStore(); await store.open();
    const coordinates = intentStatusCoordinates(requester, { domain: 'secret', coordinate: 'service:one', intentId: 'intent-one' });
    expect(coordinates).toEqual([`intent-status:${requester}:secret:intent-one`, `intent-status:${requester}:service:one`]);
    await store.add({ event: intent, domain: 'secret' });
    expect(await store.handleStatus({ ...status('rejected', 'invalid_request'), tags: [
      ['d', coordinates[0]], ['p', requester], ['intent_id', 'intent-one'], ['status', 'rejected']
    ] })).toBe(true);
    expect(store.query()[0].reason).toBe('invalid_request');
    await store.handleStatus(status('accepted'));
    expect(store.query()).toEqual([]);
    store.close();
  });

  it('matches exact Go-produced status tag envelopes for confidential outcomes', async () => {
    for (const envelope of envelopes) {
      const actor = envelope.tags.find(tag => tag[0] === 'p')[1];
      const store = createPendingIntents({ namespace: `status-envelope-${++sequence}`, servicePubkey: service,
        requesterPubkey: actor });
      await store.open();
      await store.add({ event: { kind: 30900, id: envelope.domain === 'backup' ? 'a'.repeat(64) : 'request', created_at: 100,
        tags: [['d', envelope.coordinate], ['intent_id', envelope.intent_id]] }, domain: envelope.domain });
      const filter = { kinds: [30315], '#d': intentStatusCoordinates(actor, {
        domain: envelope.domain, coordinate: envelope.coordinate, intentId: envelope.intent_id
      }), '#p': [actor], '#t': [INTENT_STATUS_TOPIC] };
      const event = { kind: 30315, pubkey: service, tags: envelope.tags, content: envelope.content || '{"reason":"denied"}' };
      expect(JSON.stringify(event.tags)).toBe(JSON.stringify(envelope.tags));
      if (envelope.content) expect(event.content).toBe(envelope.content);
      expect(filter['#d']).toContain(envelope.tags[0][1]);
      expect(filter['#p']).toContain(envelope.tags[4][1]);
      expect(filter['#t']).toContain(envelope.tags[3][1]);
      expect(await store.handleStatus(event)).toBe(true);
      expect(store.query()[0]?.status).toBe(envelope.status === 'accepted' ? undefined : envelope.status);
      store.close();
    }
  });

});
