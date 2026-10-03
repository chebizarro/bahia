import { onStoreRefresh } from '$lib/nostr/boot.js';
import { CP_STATE_TOPICS, PAYMENT_RECORD } from '$lib/nostr/kinds.gen.js';
import { onContentKeyChange } from '$lib/stores/auth-roles.svelte.js';
import { readConfidentialTopic } from './collections/confidential-records.js';

export const paymentHistoryState = $state({ records: [], loading: false, error: null, loadedWorker: '', unreadable: false });
let subscribedPaymentQuery = { worker: '', limit: 50 };
let unsubscribeRefresh = null;
let unsubscribeKeys = null;

export function paymentRecordsSnapshot() {
  return readConfidentialTopic(CP_STATE_TOPICS.PAYMENT_RECORD, PAYMENT_RECORD);
}

export function requestPaymentHistoryRecords({ worker, limit = 50 } = {}) {
  const workerPubkey = String(worker || '').trim();
  if (!workerPubkey) return [];
  const { rows } = paymentRecordsSnapshot();
  return rows.filter((record) => record.worker_pubkey === workerPubkey)
    .sort((a, b) => String(b.created_at || '').localeCompare(String(a.created_at || '')))
    .slice(0, Number(limit) || 50);
}

export function resetPaymentHistory() {
  paymentHistoryState.records = [];
  paymentHistoryState.loading = false;
  paymentHistoryState.error = null;
  paymentHistoryState.loadedWorker = '';
  paymentHistoryState.unreadable = false;
}

export function loadPaymentHistory({ worker, limit = 50 } = {}) {
  const workerPubkey = String(worker || '').trim();
  subscribedPaymentQuery = { worker: workerPubkey, limit: Number(limit) || 50 };
  if (!workerPubkey) { resetPaymentHistory(); return []; }
  const { rows, unreadable } = paymentRecordsSnapshot();
  paymentHistoryState.records = rows.filter((record) => record.worker_pubkey === workerPubkey)
    .sort((a, b) => String(b.created_at || '').localeCompare(String(a.created_at || '')))
    .slice(0, subscribedPaymentQuery.limit);
  paymentHistoryState.loadedWorker = workerPubkey;
  paymentHistoryState.unreadable = unreadable > 0;
  paymentHistoryState.error = null;
  return paymentHistoryState.records;
}

export function refreshPaymentHistory(query = subscribedPaymentQuery) { return loadPaymentHistory(query); }
export async function subscribeToPaymentHistoryUpdates(query = {}) {
  subscribedPaymentQuery = { worker: String(query.worker || subscribedPaymentQuery.worker || '').trim(),
    limit: Number(query.limit || subscribedPaymentQuery.limit) || 50 };
  if (!unsubscribeRefresh) unsubscribeRefresh = onStoreRefresh(() => refreshPaymentHistory());
  if (!unsubscribeKeys) unsubscribeKeys = onContentKeyChange(() => refreshPaymentHistory());
  refreshPaymentHistory();
  return unsubscribeFromPaymentHistoryUpdates;
}
export function unsubscribeFromPaymentHistoryUpdates() {
  unsubscribeRefresh?.(); unsubscribeRefresh = null;
  unsubscribeKeys?.(); unsubscribeKeys = null;
}
export function resetPaymentHistoryStore() { unsubscribeFromPaymentHistoryUpdates(); resetPaymentHistory(); }
