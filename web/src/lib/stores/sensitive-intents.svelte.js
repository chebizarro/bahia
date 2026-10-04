import { authState } from './auth.svelte.js';
import { getNip07Signer } from '$lib/nostr/nip07-signer.js';
import { getNip46Signer } from '$lib/nostr/nip46.js';
import { boot, getEventStore, getPool, getRelayUrls, getServicePubkey } from '$lib/nostr/boot.js';
import { signIntent } from '$lib/nostr/intent-signer.js';
import { giftWrapIntent, sensitiveIntentBlocker } from '$lib/nostr/intent-giftwrap.js';
import { createIntentOutbox } from '$lib/nostr/outbox.js';
import { createPendingIntents } from './pending-intents.svelte.js';
import { orgRoles } from './auth-roles.svelte.js';

export const sensitivePendingState = $state({ rows: [] });
let session = null;
let sessionOpening = null;

export function sensitiveMutationBlocker() {
  return sensitiveIntentBlocker(authState.capabilities);
}

export function orgIdFor(record, relatedRecords = []) {
  const explicit = record?.org_id || record?.orgId || record?.organization_id;
  if (explicit) return explicit;
  const orgs = [...new Set([...Object.keys(orgRoles), ...relatedRecords.map(item => item?.org_id || item?.orgId).filter(Boolean)])];
  if (orgs.length === 1) return orgs[0];
  throw new Error('Select an organization before changing sensitive settings');
}

function activeSigner() {
  return authState.authMethod === 'nip46' ? getNip46Signer() : getNip07Signer();
}

async function openSession() {
  if (authState.status !== 'authenticated' || !authState.pubkey) throw new Error('Sign in to submit an intent');
  await boot();
  const servicePubkey = getServicePubkey();
  const relays = getRelayUrls();
  const pool = getPool();
  const store = getEventStore();
  if (!servicePubkey || !relays.length || !pool || !store) throw new Error('Service pubkey and relays are required for sensitive intents');
  const namespace = `${servicePubkey}-${authState.pubkey}`;
  if (session?.namespace === namespace) return session;
  session?.close();
  const pending = createPendingIntents({ namespace, servicePubkey, requesterPubkey: authState.pubkey });
  await pending.open();
  const refresh = () => { sensitivePendingState.rows = pending.query(); };
  const unsubPending = pending.subscribe(refresh);
  refresh();

  const sockets = new Map(relays.map(relay => [relay, pool.getPool().get(relay)]));
  const listeners = new Set();
  const detachSockets = [];
  for (const [relay, socket] of sockets) {
    const onStatus = status => {
      if (status === 'open') for (const listener of listeners) listener({ relay });
    };
    socket.on('status', onStatus);
    detachSockets.push(() => socket.off('status', onStatus));
    const onAuth = status => {
      if (status === 'ok') for (const listener of listeners) listener({ relay, auth: true });
    };
    socket.auth.on('status', onAuth);
    detachSockets.push(() => socket.auth.off('status', onAuth));
  }
  const deliveryPool = {
    publishEvent: args => pool.publishEvent(args),
    getConnectedRelays: () => [...sockets].filter(([, socket]) => socket.status === 'open').map(([relay]) => relay),
    onRelayReady(listener) { listeners.add(listener); return () => listeners.delete(listener); }
  };
  const outbox = createIntentOutbox({ namespace, pool: deliveryPool, relays, onStateChange: entry => {
    if (entry.state === 'failed') {
      const intentId = pending.query().find(row => row.wrapEventId === entry.id)?.intentId;
      if (intentId) void pending.setFailed(intentId, 'Every relay permanently rejected the gift wrap');
    }
  } });
  await outbox.open();
  const statusFilter = { kinds: [30315], authors: [servicePubkey], '#p': [authState.pubkey], '#t': ['intent-status'], limit: 500 };
  const unsubStatus = store.subscribe(statusFilter, event => void pending.handleStatus(event));
  for (const event of store.query(statusFilter)) await pending.handleStatus(event);
  const statusReq = pool.subscribe({ relays, filters: [statusFilter], filterKey: `intent-status:${authState.pubkey}` });
  const unsubCanonical = store.subscribe({ kinds: [30900], authors: [servicePubkey] }, event => void pending.handleCanonical(event));
  session = { namespace, pending, outbox, close() {
    unsubPending(); for (const detach of detachSockets) detach(); unsubStatus(); unsubCanonical(); statusReq.unsubscribe();
    outbox.close(); pending.close();
  } };
  return session;
}

