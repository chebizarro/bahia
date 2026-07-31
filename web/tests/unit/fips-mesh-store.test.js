import { describe, it, expect, beforeEach, vi } from 'vitest';

const systemInfoMock = vi.hoisted(() => ({
  loadSystemInfo: vi.fn()
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
    subscribeWithRecovery: vi.fn()
  };
});

vi.mock('../../src/lib/stores/system.svelte.js', () => ({
  loadSystemInfo: systemInfoMock.loadSystemInfo
}));

vi.mock('../../src/lib/nostr/client.js', async () => {
  const actual = await vi.importActual('../../src/lib/nostr/client.js');
  return {
    ...actual,
    nostr: nostrMock
  };
});

function event({ id, kind = 30900, pubkey = 'b'.repeat(64), created_at = 100, tags = [], content = {} }) {
  return {
    id,
    kind,
    pubkey,
    created_at,
    tags,
    content: typeof content === 'string' ? content : JSON.stringify(content)
  };
}

function meshEndpoint(overrides = {}) {
  return event({
    id: overrides.id || 'mesh-endpoint-1',
    created_at: overrides.created_at || 100,
    tags: overrides.tags || [['domain', 'dns'], ['schema', 'bahia.state.dns-endpoint.v1'], ['d', 'endpoint:mesh:worker-a:mesh'], ['family', 'mesh'], ['mesh', 'fips'], ['npub', 'worker-a'], ['dns', 'worker-a.mesh.example'], ['addr', 'fd00::1'], ['health', 'healthy']],
    content: {
      family: 'mesh',
      worker_pubkey: 'worker-a',
      name: 'worker-a',
      fqdn: 'worker-a.mesh.example',
      address: 'fd00::1',
      source: 'worker_fips_overlay',
      health: 'healthy',
      metadata: { projection_status: 'projected' },
      ...overrides.content
    },
    ...overrides
  });
}

