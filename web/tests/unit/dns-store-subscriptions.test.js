import { describe, it, expect, beforeEach, vi } from 'vitest';

const systemInfoMock = vi.hoisted(() => ({
  loadSystemInfo: vi.fn()
}));

const controlplaneMock = vi.hoisted(() => ({
  bootstrapControlplane: vi.fn()
}));

const dnsCommandMock = vi.hoisted(() => ({
  startDNSCommand: vi.fn(),
  dnsResultIsFailure: vi.fn((result) => ['error', 'failed', 'rejected'].includes(String(result?.status || '').toLowerCase())),
  DNS_COMMANDS: {
    ZONE_CREATE: 'zone_create',
    POLICY_APPLY: 'policy_apply',
    RECORD_OVERRIDE: 'record_override',
    DRIFT_REMEDIATE: 'drift_remediate'
  }
}));

const nostrMock = vi.hoisted(() => {
  function store(initial) {
    let value = initial;
    const subscribers = new Set();
    return {
      subscribe(fn) {
        subscribers.add(fn);
        fn(value);
        return () => subscribers.delete(fn);
      },
      set(next) {
        value = next;
        for (const fn of subscribers) fn(value);
      }
    };
  }

  return {
    connected: store(false),
    setRelays: vi.fn(),
    connect: vi.fn(),
    queryUntilEose: vi.fn(),
    subscribe: vi.fn()
  };
});

vi.mock('../../src/lib/stores/system.svelte.js', () => ({
  loadSystemInfo: systemInfoMock.loadSystemInfo
}));

vi.mock('../../src/lib/stores/controlplane.svelte.js', () => ({
  bootstrapControlplane: controlplaneMock.bootstrapControlplane
}));

vi.mock('../../src/lib/nostr/dns-controlplane.js', () => dnsCommandMock);

vi.mock('../../src/lib/nostr/client.js', async () => {
  const actual = await vi.importActual('../../src/lib/nostr/client.js');
  return {
    ...actual,
    nostr: nostrMock
  };
});

function event({ id, kind, pubkey = 'b'.repeat(64), created_at = 100, tags = [], content = {} }) {
  return {
    id,
    kind,
    pubkey,
    created_at,
    tags,
    content: typeof content === 'string' ? content : JSON.stringify(content)
  };
}

function zoneEvent(overrides = {}) {
  return event({
    id: overrides.id || 'zone-1',
    kind: 31975,
    tags: overrides.tags || [['d', 'zone:prod.example'], ['zone', 'prod.example'], ['backend', 'coredns'], ['t', 'dns-zone'], ['t', 'bahia']],
    content: { name: 'prod.example', visibility: 'public', backend_ref: 'coredns', health: 'healthy', ...overrides.content },
    ...overrides
  });
}

function endpointEvent(overrides = {}) {
  return event({
    id: overrides.id || 'endpoint-1',
    kind: 31976,
    tags: overrides.tags || [['d', 'endpoint:service:api:prod'], ['family', 'service'], ['environment', 'prod'], ['dns', 'api.prod.example'], ['addr', '10.0.1.44'], ['health', 'healthy'], ['t', 'dns-endpoint'], ['t', 'bahia']],
    content: { id: 'endpoint-row-id', coordinate: 'endpoint:service:api:prod', family: 'service', name: 'api', environment: 'prod', zone: 'prod.example', fqdn: 'api.prod.example', address: '10.0.1.44', health: 'healthy', ...overrides.content },
    ...overrides
  });
}

function policyEvent(overrides = {}) {
  return event({
    id: overrides.id || 'policy-1',
    kind: 31977,
    tags: overrides.tags || [['d', 'dnspolicy:policy-a'], ['policy', 'policy-a'], ['t', 'dns-policy'], ['t', 'bahia']],
    content: { id: 'policy-a', name: 'Internal only', enabled: true, rules: [{ action: 'allow' }], ...overrides.content },
    ...overrides
  });
}