function ensureSession() {
  if (!sessionOpening) sessionOpening = openSession().finally(() => { sessionOpening = null; });
  return sessionOpening;
}

/** Restore redacted pending metadata and resume status/outbox subscriptions. */
export async function initializeSensitiveIntents() {
  await ensureSession();
}

export async function submitSensitiveIntent({ domain, op, coordinate, orgId, content, currentRecord, expectedUpdatedAt, schema, intentId }) {
  const blocker = sensitiveMutationBlocker();
  if (blocker) throw new Error(blocker);
  const { pending, outbox } = await ensureSession();
  const signer = activeSigner();
  const { event: inner, intentId: signedIntentId } = await signIntent({ domain, op, coordinate, orgId, content, currentRecord, expectedUpdatedAt, schema, intentId }, signer);
  const wrap = await giftWrapIntent(inner, getServicePubkey(), signer);
  // Never persist plaintext sensitive desired state or the signed inner event.
  const row = await pending.add({ event: inner, domain, op, desiredState: null });
  row.wrapEventId = wrap.id;
  await outbox.enqueue(wrap);
  for (const relay of getRelayUrls()) outbox.onReconnect(relay);
  return { id: coordinate, coordinate, intentId: signedIntentId, pending: true, sensitive: true };
}

/** Resolve a gift-wrapped intent only from its signed, correlated daemon status. */
export async function waitForSensitiveIntentStatus({ coordinate, intentId }, { signal } = {}) {
  const { pending } = await ensureSession();
  const requester = authState.pubkey;
  const service = getServicePubkey();
  const filter = { kinds: [30315], authors: [service],
    '#d': [`intent-status:${requester}:${coordinate}`], '#p': [requester], '#t': ['intent-status'] };
  const store = getEventStore();
  return new Promise((resolve, reject) => {
    let finished = false;
    let unsubscribe = () => {};
    let unsubscribePending = () => {};
    const request = getPool().subscribe({ relays: getRelayUrls(), filters: [{ ...filter, limit: 1 }] });
    const finish = (value, error) => {
      if (finished) return;
      finished = true;
      unsubscribe();
      unsubscribePending();
      request.unsubscribe();
      signal?.removeEventListener('abort', abort);
      if (error) reject(error); else resolve(value);
    };
    const abort = () => finish(null, signal.reason || new Error('Intent status wait aborted'));
    const onStatus = event => {
      const tag = name => event.tags?.find(item => item[0] === name)?.[1];
      if (event.kind !== 30315 || event.pubkey !== service || tag('d') !== filter['#d'][0] ||
          tag('p') !== requester || tag('intent_id') !== intentId || tag('t') !== 'intent-status') return;
      let body;
      try { body = JSON.parse(event.content || '{}'); }
      catch { finish(null, new Error('Invalid intent status content')); return; }
      const status = tag('status') || body.status;
      if (status === 'accepted') finish({ ...body, status });
      else if (['rejected', 'conflict', 'superseded'].includes(status)) {
        finish(null, new Error(tag('reason') || body.reason || `Intent ${status}`));
      }
    };
    unsubscribe = store.subscribe(filter, onStatus);
    unsubscribePending = pending.subscribe(() => {
      const row = pending.query({ coordinate }).find(item => item.intentId === intentId);
      if (row?.status === 'failed') finish(null, new Error(row.reason || 'Intent publish failed'));
    });
    if (signal?.aborted) abort();
    else {
      signal?.addEventListener('abort', abort, { once: true });
      for (const event of store.query(filter)) onStatus(event);
    }
  });
}
