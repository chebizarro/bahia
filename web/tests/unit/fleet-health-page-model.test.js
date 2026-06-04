import { describe, expect, it, vi } from 'vitest';
import {
  activeCleanupByWorker,
  buildFleetHealthSummary,
  buildFleetWeatherNodes,
  cleanupExecutionsForWorker,
  dominantPressureSignal,
  fleetHealthNavBadge,
  sortCleanupExecutions
} from '../../src/routes/fleet-health/page-model.js';

describe('fleet health page model', () => {
  it('summarizes fleet pressure, telemetry, cleanup lifecycle, and nav badges', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-05-24T12:00:00Z'));

    const workers = [
      {
        pubkey: 'blocked-worker',
        name: 'Blocked worker',
        status: 'online',
        pressure: { capacity_class: 'blocked', overall_level: 'critical', recommended_action: 'operator_intervention', disk_pressure: 'critical' },
        telemetry: { disk_used_pct: 96 }
      },
      {
        pubkey: 'cleanup-worker',
        name: 'Cleanup worker',
        status: 'online',
        pressure: { capacity_class: 'cleanup_only', overall_level: 'warning', recommended_action: 'cleanup_recommended' },
        telemetry: { ram_used_gb: 120, ram_total_gb: 128 }
      },
      {
        pubkey: 'clear-worker',
        name: 'Clear worker',
        status: 'online',
        pressure: { capacity_class: 'open', overall_level: 'nominal', recommended_action: 'none' },
        telemetry: { disk_used_pct: 30 }
      },
      { pubkey: 'missing-telemetry', name: 'Missing telemetry', status: 'online' }
    ];
    const cleanupExecutions = [
      { id: 'cleanup-1', worker_pubkey: 'cleanup-worker', status: 'running', started_at: '2026-05-24T11:50:00Z' },
      { id: 'cleanup-2', worker_pubkey: 'blocked-worker', status: 'failed', started_at: '2026-05-24T11:00:00Z', updated_at: '2026-05-24T11:10:00Z' },
      { id: 'cleanup-3', worker_pubkey: 'clear-worker', status: 'completed', started_at: '2026-05-24T10:00:00Z', completed_at: '2026-05-24T10:05:00Z' }
    ];

    expect(buildFleetHealthSummary(workers, cleanupExecutions)).toMatchObject({
      total: 4,
      capacity: { open: 2, reduced: 0, cleanup_only: 1, blocked: 1 },
      pressure: { nominal: 1, warning: 1, critical: 1, unknown: 1 },
      recommended: { none: 2, cleanup_recommended: 1, operator_intervention: 1 },
      telemetry: { present: 3, missing: 1 },
      cleanup: { active: 1, completed: 1, failed: 1, total: 3 }
    });
    expect(fleetHealthNavBadge(workers)).toBe('Blocked 1');

    vi.useRealTimers();
  });

  it('orders weather nodes by pressure severity and attaches active cleanup state', () => {
    const workers = [
      { pubkey: 'open', name: 'Open', pressure: { capacity_class: 'open', overall_level: 'nominal', recommended_action: 'none' }, telemetry: { disk_used_pct: 10 } },
      { pubkey: 'cleanup', name: 'Cleanup', pressure: { capacity_class: 'cleanup_only', overall_level: 'warning', recommended_action: 'cleanup_recommended' }, telemetry: { disk_used_pct: 88 } },
      { pubkey: 'blocked', name: 'Blocked', pressure: { capacity_class: 'blocked', overall_level: 'critical', recommended_action: 'operator_intervention' }, telemetry: { disk_used_pct: 94 } }
    ];

    const nodes = buildFleetWeatherNodes(workers, [
      { id: 'cleanup-active', worker_pubkey: 'cleanup', status: 'dispatched', started_at: '2026-05-24T11:00:00Z' }
    ], [{ worker_pubkey: 'blocked', active_assignments: [{ id: 'a' }, { id: 'b' }] }]);

    expect(nodes.map((node) => node.id)).toEqual(['blocked', 'cleanup', 'open']);
    expect(nodes[0].assignmentCount).toBe(2);
    expect(nodes[1].cleanup.status).toBe('dispatched');
    expect(dominantPressureSignal(workers[1])).toMatchObject({ key: 'storage', label: 'Storage', level: 'warning' });
  });

  it('sorts and filters cleanup executions by worker', () => {
    const executions = [
      { id: 'old', worker_pubkey: 'w1', status: 'completed', updated_at: '2026-05-24T10:00:00Z' },
      { id: 'new', worker_pubkey: 'w1', status: 'failed', updated_at: '2026-05-24T12:00:00Z' },
      { id: 'other', worker_pubkey: 'w2', status: 'running', updated_at: '2026-05-24T11:00:00Z' }
    ];

    expect(sortCleanupExecutions(executions).map((execution) => execution.id)).toEqual(['new', 'other', 'old']);
    expect(cleanupExecutionsForWorker(executions, 'w1').map((execution) => execution.id)).toEqual(['new', 'old']);
    expect(activeCleanupByWorker(executions).get('w2').id).toBe('other');
  });
});
