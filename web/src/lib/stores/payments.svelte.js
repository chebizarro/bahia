import { encryptedRequestsAvailable, requestEncryptedResult, servicePubkeyFromSystemInfo } from '$lib/nostr/encrypted-controlplane.js';
import { subscribeToDomainRefresh } from '$lib/nostr/retained-domain-subscription.js';
import { authState, initializeAuth } from '$lib/stores/auth.js';
import { currentSystemInfo, loadSystemInfo } from '$lib/stores/system.svelte.js';

export const paymentHistoryState = $state({
  records: [],
  loading: false,
  error: null,
  loadedWorker: ''
});

let paymentSubscription = null;
let paymentSubscriptionGeneration = 0;
let subscribedPaymentQuery = { worker: '', limit: 50 };

function unwrapEncryptedResult(response) {
  const envelope = response?.result;
  if (envelope?.status === 'error') {
    throw new Error(envelope?.error?.message || 'Encrypted payments request failed');
  }
  return envelope?.payload ?? [];
}

export function resetPaymentHistory() {
  paymentHistoryState.records = [];
  paymentHistoryState.loading = false;
  paymentHistoryState.error = null;
  paymentHistoryState.loadedWorker = '';
}

async function ensureEncryptedPaymentHistoryRequests() {
  let info = currentSystemInfo();
  if (!info) {
    info = await loadSystemInfo();
  }
  if (!encryptedRequestsAvailable(info)) {
    throw new Error('ContextVM requests are not available. Ensure Bahia discovery advertises standard relay URLs and a Bahia service pubkey before loading payment history.');
  }
  if (authState.status === 'unknown' || authState.status === 'checking') {
    await initializeAuth();
  }
  return info;
}

export async function requestPaymentHistoryRecords({ worker, limit = 50 } = {}) {
  const workerPubkey = String(worker || '').trim();
  if (!workerPubkey) {
    return [];
  }

  await ensureEncryptedPaymentHistoryRequests();

  const response = await requestEncryptedResult({
    operation: 'payments.history',
    payload: { worker: workerPubkey, limit: Number(limit) || 50 },
    tags: [['domain', 'payments']]
  });
  const records = unwrapEncryptedResult(response);
  return Array.isArray(records) ? records : [];
}

export function unsubscribeFromPaymentHistoryUpdates() {
  paymentSubscriptionGeneration += 1;
  paymentSubscription?.();
  paymentSubscription = null;
}

export async function refreshPaymentHistory(query = subscribedPaymentQuery) {
  const worker = String(query?.worker || '').trim();
  const limit = Number(query?.limit) || 50;
  subscribedPaymentQuery = { worker, limit };
  return loadPaymentHistory(subscribedPaymentQuery);
}

export async function subscribeToPaymentHistoryUpdates(query = {}) {
  subscribedPaymentQuery = {
    worker: String(query.worker || subscribedPaymentQuery.worker || '').trim(),
    limit: Number(query.limit || subscribedPaymentQuery.limit) || 50
  };
  if (paymentSubscription) {
    const ownedSubscription = paymentSubscription;
    return () => {
      if (paymentSubscription === ownedSubscription) unsubscribeFromPaymentHistoryUpdates();
    };
  }

  const generation = ++paymentSubscriptionGeneration;
  const info = await ensureEncryptedPaymentHistoryRequests();
  const unsubscribe = await subscribeToDomainRefresh({
    domain: 'payments',
    servicePubkey: servicePubkeyFromSystemInfo(info),
    refresh: () => refreshPaymentHistory(),
    onError: (error) => {
      paymentHistoryState.error = error?.message || 'Payment history live updates failed';
    }
  });

  if (generation !== paymentSubscriptionGeneration) {
    unsubscribe();
    return () => {};
  }
  paymentSubscription = unsubscribe;
  return () => {
    if (paymentSubscription === unsubscribe) unsubscribeFromPaymentHistoryUpdates();
  };
}

export function resetPaymentHistoryStore() {
  unsubscribeFromPaymentHistoryUpdates();
  resetPaymentHistory();
}

export async function loadPaymentHistory({ worker, limit = 50 } = {}) {
  const workerPubkey = String(worker || '').trim();
  subscribedPaymentQuery = { worker: workerPubkey, limit: Number(limit) || 50 };
  if (!workerPubkey) {
    resetPaymentHistory();
    return [];
  }

  paymentHistoryState.loading = true;
  paymentHistoryState.error = null;

  try {
    paymentHistoryState.records = await requestPaymentHistoryRecords({ worker: workerPubkey, limit });
    paymentHistoryState.loadedWorker = workerPubkey;
    return paymentHistoryState.records;
  } catch (error) {
    paymentHistoryState.records = [];
    paymentHistoryState.loadedWorker = '';
    paymentHistoryState.error = error?.message || 'Failed to load payment history';
    throw error;
  } finally {
    paymentHistoryState.loading = false;
  }
}
