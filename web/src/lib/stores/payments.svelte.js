import { requestPrivateResult } from '$lib/nostr/private-controlplane.js';

export const paymentHistoryState = $state({
  records: [],
  loading: false,
  error: null,
  loadedWorker: ''
});

function unwrapPrivateResult(response) {
  const envelope = response?.result;
  if (envelope?.status === 'error') {
    throw new Error(envelope?.error?.message || 'Private payments request failed');
  }
  return envelope?.payload ?? [];
}

export function resetPaymentHistory() {
  paymentHistoryState.records = [];
  paymentHistoryState.loading = false;
  paymentHistoryState.error = null;
  paymentHistoryState.loadedWorker = '';
}

export async function loadPaymentHistory({ worker, limit = 50 } = {}) {
  const workerPubkey = String(worker || '').trim();
  if (!workerPubkey) {
    resetPaymentHistory();
    return [];
  }

  paymentHistoryState.loading = true;
  paymentHistoryState.error = null;

  try {
    const response = await requestPrivateResult({
      operation: 'payments.history',
      payload: { worker: workerPubkey, limit: Number(limit) || 50 },
      tags: [['domain', 'payments']]
    });
    const records = unwrapPrivateResult(response);
    paymentHistoryState.records = Array.isArray(records) ? records : [];
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
