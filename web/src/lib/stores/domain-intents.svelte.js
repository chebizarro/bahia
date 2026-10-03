import { authState, signWithAuth } from './auth.js';
import { orgRoles } from './auth-roles.svelte.js';
import { currentSystemInfo } from './system.svelte.js';
import { boot, getEventStore, getPool, getRelayUrls, getServicePubkey } from '../nostr/boot.js';
import { toWebSocketUrl } from '../nostr/pool-utils.js';
import { signIntent } from '../nostr/intent-signer.js';
import { createIntentOutbox } from '../nostr/outbox.js';
import { createPendingIntents } from './pending-intents.svelte.js';

export const domainIntentState = $state({ rows: [] });

// ParseIntent requires a non-nil org UUID even for FleetScopedHandler domains;
// their authorization does not consult that tag. Fleet operators need not be org members.
export const FLEET_INTENT_ORG_ID = 'f1e7f1e7-f1e7-51e7-a11e-f1e7f1e7f1e7';

let active = null;
let opening = null;

function tag(event, name) { return event?.tags?.find(value => value[0] === name)?.[1] || ''; }

export function resolveIntentOrgId(domain, explicit = '', discovered = '', profile = '', memberships = []) {
  return explicit || discovered || profile || memberships[0] ||
    (domain === 'backup' || domain === 'package' ? FLEET_INTENT_ORG_ID : '');
}

function watchRow(state, row) {
  if (row.status !== 'pending' || state.handles.has(row.key)) return;
  const handle = state.pool.subscribe({
    relays: state.relays,
    filters: [
      { kinds: [30315], authors: [state.servicePubkey], '#p': [state.requesterPubkey],
        '#d': [`intent-status:${state.requesterPubkey}:${row.coordinate}`], limit: 1 },
      { kinds: [30900], authors: [state.servicePubkey], '#d': [row.coordinate], limit: 1 }
    ]
  });
  state.handles.set(row.key, handle);
}

async function openWorkflow() {
  await boot();
  const pool = getPool();
  const store = getEventStore();
  const servicePubkey = getServicePubkey();
  const requesterPubkey = authState.pubkey;
  const relays = [...new Set(getRelayUrls().map(toWebSocketUrl).filter(Boolean))];
  if (!pool || !store || !servicePubkey || !requesterPubkey || !relays.length) {
    throw new Error('Signed intents need an authenticated signer, trusted service, and relay pool');
  }
  const identity = `${servicePubkey}:${requesterPubkey}`;
  if (active?.identity === identity) return active;
  if (active) {
    active.unsubscribe(); active.outbox.close(); active.pending.close();
    for (const handle of active.handles.values()) handle.unsubscribe();
  }
  const namespace = `${servicePubkey.slice(0, 8)}-${requesterPubkey.slice(0, 8)}`;
  const pending = createPendingIntents({ namespace, servicePubkey, requesterPubkey });
  const state = { identity, pool, store, servicePubkey, requesterPubkey, relays, pending,
    handles: new Map(), outbox: null, unsubscribe: () => {} };
  await pending.open();
  const refresh = () => {
    domainIntentState.rows = pending.query();
    for (const [key, handle] of state.handles) {
      if (!domainIntentState.rows.some(row => row.key === key && row.status === 'pending')) {
        handle.unsubscribe(); state.handles.delete(key);
      }
    }
  };
  const unsubscribePending = pending.subscribe(refresh);
  const unsubscribeEvents = store.subscribe({ kinds: [30315, 30900], authors: [servicePubkey] }, async event => {
    if (event.kind === 30315) await pending.handleStatus(event);
    else await pending.handleCanonical(event);
  });
  state.unsubscribe = () => { unsubscribePending(); unsubscribeEvents(); };
  state.outbox = createIntentOutbox({ namespace, pool, relays, onStateChange: entry => {
    if (entry.state === 'failed') {
      const intentId = tag(entry.event, 'intent_id');
      void pending.setFailed(intentId, Object.values(entry.relays).map(value => value.message).filter(Boolean).join('; '));
    }
  } });
  await state.outbox.open();
  active = state;
  refresh();
  for (const row of pending.query({ status: 'pending' })) watchRow(state, row);
  return state;
}

/** Sign once, persist the UI overlay, then let relay OK and 30315 drive resolution. */
export async function publishDomainIntent({ domain, op, coordinate, orgId, content, intentId, currentRecord, expectedUpdatedAt }) {
  if (!opening) opening = openWorkflow().finally(() => { opening = null; });
  const state = await opening;
  const resolvedOrgId = resolveIntentOrgId(domain, orgId || content?.org_id,
    currentSystemInfo()?.organization_id, authState.profile?.org_id, Object.keys(orgRoles));
  if (!resolvedOrgId) throw new Error('An organization ID is required to sign an intent');
  const signer = { getPublicKey: async () => authState.pubkey, signEvent: signWithAuth };
  const signed = await signIntent({ domain, op, coordinate, orgId: resolvedOrgId, content,
    intentId, currentRecord, expectedUpdatedAt }, signer);
  const row = await state.pending.add({ event: signed.event, domain, op, desiredState: content });
  watchRow(state, row);
  await state.outbox.enqueue(signed.event);
  return { pending: true, intentId: signed.intentId, coordinate, event: signed.event, desiredState: content };
}
