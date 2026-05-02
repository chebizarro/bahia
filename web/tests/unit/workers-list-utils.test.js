import { describe, it, expect, vi } from 'vitest';
import { inferWorkerStatus, getCapabilityOptions, filterWorkers } from '../../src/routes/workers/list-utils.js';

describe('workers list utils', () => {
  it('prefers explicit status for online/offline', () => {
    expect(inferWorkerStatus({ status: 'online', last_seen: '2020-01-01T00:00:00Z' })).toBe('online');
    expect(inferWorkerStatus({ status: 'offline', last_seen: new Date().toISOString() })).toBe('offline');
  });

  it('falls back to last_seen recency when status is missing', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-05-02T12:00:00Z'));

    expect(inferWorkerStatus({ last_seen: '2026-05-02T11:58:00Z' })).toBe('online');
    expect(inferWorkerStatus({ last_seen: '2026-05-02T11:40:00Z' })).toBe('offline');

    vi.useRealTimers();
  });

  it('builds stable sorted capability options', () => {
    expect(getCapabilityOptions([
      { capabilities: ['docker', 'kubernetes'] },
      { capabilities: ['docker', 'wasm'] },
      { capabilities: [] }
    ])).toEqual(['docker', 'kubernetes', 'wasm']);
  });

  it('filters by selected capability and capability search', () => {
    const workers = [
      { pubkey: 'a', capabilities: ['docker', 'gpu'] },
      { pubkey: 'b', capabilities: ['kubernetes'] },
      { pubkey: 'c', capabilities: ['docker'] }
    ];

    expect(filterWorkers(workers, 'docker', '')).toHaveLength(2);
    expect(filterWorkers(workers, '', 'kube').map((w) => w.pubkey)).toEqual(['b']);
    expect(filterWorkers(workers, 'docker', 'gpu').map((w) => w.pubkey)).toEqual(['a']);
  });
});