function backendEvent(overrides = {}) {
  return event({
    id: overrides.id || 'backend-1',
    kind: 31978,
    tags: overrides.tags || [['d', 'dnsbackend:coredns'], ['backend', 'coredns'], ['health', 'healthy'], ['t', 'dns-backend'], ['t', 'bahia']],
    content: { ref: 'coredns', type: 'coredns', health: 'healthy', zones: ['prod.example'], ...overrides.content },
    ...overrides
  });
}

function driftEvent(overrides = {}) {
  return event({
    id: overrides.id || 'drift-1',
    kind: 31022,
    tags: overrides.tags || [['event_type', 'dns.drift_detected'], ['zone', 'prod.example'], ['fqdn', 'api.prod.example']],
    content: { event_type: 'dns.drift_detected', data: { zone: 'prod.example', fqdn: 'api.prod.example', expected: '10.0.1.44', actual: '10.0.1.45' }, ...overrides.content },
    ...overrides
  });
}

describe('DNS dashboard Nostr subscription store', () => {
  let store;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    global.fetch = vi.fn(async () => ({ ok: true, json: async () => ({ supported_nips: [1, 11, 42] }) }));
    nostrMock.connected.set(false);
    nostrMock.connect.mockImplementation(async () => {
      nostrMock.connected.set(true);
      return { connected: 1, total: 1, failed: 0, connecting: 0, relays: [{ url: 'ws://localhost:10547/relay', status: 'connected' }] };
    });
    nostrMock.queryUntilEose.mockResolvedValue([]);
    nostrMock.subscribe.mockReturnValue(vi.fn());
    systemInfoMock.loadSystemInfo.mockResolvedValue({
      nostr: {
        browser_relays: ['http://localhost:10547/relay'],
        service_pubkey: 'b'.repeat(64)
      }
    });
    store = await import('../../src/lib/stores/dns.svelte.js');
    store.resetDnsStore();
  });

  it('builds narrow DNS read-model filters scoped by kind, tags, and service author', () => {
    store.dnsState.connection.servicePubkey = 'b'.repeat(64);

    expect(store.dnsReadModelFilters()).toEqual(expect.arrayContaining([
      expect.objectContaining({ kinds: [31975], '#t': ['dns-zone'], authors: ['b'.repeat(64)], limit: 5000 }),
      expect.objectContaining({ kinds: [31976], '#t': ['dns-endpoint'], '#family': ['service'], authors: ['b'.repeat(64)], limit: 5000 }),
      expect.objectContaining({ kinds: [31977], '#t': ['dns-policy'], authors: ['b'.repeat(64)], limit: 5000 }),
      expect.objectContaining({ kinds: [31978], '#t': ['dns-backend'], authors: ['b'.repeat(64)], limit: 5000 }),
      expect.objectContaining({ kinds: [31020, 31021, 31022, 31023, 31024], '#event_type': expect.arrayContaining(['dns.drift_detected']), authors: ['b'.repeat(64)], limit: 5000 })
    ]));
  });

  it('bootstraps zones, endpoints, policies, backends, and drift via EOSE and keeps a live subscription open without REST fetches', async () => {
    nostrMock.queryUntilEose.mockResolvedValue([zoneEvent(), endpointEvent(), policyEvent(), backendEvent(), driftEvent()]);

    const result = await store.bootstrapDnsDashboard();

    expect(result.ok).toBe(true);
    expect(nostrMock.setRelays).toHaveBeenCalledWith(['ws://localhost:10547/relay'], false);
    expect(nostrMock.connect).toHaveBeenCalledWith(['ws://localhost:10547/relay'], { force: true });
    expect(nostrMock.queryUntilEose).toHaveBeenCalledWith(expect.arrayContaining([
      expect.objectContaining({ kinds: [31976], '#t': ['dns-endpoint'], '#family': ['service'] })
    ]));
    expect(nostrMock.subscribe).toHaveBeenCalledWith(expect.any(Array), expect.objectContaining({
      onEvent: expect.any(Function),
      onEose: expect.any(Function),
      onClosed: expect.any(Function),
      onAuth: expect.any(Function)
    }));
    expect(store.dnsState.zones[0]).toMatchObject({ name: 'prod.example' });
    expect(store.dnsState.endpoints[0]).toMatchObject({ id: 'endpoint:service:api:prod', fqdn: 'api.prod.example' });
    expect(store.dnsState.policies[0]).toMatchObject({ id: 'policy-a', name: 'Internal only' });
    expect(store.dnsState.backends[0]).toMatchObject({ id: 'coredns', ref: 'coredns' });
    expect(store.dnsState.driftEvents[0]).toMatchObject({ event_type: 'dns.drift_detected', record: 'api.prod.example' });
    expect(global.fetch).not.toHaveBeenCalledWith(expect.stringContaining('/api/v1/dns'), expect.anything());
  });

  it('applies live EVENT updates after bootstrap', async () => {
    await store.bootstrapDnsDashboard();
    const callbacks = nostrMock.subscribe.mock.calls[0][1];

    callbacks.onEvent(endpointEvent({ id: 'live-endpoint', created_at: 120, content: { fqdn: 'live.prod.example', address: '10.0.1.55' } }), 'relay-a');
    callbacks.onEose('relay-a');

    expect(store.dnsState.endpoints).toHaveLength(1);
    expect(store.dnsState.endpoints[0]).toMatchObject({ fqdn: 'live.prod.example', address: '10.0.1.55' });
    expect(store.dnsState.connection.lastEoseAt).toEqual(expect.any(String));
    expect(store.dnsState.connection.status).toBe('live');
  });

  it('dedupes replaceable events by author and d tag and removes tombstoned read models', () => {
    const active = endpointEvent({ id: 'active', created_at: 100, content: { fqdn: 'old.prod.example' } });
    const newer = endpointEvent({ id: 'newer', created_at: 120, content: { fqdn: 'new.prod.example' } });
    const stale = endpointEvent({ id: 'stale', created_at: 110, content: { fqdn: 'stale.prod.example' } });
    const tombstone = endpointEvent({
      id: 'deleted',
      created_at: 130,
      tags: [['d', 'endpoint:service:api:prod'], ['family', 'service'], ['deleted', 'true'], ['t', 'dns-endpoint'], ['t', 'bahia']],
      content: { deleted: true, coordinate: 'endpoint:service:api:prod' }
    });

    expect(store.applyDnsEvent(active)).toBe(true);
    expect(store.applyDnsEvent(newer)).toBe(true);
    expect(store.applyDnsEvent(stale)).toBe(false);
    expect(store.dnsState.endpoints).toHaveLength(1);
    expect(store.dnsState.endpoints[0].fqdn).toBe('new.prod.example');
    expect(store.applyDnsEvent(tombstone)).toBe(true);
    expect(store.dnsState.endpoints).toEqual([]);
    expect(store.applyDnsEvent(stale)).toBe(false);
    expect(store.dnsState.endpoints).toEqual([]);
  });

  it('surfaces CLOSED and AUTH subscription errors visibly', async () => {
    await store.bootstrapDnsDashboard();
    const callbacks = nostrMock.subscribe.mock.calls[0][1];

    callbacks.onClosed('rate-limited', 'relay-a', { terminal: true, source: 'closed' });
    expect(store.dnsState.connection.lastClosed).toMatchObject({ reason: 'rate-limited', relay: 'relay-a', terminal: true });
    expect(store.dnsState.error.subscription).toContain('rate-limited');
    expect(store.dnsState.connection.status).toBe('degraded');

    callbacks.onAuth('auth-required', 'relay-b');
    expect(store.dnsState.connection.lastClosed).toMatchObject({ reason: 'auth-required', relay: 'relay-b', source: 'auth' });
    expect(store.dnsState.error.subscription).toContain('requires AUTH');
  });
});
