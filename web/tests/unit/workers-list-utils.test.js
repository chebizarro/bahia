import { describe, it, expect, vi } from 'vitest';
import {
  inferWorkerActivityBucket,
  inferWorkerStatus,
  getCapabilityOptions,
  filterWorkers,
  summarizeWorkerActivity,
  workerActivityLabel,
  workerActivitySummary,
  workerPriceLabel,
  workerLastAdvertisementLabel
} from '../../src/routes/workers/list-utils.js';

describe('workers list utils', () => {
  it('prefers explicit worker liveness status', () => {
    expect(inferWorkerStatus({ status: 'online', last_advertisement_at: '2020-01-01T00:00:00Z' })).toBe('online');
    expect(inferWorkerStatus({ status: 'stale', last_advertisement_at: new Date().toISOString() })).toBe('stale');
    expect(inferWorkerStatus({ status: 'offline', last_advertisement_at: new Date().toISOString() })).toBe('offline');
  });

  it('falls back to last advertisement recency when status is missing', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-05-02T12:00:00Z'));

    expect(inferWorkerStatus({ last_advertisement_at: '2026-05-02T11:58:00Z' })).toBe('online');
    expect(inferWorkerStatus({ last_advertisement_at: '2026-05-02T11:52:00Z' })).toBe('stale');
    expect(inferWorkerStatus({ last_advertisement_at: '2026-05-02T11:20:00Z' })).toBe('offline');

    vi.useRealTimers();
  });

  it('groups workers into live, recent, and catalog activity buckets', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-05-02T12:00:00Z'));

    expect(inferWorkerActivityBucket({ last_advertisement_at: '2026-05-02T11:58:00Z' })).toBe('live');
    expect(inferWorkerActivityBucket({ last_advertisement_at: '2026-05-02T03:00:00Z' })).toBe('recent');
    expect(inferWorkerActivityBucket({ last_advertisement_at: '2026-04-30T11:59:59Z' })).toBe('catalog');
    expect(summarizeWorkerActivity([
      { last_advertisement_at: '2026-05-02T11:58:00Z' },
      { last_advertisement_at: '2026-05-02T03:00:00Z' },
      { last_advertisement_at: '2026-04-30T11:59:59Z' }
    ])).toEqual({ live: 1, recent: 2, catalog: 3 });

    vi.useRealTimers();
  });

  it('builds stable sorted capability options from current worker fields', () => {
    expect(getCapabilityOptions([
      { software: [{ name: 'docker' }], ml_capabilities: { tasks: ['chat_completions'], runtimes: ['vllm'] } },
      { software: [{ name: 'kubernetes' }], ml_capabilities: { artifact_formats: ['safetensors'] } },
      { software: [] }
    ])).toEqual(['chat_completions', 'docker', 'kubernetes', 'safetensors', 'vllm']);
  });

  it('filters by selected capability and capability search', () => {
    const workers = [
      { pubkey: 'a', software: [{ name: 'docker' }], ml_capabilities: { accelerators: ['gpu'] } },
      { pubkey: 'b', software: [{ name: 'kubernetes' }] },
      { pubkey: 'c', software: [{ name: 'docker' }] }
    ];

    expect(filterWorkers(workers, 'docker', '')).toHaveLength(2);
    expect(filterWorkers(workers, '', 'kube').map((w) => w.pubkey)).toEqual(['b']);
    expect(filterWorkers(workers, 'docker', 'gpu').map((w) => w.pubkey)).toEqual(['a']);
  });

  it('summarizes active Loom jobs and live worker outcomes by pubkey', () => {
    const summary = workerActivitySummary([
      { job_id: 'job-1', worker_pubkey: 'worker-a', status: 'running', cmd: 'build' },
      { job_id: 'job-2', worker_pubkey: 'worker-a', status: 'completed', cmd: 'test' },
      { job_id: 'job-3', worker_pubkey: 'worker-b', status: 'queued', cmd: 'deploy' }
    ], [
      { id: 'op-1', domain: 'worker', worker_pubkey: 'worker-a', status: 'completed', operation: 'worker/cordon' },
      { id: 'op-2', domain: 'worker', worker_pubkey: 'worker-b', status: 'failed' }
    ], 'worker-a');

    expect(summary.jobs).toHaveLength(2);
    expect(summary.activeJobCount).toBe(1);
    expect(summary.latestOperation?.id).toBe('op-1');
    expect(workerActivityLabel(summary)).toBe('1 active · build');
  });

  it('falls back to worker 6997/7997 activity when no job is active', () => {
    const summary = workerActivitySummary([], [
      { domain: 'worker', entity_refs: { worker_pubkey: 'worker-a' }, status: 'failed', operation: 'worker/drain' }
    ], 'worker-a');

    expect(workerActivityLabel(summary)).toBe('failed · worker/drain');
  });

  it('formats pricing and advertisement fields from the worker domain model', () => {
    expect(workerPriceLabel({ pricing: [{ price_per_second: 2, unit: 'sat' }] })).toBe('2 sat/sec');
    expect(workerPriceLabel({ pricing: [] })).toBe('-');
    expect(workerLastAdvertisementLabel({ last_advertisement_at: '2026-05-02T11:58:00Z' })).toBe('2026-05-02 11:58:00');
  });
});
