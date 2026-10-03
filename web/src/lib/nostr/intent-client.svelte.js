import { boot, getEventStore, getPool, getRelayUrls, getServicePubkey } from './boot.js';
import { toWebSocketUrl } from './pool-utils.js';
import { signIntent } from './intent-signer.js';
import { createIntentOutbox } from './outbox.js';
import { createPendingIntents } from '../stores/pending-intents.svelte.js';
import { authState, resolveActiveSigner, signWithAuth } from '../stores/auth.svelte.js';

export const pendingIntentRows = $state([]);

function tag(event, name) { return event?.tags?.find(item => item[0] === name)?.[1]; }

export function createIntentClient({ store, pool, servicePubkey, requesterPubkey, relays, signer,
  onPendingChange = () => {} }) {
  const namespace = `${servicePubkey}-${requesterPubkey}`;
  const pending = createPendingIntents({ namespace, servicePubkey, requesterPubkey });
  const subscriptions = new Map();
  let unsubscribeCanonical = () => {};
  let unsubscribeStatus = () => {};
  let unsubscribePending = () => {};
  const refresh = () => onPendingChange(pending.query());
  const outbox = createIntentOutbox({ namespace, pool, relays, onStateChange: entry => {
    if (entry.state === 'failed') {
      const intentId = tag(entry.event, 'intent_id');
      void pending.setFailed(intentId, Object.values(entry.relays).map(result => result.message).filter(Boolean).join('; '));
    }
  } });

  function retainStatus(coordinate) {
    const existing = subscriptions.get(coordinate);
    if (existing) { existing.refs++; return; }
    const filter = { kinds: [30315], authors: [servicePubkey], '#d': [`intent-status:${requesterPubkey}:${coordinate}`],
      '#p': [requesterPubkey], '#t': ['intent-status'] };
    const handle = pool.subscribe({ relays, filters: [filter] });
    subscriptions.set(coordinate, { refs: 1, handle });
    for (const event of store.query(filter)) void pending.handleStatus(event);
  }

  function releaseResolved(coordinate) {
    const active = pending.query({ coordinate, status: 'pending' });
    const existing = subscriptions.get(coordinate);
    if (!existing || active.length) return;
    existing.handle.unsubscribe();
    subscriptions.delete(coordinate);
  }

  return {
    pending,
    outbox,
    async open() {
      await pending.open();
      unsubscribePending = pending.subscribe(refresh);
      refresh();
      unsubscribeCanonical = store.subscribe({ kinds: [30900], authors: [servicePubkey] }, event => {
        void pending.handleCanonical(event).then(resolved => { if (resolved) releaseResolved(tag(event, 'd')); });
      });
      unsubscribeStatus = store.subscribe({ kinds: [30315], authors: [servicePubkey], '#p': [requesterPubkey],
        '#t': ['intent-status'] }, event => {
        void pending.handleStatus(event).then(resolved => { if (resolved) releaseResolved(tag(event, 'd')?.split(':').slice(2).join(':')); });
      });
      for (const row of pending.query({ status: 'pending' })) retainStatus(row.coordinate);
      await outbox.open();
    },
    close() {
      unsubscribeCanonical(); unsubscribeStatus(); unsubscribePending();
      for (const { handle } of subscriptions.values()) handle.unsubscribe();
      subscriptions.clear(); outbox.close(); pending.close();
    },
    async submit({ domain, op, coordinate, orgId, content, currentRecord, expectedUpdatedAt, intentId }) {
      const signed = await signIntent({ domain, op, coordinate, orgId, content, currentRecord,
        expectedUpdatedAt, intentId }, signer);
      retainStatus(coordinate);
      await pending.add({ event: signed.event, domain, op, desiredState: content });
      store.ingest(signed.event);
      await outbox.enqueue(signed.event);
      return { ...signed, pending: true };
    }
  };
}

let active = null;
let opening = null;

async function currentClient() {
  await boot();
  const requesterPubkey = authState.pubkey;
  const servicePubkey = getServicePubkey();
  const store = getEventStore();
  const pool = getPool();
  const relays = [...new Set(getRelayUrls().map(toWebSocketUrl).filter(Boolean))];
  if (!requesterPubkey || !servicePubkey || !store || !pool || relays.length === 0) {
    throw new Error('Signing an intent requires an authenticated signer, Bahia store and relay seed');
  }
  const key = `${servicePubkey}:${requesterPubkey}:${relays.join(',')}`;
  if (active?.key === key) return active.client;
  if (opening?.key === key) return opening.promise;
  active?.client.close();
  pool.setSign(signWithAuth);
  const client = createIntentClient({ store, pool, servicePubkey, requesterPubkey, relays,
    signer: resolveActiveSigner(), onPendingChange: rows => pendingIntentRows.splice(0, pendingIntentRows.length, ...rows) });
  const promise = client.open().then(() => { active = { key, client }; return client; });
  opening = { key, promise };
  try { return await promise; } finally { opening = null; }
}

/** Sign and enqueue a full desired-state intent; never wait for daemon completion. */
export async function publishIntent(request) {
  return (await currentClient()).submit(request);
}

export async function resumeIntentClient() { return currentClient(); }

export function canonicalIntentRecord(coordinate) {
  const event = getEventStore()?.query({ kinds: [30900], authors: [getServicePubkey()], '#d': [coordinate] })?.[0];
  if (!event) return null;
  return { event, content: JSON.parse(event.content) };
}

export function stopIntentClient() {
  active?.client.close(); active = null; opening = null;
  pendingIntentRows.splice(0);
}
