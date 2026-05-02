const SPEND_STATUSES = new Set(['sent', 'redeemed']);

export function normalizePaymentHistory(value) {
  if (Array.isArray(value)) return value;
  if (Array.isArray(value?.payments)) return value.payments;
  if (Array.isArray(value?.records)) return value.records;
  if (Array.isArray(value?.data)) return value.data;
  return [];
}

function paymentKey(payment) {
  if (payment?.id) return `id:${payment.id}`;
  return [
    payment?.worker_pubkey,
    payment?.deployment_run_id,
    payment?.created_at,
    payment?.amount_sats,
    payment?.direction,
    payment?.status
  ].map((value) => String(value ?? '')).join('|');
}

export function isRecentSpendPayment(payment) {
  const direction = String(payment?.direction || '').toLowerCase();
  const status = String(payment?.status || '').toLowerCase();
  return direction === 'payment' && SPEND_STATUSES.has(status);
}

export function summarizeRecentSpend(payments, { workerCount = 0 } = {}) {
  const seen = new Set();
  let totalSats = 0;
  let paymentCount = 0;
  let latestPaymentAt = '';

  for (const payment of normalizePaymentHistory(payments)) {
    const key = paymentKey(payment);
    if (seen.has(key)) continue;
    seen.add(key);

    if (!isRecentSpendPayment(payment)) continue;

    const amount = Number(payment?.amount_sats);
    if (!Number.isFinite(amount)) continue;

    totalSats += amount;
    paymentCount += 1;

    const timestamp = String(payment?.created_at || payment?.updated_at || '');
    if (timestamp && (!latestPaymentAt || timestamp > latestPaymentAt)) {
      latestPaymentAt = timestamp;
    }
  }

  return { totalSats, paymentCount, workerCount, latestPaymentAt };
}

export function formatDashboardSats(value) {
  const amount = Number(value);
  if (!Number.isFinite(amount)) return '-';
  return `${amount.toLocaleString()} sats`;
}
