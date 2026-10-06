import { describe, expect, it, vi } from 'vitest';

const bootMock = vi.hoisted(() => ({
  store: null as any, pool: null as any, relays: [] as string[], refresh: null as any,
  auth: { status: 'unauthenticated', pubkey: '' }
}));
vi.mock('$lib/nostr/boot.js', () => ({
  getEventStore: () => bootMock.store,
  getServicePubkeys: () => ['a'.repeat(64)],
  getPool: () => bootMock.pool, getRelayUrls: () => bootMock.relays,
  onStoreRefresh: (cb: any) => { bootMock.refresh = cb; return () => { bootMock.refresh = null; }; }
}));
vi.mock('$lib/stores/auth.js', () => ({ authState: bootMock.auth }));
import {
  continuityNostrFilters,
  continuityEventsFromStore,
  initContinuityStoreBinding,
  teardownContinuityStoreBinding,
  continuityRequestsFromEvents,
  continuityStatusesFromEvents,
  deriveContinuityAssessments,
  parseContinuityStatusEvent,
  simulateWorkerFailureFromEvents,
  subscribeToContinuityDashboard
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
  it('scopes service projections to seed roots and operator-authored families to the authenticated operator', () => {
    const filters = continuityNostrFilters({ serviceAuthors: [SERVICE_AUTHOR], operatorAuthors: [WORKER_AUTHOR] });
    expect(filters).toHaveLength(6);
    expect(filters).toEqual(expect.arrayContaining([
      expect.objectContaining({ kinds: [30351], authors: [SERVICE_AUTHOR] }),
      expect.objectContaining({ kinds: [30353], authors: [SERVICE_AUTHOR] }),
      expect.objectContaining({ kinds: [31400, 31401, 31402, 31403, 31404], authors: [WORKER_AUTHOR] }),
      expect.objectContaining({ kinds: [38430, 38431], authors: [WORKER_AUTHOR] }),
      expect.objectContaining({ kinds: [30315], authors: [WORKER_AUTHOR] }),
      expect.objectContaining({ kinds: [30900], '#t': ['worker-state'], authors: [SERVICE_AUTHOR] })
    ]));
    expect(continuityNostrFilters()).toEqual([]);
  });

  it('renders verified cached service events immediately and ignores an untrusted signer', async () => {
    const status = (id: string, pubkey: string) => event({
      id, kind: 30351, pubkey, created_at: 300,
      tags: [['d', `continuity-status:${SERVICE}`], ['service', SERVICE], ['t', 'continuity']],
      content: { service_key: SERVICE, active_profile: 'degraded', operation_state: 'failover_in_progress' }
    });
    const events = [status('cached', SERVICE_AUTHOR), status('forged', WORKER_AUTHOR)];
    bootMock.store = { query: (filter: any) => events.filter((item) => filter.kinds.includes(item.kind) && filter.authors.includes(item.pubkey)) };
    const updates: any[] = [];
    const unsubscribe = await subscribeToContinuityDashboard({ onUpdate: (snapshot) => updates.push(snapshot) });
    expect(updates.at(-1).events.map((item: any) => item.id)).toEqual(['cached']);
    expect(continuityEventsFromStore().map((item) => item.id)).toEqual(['cached']);
    events.push(status('live', SERVICE_AUTHOR));
    bootMock.refresh();
    expect(updates.at(-1).events.map((item: any) => item.id)).toContain('live');
    unsubscribe();
    expect(bootMock.refresh).toBeNull();
  });

  it('represents failover and recovery request events', () => {
    const requests = continuityRequestsFromEvents([
      event({ id: 'failover-request', kind: 38430, tags: [['service', SERVICE], ['worker', 'standby-a']], content: { reason: 'primary unavailable' } }),
      event({ id: 'recovery-request', kind: 38431, tags: [['service', SERVICE]], content: { worker_pubkey: 'primary-a' } })
    ]);

    expect(requests).toEqual(expect.arrayContaining([
      expect.objectContaining({ id: 'failover-request', request_type: 'failover', service_key: SERVICE, worker_pubkey: 'standby-a' }),
      expect.objectContaining({ id: 'recovery-request', request_type: 'recovery', service_key: SERVICE, worker_pubkey: 'primary-a' })
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

  // Producer shape: internal/controlplane/worker_state_publisher.go emits the
  // full worker (standby_assignments included) as 30900 cp-state with
  // legacy_kind 32000 and t=worker-state; the tombstone keeps d and t.
  function workerState({ id, created_at, deleted = false, standbys = [] as string[] }: { id: string; created_at: number; deleted?: boolean; standbys?: string[] }) {
    return event({
      id,
      kind: 30900,
      created_at,
      tags: [['d', 'worker:state:standby-w'], ['domain', 'worker'], ['schema', 'bahia.cp-state.v1'], ['legacy_kind', '32000'], ['deleted', String(deleted)], ['t', 'worker-state'], ['worker', 'standby-w']],
      content: {
        deleted,
        pubkey: 'standby-w',
        status: 'online',
        standby_assignments: standbys.map((serviceKey) => ({ service_key: serviceKey, tier: 'warm', supported_profiles: ['full'], updated_at: '2026-09-30T00:00:00Z' }))
      }
    });
  }

  it('derives standbys only from each worker\'s newest live cp-state record', () => {
    const status = event({ id: 'status', kind: 30351, tags: [['d', `continuity-status:${SERVICE}`], ['service', SERVICE], ['t', 'continuity'], ['t', 'continuity-status']], content: { service_key: SERVICE, active_profile: 'full', operation_state: 'steady', primary_worker_pubkey: 'primary-a', active_worker_pubkey: 'primary-a' } });
    const standbyCount = (events: any[]) => deriveContinuityAssessments([status, ...events], continuityStatusesFromEvents([status]))[0].standby_count;

    const live = workerState({ id: 'live', created_at: 100, standbys: [SERVICE] });
    expect(standbyCount([live])).toBe(1);

    const stale = workerState({ id: 'stale', created_at: 50, standbys: [SERVICE] });
    const newerWithout = workerState({ id: 'newer', created_at: 200 });
    expect(standbyCount([stale, newerWithout])).toBe(0);

    const tombstone = workerState({ id: 'tombstone', created_at: 300, deleted: true, standbys: [SERVICE] });
    expect(standbyCount([live, tombstone])).toBe(0);
  });
});

describe('continuity heartbeat expiry', () => {
  const standby = (worker: string) => event({ id: `standby-${worker}`, kind: 31402, tags: [['d', `standby-node:${SERVICE}:${worker}`], ['service', SERVICE], ['worker', worker], ['profile', 'full']] });
  const heartbeat = (worker: string, tags: string[][], createdAt = Math.floor(Date.now() / 1000)) =>
    event({ id: `heartbeat-${worker}`, kind: 30315, pubkey: WORKER_AUTHOR, created_at: createdAt, tags: [['d', `continuity:heartbeat:${worker}`], ['domain', 'continuity'], ['worker', worker], ['status', 'online'], ...tags] });
  const active = (events: any[]) => deriveContinuityAssessments(events)[0].heartbeat_active;

  it('honours the NIP-40 expiration tag', () => {
    const now = Math.floor(Date.now() / 1000);
    expect(active([standby('w'), heartbeat('w', [['expiration', String(now + 30)]])])).toBe(true);
    expect(active([standby('w'), heartbeat('w', [['expiration', String(now - 1)]], now - 60)])).toBe(false);
  });

  it('prefers expiration over the legacy expires_after_ms and still reads the legacy tag alone', () => {
    const now = Math.floor(Date.now() / 1000);
    expect(active([standby('w'), heartbeat('w', [['expiration', String(now - 1)], ['expires_after_ms', '600000']], now - 60)])).toBe(false);
    expect(active([standby('w'), heartbeat('w', [['expires_after_ms', '1000']], now - 60)])).toBe(false);
    expect(active([standby('w'), heartbeat('w', [['expires_after_ms', '600000']], now - 60)])).toBe(true);
  });
});

describe('continuity app-lifetime store binding', () => {
  const RELAY = 'wss://relay.test';
  const OPERATOR = 'c'.repeat(64);

  // Answers every paged REQ with an empty page so catch-up completes.
  function relayPool() {
    const requests: any[] = [];
    const pool = { subscribe: vi.fn((options: any) => {
      requests.push(options);
      if (options.filters[0].limit !== undefined) queueMicrotask(() => options.onEose(RELAY));
      return { unsubscribe: vi.fn() };
    }) };
    return { pool, requests };
  }

  function start() {
    const { pool, requests } = relayPool();
    bootMock.pool = pool;
    bootMock.relays = [RELAY];
    bootMock.store = { query: () => [], getCursor: () => null, setCursor: vi.fn() };
    bootMock.auth.status = 'unauthenticated';
    bootMock.auth.pubkey = '';
    teardownContinuityStoreBinding();
    initContinuityStoreBinding();
    return { pool, requests };
  }

  it('subscribes only under trusted authors and adds operator families when an operator signs in', async () => {
    const { pool, requests } = start();
    const authorsOf = () => requests.flatMap((request) => request.filters.map((filter: any) => filter.authors));
    expect(requests.length).toBeGreaterThan(0);
    expect(authorsOf().every((authors) => authors.length === 1 && authors[0] === SERVICE_AUTHOR)).toBe(true);
    expect(requests.flatMap((request) => request.filters.flatMap((filter: any) => filter.kinds))).not.toContain(31400);

    await vi.waitFor(() => expect(pool.subscribe.mock.calls.length).toBeGreaterThanOrEqual(4));
    const beforeLogin = pool.subscribe.mock.calls.length;
    bootMock.auth.status = 'authenticated';
    bootMock.auth.pubkey = OPERATOR;
    initContinuityStoreBinding();
    const added = requests.slice(beforeLogin);
    expect(added.length).toBeGreaterThan(0);
    expect(added.every((request) => request.filters.every((filter: any) => filter.authors[0] === OPERATOR))).toBe(true);
    expect(added[0].filters.map((filter: any) => filter.kinds)).toEqual([[31400, 31401, 31402, 31403, 31404], [38430, 38431], [30315]]);
    expect(authorsOf().every((authors) => Array.isArray(authors) && authors.length === 1)).toBe(true);
    teardownContinuityStoreBinding();
  });

  it('lets pages come and go without opening or closing a relay REQ', async () => {
    const { pool } = start();
    await vi.waitFor(() => expect(pool.subscribe.mock.calls.length).toBeGreaterThanOrEqual(4));
    const opened = pool.subscribe.mock.calls.length;
    const closed = () => pool.subscribe.mock.results.filter((result: any) => result.value.unsubscribe.mock.calls.length > 0).length;
    const closedBefore = closed();
    for (let visit = 0; visit < 3; visit += 1) {
      const leave = await subscribeToContinuityDashboard({ onUpdate: () => {} });
      initContinuityStoreBinding();
      leave();
    }
    expect(pool.subscribe).toHaveBeenCalledTimes(opened);
    expect(closed()).toBe(closedBefore);
    teardownContinuityStoreBinding();
  });

  it('reports relay catch-up as a badge on an already rendered snapshot and skips unchanged refreshes', async () => {
    const { pool } = start();
    const updates: any[] = [];
    const leave = await subscribeToContinuityDashboard({ onUpdate: (snapshot) => updates.push(snapshot) });
    expect(updates).toHaveLength(1);
    expect(updates[0]).toMatchObject({ ready: true, synced: false, error: null });
    await vi.waitFor(() => expect(updates.at(-1).synced).toBe(true));
    const delivered = updates.length;
    bootMock.refresh();
    expect(updates).toHaveLength(delivered);
    expect(pool.subscribe.mock.calls.every(([options]: any) => options.filters.every((filter: any) => filter.authors?.length))).toBe(true);
    leave();
    teardownContinuityStoreBinding();
  });
});
