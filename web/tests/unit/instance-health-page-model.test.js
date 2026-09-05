import { describe, expect, it } from 'vitest';
import {
  buildInstanceHealthSummary,
  formatBytes,
  managedInstanceKey,
  memoryPercent,
  statusClass
} from '../../src/routes/instance-health/page-model.js';

describe('instance health page model', () => {
  it('builds operational, attention, recovery, and maintenance totals', () => {
    expect(buildInstanceHealthSummary([
      { status: 'healthy' },
      { status: 'degraded' },
      { status: 'oom_killed', last_recovery_attempt: {} },
      { status: 'manual_override', maintenance_override: {} }
    ])).toEqual({ total: 4, operational: 1, attention: 2, recovery: 1, recentRecovery: 1, maintenance: 1 });
  });

  it('builds stable keys and formats memory', () => {
    const row = { service_id: 'svc', environment_id: 'env', deployment_unit_id: 'unit', runtime_target_name: 'edge', memory_current_bytes: 512, memory_limit_bytes: 1024 };
    expect(managedInstanceKey(row)).toBe('svc:env:unit:edge');
    expect(memoryPercent(row)).toBe(50);
    expect(formatBytes(1024 * 1024)).toBe('1.0 MiB');
  });

  it('maps recovery and attention statuses to visible severity classes', () => {
    expect(statusClass('unhealthy')).toBe('critical');
    expect(statusClass('unknown')).toBe('warning');
    expect(statusClass('running')).toBe('healthy');
  });
});
