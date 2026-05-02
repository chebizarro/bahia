import { describe, expect, it } from 'vitest';
import {
  formatDashboardSats,
  isRecentSpendPayment,
  normalizePaymentHistory,
  summarizeRecentSpend
} from '../../src/routes/dashboard-cost-summary.js';

describe('dashboard cost summary utilities', () => {
  it('normalizes payment history response shapes', () => {
    const payments = [{ id: 'p-1' }];

    expect(normalizePaymentHistory(payments)).toBe(payments);
    expect(normalizePaymentHistory({ payments })).toBe(payments);
    expect(normalizePaymentHistory({ records: payments })).toBe(payments);
    expect(normalizePaymentHistory({ data: payments })).toBe(payments);
    expect(normalizePaymentHistory(null)).toEqual([]);
  });

  it('identifies outgoing sent or redeemed payments as recent spend', () => {
    expect(isRecentSpendPayment({ direction: 'payment', status: 'sent' })).toBe(true);
    expect(isRecentSpendPayment({ direction: 'payment', status: 'redeemed' })).toBe(true);
    expect(isRecentSpendPayment({ direction: 'change', status: 'redeemed' })).toBe(false);
    expect(isRecentSpendPayment({ direction: 'payment', status: 'failed' })).toBe(false);
  });

  it('summarizes spend, dedupes records, and ignores change or failed payments', () => {
    const summary = summarizeRecentSpend([
      { id: 'p-1', amount_sats: 1200, direction: 'payment', status: 'sent', created_at: '2026-05-02T10:00:00Z' },
      { id: 'p-1', amount_sats: 1200, direction: 'payment', status: 'sent', created_at: '2026-05-02T10:00:00Z' },
      { id: 'p-2', amount_sats: '800', direction: 'payment', status: 'redeemed', created_at: '2026-05-02T11:00:00Z' },
      { id: 'p-3', amount_sats: 50, direction: 'change', status: 'redeemed', created_at: '2026-05-02T12:00:00Z' },
      { id: 'p-4', amount_sats: 500, direction: 'payment', status: 'failed', created_at: '2026-05-02T13:00:00Z' },
      { id: 'p-5', amount_sats: 'not-a-number', direction: 'payment', status: 'sent', created_at: '2026-05-02T14:00:00Z' }
    ], { workerCount: 2 });

    expect(summary).toEqual({
      totalSats: 2000,
      paymentCount: 2,
      workerCount: 2,
      latestPaymentAt: '2026-05-02T11:00:00Z'
    });
  });

  it('formats dashboard sats for display', () => {
    expect(formatDashboardSats(2000)).toBe('2,000 sats');
    expect(formatDashboardSats('0')).toBe('0 sats');
    expect(formatDashboardSats(Number.NaN)).toBe('-');
  });
});
