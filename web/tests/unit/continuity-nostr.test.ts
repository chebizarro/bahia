import { describe, expect, it } from 'vitest';
import {
  continuityNostrFilters,
  continuityStatusesFromEvents,
  deriveContinuityAssessments,
  parseContinuityStatusEvent,
  simulateWorkerFailureFromEvents
} from '../../src/lib/nostr/continuity';

const SERVICE = 'svc-api';
const SERVICE_AUTHOR = 'a'.repeat(64);
const WORKER_AUTHOR = 'b'.repeat(64);

function event(overrides: Record<string, any>) {
  return {
    id: overrides.id || `${overrides.kind}-${Math.random()}`,
    kind: overrides.kind,
    pubkey: overrides.pubkey || SERVICE_AUTHOR,
    created_at: overrides.created_at || 1_779_989_600,
    tags: overrides.tags || [],
    content: overrides.content === undefined ? '{}' : typeof overrides.content === 'string' ? overrides.content : JSON.stringify(overrides.content)
  };
}

describe('continuity Nostr read models', () => {
  it('uses scoped filters for continuity status, definitions, heartbeat, and worker state events', () => {
    expect(continuityNostrFilters()).toEqual(expect.arrayContaining([
      expect.objectContaining({ kinds: [30351], '#t': ['continuity', 'continuity-status'] }),
      expect.objectContaining({ kinds: [30353], '#t': ['continuity', 'recovery-progress'] }),
      expect.objectContaining({ kinds: [31400, 31401, 31402, 31403, 31404] }),
      expect.objectContaining({ kinds: [30315], '#domain': ['continuity'] }),
      expect.objectContaining({ kinds: [30900], '#domain': ['worker'], '#schema': ['bahia.state.worker.v1'] })
    ]));
    expect(continuityNostrFilters()).not.toEqual(expect.arrayContaining([
      expect.objectContaining({ kinds: [30350] })
    ]));
  });

  it('decodes kind 30351 continuity status read models from content and tags', () => {
    const status = parseContinuityStatusEvent(event({
      id: 'status-1',
      kind: 30351,
      tags: [
        ['d', 'continuity-status:svc-api'],
        ['service', SERVICE],
        ['profile', 'degraded'],
        ['operation_state', 'failover_in_progress'],
        ['primary_worker', 'primary-a'],
        ['active_worker', 'standby-a'],
        ['standby_worker', 'standby-b'],
        ['run', 'run-1'],
        ['step', '2'],
        ['step_count', '4'],
        ['action', 'restore_backup'],
        ['t', 'continuity'],
        ['t', 'continuity-status']
      ],
      content: { reason: 'heartbeat expired', changed_at: '2026-06-03T12:00:00Z' }
    }));

    expect(status).toEqual({
      service_key: SERVICE,
      active_profile: 'degraded',
      operation_state: 'failover_in_progress',
      primary_worker_pubkey: 'primary-a',
      active_worker_pubkey: 'standby-a',
      standby_worker_pubkey: 'standby-b',
      reason: 'heartbeat expired',
      changed_at: '2026-06-03T12:00:00Z',
      current_run: { id: 'run-1', step_index: 2, step_count: 4, step_action: 'restore_backup' }
    });
  });

  it('ignores shared kind 30315 statuses outside the continuity heartbeat domain', () => {
    const events = [
      event({ id: 'status', kind: 30351, tags: [['d', `continuity-status:${SERVICE}`], ['service', SERVICE], ['t', 'continuity'], ['t', 'continuity-status']], content: { service_key: SERVICE, active_profile: 'full', operation_state: 'steady', primary_worker_pubkey: 'primary-a', active_worker_pubkey: 'primary-a' } }),
      event({ id: 'profile', kind: 31400, tags: [['d', `continuity-profile:${SERVICE}`], ['service', SERVICE], ['primary', 'primary-a'], ['profile', 'full']] }),
      event({ id: 'standby-a', kind: 31402, tags: [['d', `standby-node:${SERVICE}:standby-a`], ['service', SERVICE], ['worker', 'standby-a'], ['profile', 'full']] }),
      event({ id: 'security-status', kind: 30315, tags: [['d', 'security:scan:run-1'], ['domain', 'security'], ['worker', 'standby-a'], ['status', 'online']] })
    ];

    expect(deriveContinuityAssessments(events)).toEqual([{
      service_key: SERVICE,
      survivability: 'unsatisfied',
      has_failover_recipe: false,
      has_recovery_recipe: false,
      standby_count: 1,
      replication_configured: false,
      heartbeat_active: false
    }]);
  });

  it('dedupes latest continuity statuses and derives topology from Nostr events', () => {
    const events = [
      event({ id: 'old-status', kind: 30351, created_at: 100, tags: [['d', `continuity-status:${SERVICE}`], ['service', SERVICE], ['t', 'continuity'], ['t', 'continuity-status']], content: { service_key: SERVICE, active_profile: 'full', operation_state: 'steady', primary_worker_pubkey: 'primary-a', active_worker_pubkey: 'primary-a', changed_at: '2026-06-03T11:00:00Z' } }),
      event({ id: 'new-status', kind: 30351, created_at: 200, tags: [['d', `continuity-status:${SERVICE}`], ['service', SERVICE], ['t', 'continuity'], ['t', 'continuity-status']], content: { service_key: SERVICE, active_profile: 'full', operation_state: 'steady', primary_worker_pubkey: 'primary-a', active_worker_pubkey: 'primary-a', changed_at: '2026-06-03T12:00:00Z' } }),
      event({ id: 'profile', kind: 31400, tags: [['d', `continuity-profile:${SERVICE}`], ['service', SERVICE], ['primary', 'primary-a'], ['profile', 'full'], ['profile', 'degraded']] }),
      event({ id: 'failover', kind: 31401, tags: [['d', `failover-policy:${SERVICE}:primary`], ['service', SERVICE], ['recipe-kind', 'failover']], content: { service_key: SERVICE, kind: 'failover', name: 'primary' } }),
      event({ id: 'recovery', kind: 31404, tags: [['d', `recovery-workflow:${SERVICE}:primary`], ['service', SERVICE], ['recipe-kind', 'recovery']], content: { service_key: SERVICE, kind: 'recovery', name: 'primary' } }),
      event({ id: 'standby-a', kind: 31402, tags: [['d', `standby-node:${SERVICE}:standby-a`], ['service', SERVICE], ['worker', 'standby-a'], ['profile', 'full'], ['profile', 'degraded']] }),
      event({ id: 'standby-b', kind: 31402, tags: [['d', `standby-node:${SERVICE}:standby-b`], ['service', SERVICE], ['worker', 'standby-b'], ['profile', 'emergency']] }),
      event({ id: 'replication', kind: 31403, tags: [['d', `replication-policy:${SERVICE}`], ['service', SERVICE]], content: { service_key: SERVICE, targets: [{ worker_pubkey: 'standby-a' }] } }),
      event({ id: 'heartbeat-a', kind: 30315, pubkey: WORKER_AUTHOR, tags: [['d', 'continuity:heartbeat:standby-a'], ['domain', 'continuity'], ['worker', 'standby-a'], ['status', 'online']] })
    ];

    const statuses = continuityStatusesFromEvents(events);
    expect(statuses).toHaveLength(1);
    expect(statuses[0].changed_at).toBe('2026-06-03T12:00:00Z');

    expect(deriveContinuityAssessments(events, statuses)).toEqual([{ 
      service_key: SERVICE,
      survivability: 'survivable',
      has_failover_recipe: true,
      has_recovery_recipe: true,
      standby_count: 2,
      replication_configured: true,
      heartbeat_active: true
    }]);

    expect(simulateWorkerFailureFromEvents('standby-a', events, statuses)).toEqual([{ 
      service_key: SERVICE,
      survivability: 'unsatisfied',
      has_failover_recipe: true,
      has_recovery_recipe: true,
      standby_count: 1,
      replication_configured: true,
      heartbeat_active: false
    }]);
  });
});
