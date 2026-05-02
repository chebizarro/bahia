import { describe, expect, it } from 'vitest';
import {
  buildWorkerCostEstimates,
  formatDurationSecs,
  formatPricePerSecond,
  formatSats,
  isValidEstimatedDurationSecs,
  normalizeEstimatedDurationSecs,
  summarizeDeploymentCostEstimates,
  workerDisplayName
} from '../../src/routes/services/deploy-cost-estimate.js';

const workers = [
  {
    pubkey: 'worker-expensive',
    name: 'Expensive Worker',
    pricing: [{ mint_url: 'https://mint.example.com', price_per_second: 8, unit: 'sat' }]
  },
  {
    pubkey: 'worker-cheap',
    name: 'Cheap Worker',
    min_duration_secs: 120,
    pricing: [{ mint_url: 'https://mint.example.com', price_per_second: 4, unit: 'sat' }]
  },
  {
    pubkey: 'worker-unpriced',
    name: 'Unpriced Worker',
    pricing: []
  }
];

describe('deployment cost estimate helpers', () => {
  it('validates positive whole-second durations for UI previews', () => {
    expect(isValidEstimatedDurationSecs('')).toBe(false);
    expect(isValidEstimatedDurationSecs(0)).toBe(false);
    expect(isValidEstimatedDurationSecs('90.4')).toBe(false);
    expect(isValidEstimatedDurationSecs('300')).toBe(true);
  });

  it('normalizes invalid durations to the default estimate duration', () => {
    expect(normalizeEstimatedDurationSecs('')).toBe(300);
    expect(normalizeEstimatedDurationSecs(-10)).toBe(300);
    expect(normalizeEstimatedDurationSecs('90.4')).toBe(90);
  });

  it('builds sorted estimates from workers with advertised sat pricing', () => {
    const estimates = buildWorkerCostEstimates(workers, 300);

    expect(estimates).toHaveLength(2);
    expect(estimates[0]).toMatchObject({
      worker_pubkey: 'worker-cheap',
      worker_name: 'Cheap Worker',
      price_per_second: 4,
      estimated_secs: 300,
      billable_secs: 300,
      estimated_cost_sats: 1200
    });
    expect(estimates[1].estimated_cost_sats).toBe(2400);
  });

  it('honors worker minimum billable duration', () => {
    const [estimate] = buildWorkerCostEstimates([workers[1]], 60);

    expect(estimate.estimated_secs).toBe(60);
    expect(estimate.billable_secs).toBe(120);
    expect(estimate.estimated_cost_sats).toBe(480);
  });

  it('ignores non-sat pricing tiers instead of labelling them as sats', () => {
    const estimates = buildWorkerCostEstimates([
      { pubkey: 'worker-credits', pricing: [{ price_per_second: 1, unit: 'credit' }] },
      { pubkey: 'worker-sats', pricing: [{ price_per_second: 2, unit: 'sats' }] }
    ], 100);

    expect(estimates).toHaveLength(1);
    expect(estimates[0]).toMatchObject({ worker_pubkey: 'worker-sats', unit: 'sat', estimated_cost_sats: 200 });
  });

  it('summarizes available worker estimate range', () => {
    const summary = summarizeDeploymentCostEstimates(workers, 300);

    expect(summary.available_workers).toBe(2);
    expect(summary.min_cost_sats).toBe(1200);
    expect(summary.max_cost_sats).toBe(2400);
    expect(summary.average_cost_sats).toBe(1800);
    expect(summary.cheapest.worker_pubkey).toBe('worker-cheap');
  });

  it('formats estimate display values', () => {
    expect(formatSats(1200)).toBe('1,200 sat');
    expect(formatDurationSecs(3660)).toBe('1h 1m');
    expect(formatPricePerSecond(4, 'sat')).toBe('4 sat/sec');
    expect(workerDisplayName({ pubkey: 'a'.repeat(64) })).toBe(`${'a'.repeat(12)}…${'a'.repeat(6)}`);
  });
});
