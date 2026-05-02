import { describe, expect, it } from 'vitest';
import {
  buildPaymentsCsvFilename,
  csvEscape,
  filterPayments,
  normalizePayments,
  paymentsToCsv,
  truncateMiddle
} from '../../src/routes/payments/list-utils.js';

describe('payments list utils', () => {
  const payments = [
    {
      id: 'pay-1',
      deployment_run_id: 'run-1',
      worker_pubkey: 'worker-alpha',
      amount_sats: 100,
      direction: 'payment',
      status: 'sent',
      mint_url: 'https://mint.example',
      token_hash: 'hash-1',
      error_message: '',
      created_at: '2026-05-02T10:00:00Z',
      updated_at: '2026-05-02T10:00:01Z'
    },
    {
      id: 'pay-2',
      deployment_run_id: 'run-2',
      worker_pubkey: 'worker-beta',
      amount_sats: 50,
      direction: 'change',
      status: 'failed',
      mint_url: 'https://mint.example',
      token_hash: 'hash-2',
      error_message: 'bad quote',
      created_at: '2026-05-02T11:00:00Z',
      updated_at: '2026-05-02T11:00:01Z'
    }
  ];

  it('normalizes common payment history response shapes', () => {
    expect(normalizePayments(payments)).toEqual(payments);
    expect(normalizePayments({ payments })).toEqual(payments);
    expect(normalizePayments({ records: payments })).toEqual(payments);
    expect(normalizePayments(null)).toEqual([]);
  });

  it('filters payments by status, direction, and search text', () => {
    expect(filterPayments(payments, { status: 'sent' })).toHaveLength(1);
    expect(filterPayments(payments, { direction: 'change' })).toHaveLength(1);
    expect(filterPayments(payments, { search: 'run-2' }).map((p) => p.id)).toEqual(['pay-2']);
    expect(filterPayments(payments, { status: 'sent', search: 'beta' })).toEqual([]);
  });

  it('escapes and exports CSV rows', () => {
    expect(csvEscape('hello,"world"')).toBe('"hello,""world"""');
    expect(csvEscape('=IMPORTXML("https://example.test")')).toBe(`"'=IMPORTXML(""https://example.test"")"`);
    const csv = paymentsToCsv([{ ...payments[0], error_message: 'line\nbreak' }]);
    expect(csv.split('\n')[0]).toContain('Payment ID');
    expect(csv).toContain('"line\nbreak"');
  });

  it('builds stable UI strings', () => {
    expect(truncateMiddle('1234567890abcdef', 4, 4)).toBe('1234...cdef');
    expect(buildPaymentsCsvFilename({ worker: 'worker-alpha-long', date: new Date('2026-05-02T12:00:00Z') })).toBe('bahia-payments-worker-alpha-2026-05-02.csv');
  });
});
