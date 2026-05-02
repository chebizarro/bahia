const CSV_COLUMNS = [
  ['id', 'Payment ID'],
  ['created_at', 'Created At'],
  ['updated_at', 'Updated At'],
  ['deployment_run_id', 'Run ID'],
  ['worker_pubkey', 'Worker Pubkey'],
  ['amount_sats', 'Amount (sats)'],
  ['direction', 'Direction'],
  ['status', 'Status'],
  ['mint_url', 'Mint URL'],
  ['token_hash', 'Token Hash'],
  ['error_message', 'Error Message']
];

export function normalizePayments(value) {
  if (Array.isArray(value)) return value;
  if (Array.isArray(value?.payments)) return value.payments;
  if (Array.isArray(value?.records)) return value.records;
  return [];
}

export function filterPayments(payments, { status = 'all', direction = 'all', search = '' } = {}) {
  const query = search.trim().toLowerCase();
  return normalizePayments(payments).filter((payment) => {
    const matchesStatus = status === 'all' || payment.status === status;
    const matchesDirection = direction === 'all' || payment.direction === direction;
    const matchesSearch = !query || [
      payment.id,
      payment.deployment_run_id,
      payment.worker_pubkey,
      payment.mint_url,
      payment.token_hash,
      payment.error_message
    ].some((value) => String(value || '').toLowerCase().includes(query));

    return matchesStatus && matchesDirection && matchesSearch;
  });
}

export function getUniqueValues(payments, key) {
  return Array.from(new Set(normalizePayments(payments).map((payment) => payment?.[key]).filter(Boolean))).sort();
}

export function formatDateTime(value) {
  if (!value) return '-';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value).slice(0, 19).replace('T', ' ');
  return date.toLocaleString();
}

export function formatSats(value) {
  const amount = Number(value);
  if (!Number.isFinite(amount)) return '-';
  return `${amount.toLocaleString()} sats`;
}

export function truncateMiddle(value, start = 12, end = 6) {
  if (!value) return '-';
  const text = String(value);
  if (text.length <= start + end + 3) return text;
  return `${text.slice(0, start)}...${text.slice(-end)}`;
}

export function escapeHtml(value) {
  return String(value ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

export function csvEscape(value) {
  if (value === null || value === undefined) return '';
  let text = String(value);

  // Prevent spreadsheet formula injection when CSVs are opened in Excel/Sheets.
  if (/^\s*[=+\-@]/.test(text) || /^[\t\r]/.test(text)) {
    text = `'${text}`;
  }

  if (/[",\n\r]/.test(text)) {
    return `"${text.replace(/"/g, '""')}"`;
  }
  return text;
}

export function paymentsToCsv(payments) {
  const rows = [
    CSV_COLUMNS.map(([, label]) => csvEscape(label)).join(','),
    ...normalizePayments(payments).map((payment) => CSV_COLUMNS
      .map(([key]) => csvEscape(payment?.[key] ?? ''))
      .join(','))
  ];

  return rows.join('\n');
}

export function buildPaymentsCsvFilename({ worker = '', date = new Date() } = {}) {
  const workerPart = worker ? `-${String(worker).slice(0, 12)}` : '';
  return `bahia-payments${workerPart}-${date.toISOString().slice(0, 10)}.csv`;
}