describe('FIPS mesh store', () => {
  let store;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    nostrMock.connected.set(false);
    nostrMock.connect.mockImplementation(async () => {
      nostrMock.connected.set(true);
    });
    nostrMock.subscribeWithRecovery.mockReturnValue(vi.fn());
    systemInfoMock.loadSystemInfo.mockResolvedValue({
      nostr: {
        browser_relays: ['http://localhost:10547/relay'],
        service_pubkey: 'b'.repeat(64)
      }
    });
    store = await import('../../src/lib/stores/fips-mesh.svelte.js');
    store.resetFipsMeshStore();
  });

  it('builds narrow mesh read-model filters', () => {
    store.fipsMeshState.servicePubkey = 'b'.repeat(64);

    expect(store.fipsMeshReadModelFilters()).toEqual([
      expect.objectContaining({ kinds: [30900], '#domain': ['dns'], '#schema': ['bahia.state.dns-endpoint.v1'], '#family': ['mesh'], '#mesh': ['fips'], authors: ['b'.repeat(64)], limit: 1000 }),
      expect.objectContaining({ kinds: [30900], '#domain': ['dns'], '#schema': ['bahia.state.dns-endpoint.v1'], '#family': ['worker'], '#mesh': ['fips'], authors: ['b'.repeat(64)], limit: 1000 }),
      expect.objectContaining({ kinds: [30900], '#domain': ['worker'], '#schema': ['bahia.state.worker.v1'], authors: ['b'.repeat(64)], limit: 1000 })
    ]);
  });

  it('bootstraps through a persistent subscription and marks ready on EOSE', async () => {
    const initial = meshEndpoint();

    const result = await store.bootstrapFipsMesh();
    const callbacks = nostrMock.subscribeWithRecovery.mock.calls[0][1];
    callbacks.onEvent(initial, 'relay-a');
    callbacks.onEose('relay-a');

    expect(result.ok).toBe(true);
    expect(nostrMock.setRelays).toHaveBeenCalledWith(['ws://localhost:10547/relay'], false);
    expect(nostrMock.connect).toHaveBeenCalledWith(['ws://localhost:10547/relay'], { force: true });
    expect(nostrMock.subscribeWithRecovery).toHaveBeenCalledWith(expect.arrayContaining([
      expect.objectContaining({ kinds: [30900], '#domain': ['dns'], '#schema': ['bahia.state.dns-endpoint.v1'], '#family': ['mesh'], '#mesh': ['fips'] })
    ]), expect.objectContaining({
      onEvent: expect.any(Function),
      onEose: expect.any(Function),
      onHealth: expect.any(Function),
      onClosed: expect.any(Function)
    }));
    expect(store.fipsMeshState.bootstrapComplete).toBe(true);
    expect(store.meshEndpoints).toHaveLength(1);
    expect(store.meshNodes[0]).toMatchObject({ pubkey: 'worker-a', overlayAddress: 'fd00::1', health: 'healthy' });
  });

  it('applies live EVENT, EOSE, and CLOSED callbacks without polling', async () => {
    await store.bootstrapFipsMesh();
    const callbacks = nostrMock.subscribeWithRecovery.mock.calls[0][1];

    callbacks.onEvent(meshEndpoint({ id: 'live-endpoint', created_at: 120, content: { fqdn: 'live.mesh.example' } }), 'relay-a');
    callbacks.onHealth({
      lastEoseAt: '2026-07-30T12:00:00.000Z',
      resubscribeAttempts: 2,
      lastClosedReason: 'rate-limited'
    });
    callbacks.onEose('relay-a');
    callbacks.onClosed('rate-limited', 'relay-a', { terminal: true, source: 'closed' });

    expect(store.meshEndpoints.map((endpoint) => endpoint.fqdn)).toContain('live.mesh.example');
    expect(store.fipsMeshState).toMatchObject({
      lastEoseAt: '2026-07-30T12:00:00.000Z',
      resubscribeAttempts: 2,
      lastClosedReason: 'rate-limited'
    });
    expect(store.fipsMeshState.lastClosed).toMatchObject({ reason: 'rate-limited', relay: 'relay-a', terminal: true });
    expect(store.fipsMeshState.status).toBe('degraded');
  });

  it('dedupes parameterized replaceable endpoint events by author and d tag', () => {
    const older = meshEndpoint({ id: 'older', created_at: 100, content: { fqdn: 'old.mesh.example' } });
    const stale = meshEndpoint({ id: 'stale', created_at: 90, content: { fqdn: 'stale.mesh.example' } });
    const newer = meshEndpoint({ id: 'newer', created_at: 120, content: { fqdn: 'new.mesh.example' } });

    expect(store.applyFipsMeshEvent(older)).toBe(true);
    expect(store.applyFipsMeshEvent(stale)).toBe(false);
    expect(store.applyFipsMeshEvent(newer)).toBe(true);

    expect(store.meshEndpoints).toHaveLength(1);
    expect(store.meshEndpoints[0].fqdn).toBe('new.mesh.example');
  });

  it('ignores tombstones and removes older live state', () => {
    const active = meshEndpoint({ id: 'active', created_at: 100 });
    const tombstone = meshEndpoint({
      id: 'deleted',
      created_at: 130,
      tags: [['domain', 'dns'], ['schema', 'bahia.state.dns-endpoint.v1'], ['d', 'endpoint:mesh:worker-a:mesh'], ['family', 'mesh'], ['mesh', 'fips'], ['deleted', 'true']],
      content: { deleted: true, coordinate: 'endpoint:mesh:worker-a:mesh' }
    });
    const lateReplay = meshEndpoint({ id: 'late-replay', created_at: 120, content: { fqdn: 'late.mesh.example' } });

    expect(store.applyFipsMeshEvent(active)).toBe(true);
    expect(store.meshEndpoints).toHaveLength(1);
    expect(store.applyFipsMeshEvent(tombstone)).toBe(true);
    expect(store.meshEndpoints).toEqual([]);
    expect(store.applyFipsMeshEvent(lateReplay)).toBe(false);
    expect(store.meshEndpoints).toEqual([]);
  });

  it('filters non-mesh DNS endpoint read models locally after relay delivery', () => {
    const serviceEndpoint = event({
      id: 'service-endpoint',
      tags: [['domain', 'dns'], ['schema', 'bahia.state.dns-endpoint.v1'], ['d', 'endpoint:service:api:prod'], ['family', 'service'], ['dns', 'api.example']],
      content: { family: 'service', fqdn: 'api.example', address: '203.0.113.10', source: 'service_state' }
    });

    expect(store.applyFipsMeshEvent(serviceEndpoint)).toBe(false);
    expect(store.meshEndpoints).toEqual([]);
  });

  it('merges worker state overlay fields with DNS/FIPS endpoint state', () => {
    const worker = event({
      id: 'worker-state',
      kind: 30900,
      created_at: 100,
      tags: [['domain', 'worker'], ['schema', 'bahia.state.worker.v1'], ['d', 'worker-a'], ['worker', 'worker-a'], ['status', 'online']],
      content: {
        pubkey: 'worker-a',
        name: 'Worker A',
        status: 'online',
        fips_overlay_addr: 'fd00::10',
        fips_endpoints: [{ transport: 'quic', address: 'fd00::10:443' }],
        mesh_health: { rtt: 500_000_000, loss: 0.01 }
      }
    });

    expect(store.applyFipsMeshEvent(worker)).toBe(true);
    expect(store.applyFipsMeshEvent(meshEndpoint())).toBe(true);

    expect(store.meshNodes).toHaveLength(1);
    expect(store.meshNodes[0]).toMatchObject({
      pubkey: 'worker-a',
      name: 'Worker A',
      overlayAddress: 'fd00::10',
      health: 'healthy'
    });
    expect(store.meshNodes[0].transportEndpoints).toEqual(expect.arrayContaining([expect.objectContaining({ transport: 'quic' })]));
    expect(store.meshNodes[0].dnsHostnames).toEqual(['worker-a.mesh.example']);
  });

  it('classifies FIPS mesh health deterministically', () => {
    expect(store.classifyHealth({ worker: { status: 'online', mesh_health: { rtt: 500_000_000, loss: 0.01 } } })).toBe('healthy');
    expect(store.classifyHealth({ worker: { status: 'online', mesh_health: { rtt: 2_000_000_000, loss: 0.01 } } })).toBe('degraded');
    expect(store.classifyHealth({ worker: { status: 'online', mesh_health: { rtt: 500_000_000, loss: 0.2 } } })).toBe('degraded');
    expect(store.classifyHealth({ worker: { status: 'online', mesh_health: { rtt: 6_000_000_000, loss: 0.01 } } })).toBe('unhealthy');
    expect(store.classifyHealth({ worker: { status: 'offline', mesh_health: { rtt: 1, loss: 0 } } })).toBe('unhealthy');
    expect(store.classifyHealth()).toBe('unknown');
  });
});
