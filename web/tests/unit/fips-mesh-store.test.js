import { describe, it, expect, beforeEach, vi } from 'vitest';
import { finalizeEvent, getPublicKey } from 'nostr-tools';

// Producer contract (literals pin the wire shape, not the web constants):
// - DNS endpoints: internal/adapters/nostr/projector.go controlStateEnvelope +
//   dnsEndpointTags on kind 30900 (schema bahia.cp-state.v1, legacy_kind 31976,
//   deleted=false|true, t=dns-endpoint); content is domain.DNSEndpoint, or
//   {deleted, coordinate, fqdn, updated_at} for publishDNSEndpointTombstone.
// - Worker state: internal/controlplane/worker_state_publisher.go +
//   worker_cp_state.go on kind 30900 (d=worker:state:<pk>, domain=worker,
//   schema bahia.cp-state.v1, legacy_kind 32000, deleted=false|true,
//   t=worker-state); content is the full domain.Worker JSON (mesh_health
//   durations in nanoseconds) plus deleted.

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

function secretKey(byte) {
  return Uint8Array.from({ length: 32 }, () => byte);
}

const SERVICE_SECRET = secretKey(0x11);
const FORGER_SECRET = secretKey(0x44);
const WORKER_SECRET = secretKey(0x22);
const SERVICE = getPublicKey(SERVICE_SECRET);
const WORKER = getPublicKey(WORKER_SECRET);
const COORDINATE = `endpoint:service:checkout-api:production`;

function signed(template, secret = SERVICE_SECRET) {
  return finalizeEvent({ kind: 30900, content: '', ...template, content: JSON.stringify(template.content ?? {}) }, secret);
}

function envelope({ legacyKind, d, deleted = false, domain }) {
  return [
    ['d', d],
    ['domain', domain],
    ['schema', 'bahia.cp-state.v1'],
    ['legacy_kind', String(legacyKind)],
    ['deleted', String(deleted)]
  ];
}

function liveEndpoint({ created_at = 100, fqdn = 'checkout.prod.example.com', workerPubkey = WORKER, family = 'service', d = COORDINATE, secret } = {}) {
  const meshTags = workerPubkey ? [['npub', workerPubkey], ['mesh', 'fips']] : [];
  return signed({
    created_at,
    tags: [
      ...envelope({ legacyKind: 31976, d, domain: 'dns' }),
      ['family', family], ['health', 'healthy'], ['dns', fqdn], ['addr', 'fd00::1'], ['t', 'dns-endpoint'], ['t', 'bahia'],
      ['environment', 'production'], ['proto', 'https'], ['port', '443'],
      ...meshTags,
      ['service', 'checkout-api']
    ],
    content: {
      id: '5d1c1c42-6f0a-5b2f-9d7e-1f3b2a4c5d6e',
      worker_pubkey: workerPubkey || undefined,
      family,
      name: 'checkout-api',
      environment: 'production',
      zone: 'prod.example.com',
      fqdn,
      coordinate: d,
      protocol: 'https',
      address: 'fd00::1',
      port: 443,
      health: 'healthy',
      drift_status: 'in_sync',
      source: workerPubkey ? 'fips' : 'service_state',
      metadata: workerPubkey ? { mesh: 'fips', projection_status: 'projected' } : { projection_status: 'projected' },
      materialized_at: '2026-09-30T00:00:00Z'
    }
  }, secret);
}

function endpointTombstone({ created_at = 200, d = COORDINATE, fqdn = 'checkout.prod.example.com' } = {}) {
  return signed({
    created_at,
    tags: [...envelope({ legacyKind: 31976, d, deleted: true, domain: 'dns' }), ['t', 'dns-endpoint'], ['t', 'bahia'], ['dns', fqdn]],
    content: { deleted: true, coordinate: d, fqdn, updated_at: '2026-09-30T00:01:00Z' }
  });
}

function workerState({ created_at = 100, name = 'worker-one', status = 'online', deleted = false, mesh = {} } = {}) {
  return signed({
    created_at,
    tags: [
      ...envelope({ legacyKind: 32000, d: `worker:state:${WORKER}`, deleted, domain: 'worker' }),
      ['t', 'worker-state'],
      ['worker', WORKER], ['status', status], ['scheduling_state', 'active']
    ],
    content: {
      deleted, pubkey: WORKER, name, status, scheduling_state: 'active', labels: { zone: 'a' }, capabilities: {},
      max_concurrent_jobs: 4, current_queue_depth: 0, last_advertisement_at: '2026-09-30T00:00:00.000000005Z',
      ...mesh
    }
  });
}

const MESH_FIELDS = {
  fips_overlay_addr: 'fd00::42',
  fips_endpoints: [{ transport: 'udp', address: '203.0.113.7:4242' }],
  mesh_health: { rtt: 40_000_000, loss: 0.01, jitter: 0, goodput: 0, last_report: '2026-09-30T00:00:00Z' }
};

describe('FIPS mesh store (producer 30900 contract)', () => {
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
        service_pubkey: SERVICE
      }
    });
    store = await import('../../src/lib/stores/fips-mesh.svelte.js');
    store.resetFipsMeshStore();
    store.fipsMeshState.servicePubkey = SERVICE;
  });

  it('REQs endpoints and worker state on indexed #t topics by author', () => {
    expect(store.fipsMeshReadModelFilters()).toEqual([
      { kinds: [30900], '#t': ['dns-endpoint'], authors: [SERVICE], limit: 1000 },
      { kinds: [30900], '#t': ['worker-state'], authors: [SERVICE], limit: 1000 }
    ]);
    for (const filter of store.fipsMeshReadModelFilters()) {
      expect(filter).not.toHaveProperty('#schema');
      expect(filter).not.toHaveProperty('#domain');
    }
  });

  it('bootstraps through a persistent subscription and marks ready on EOSE', async () => {
    const result = await store.bootstrapFipsMesh();
    const [filters, callbacks] = nostrMock.subscribeWithRecovery.mock.calls[0];
    callbacks.onEvent(liveEndpoint(), 'relay-a');
    callbacks.onEose('relay-a');

    expect(result.ok).toBe(true);
    expect(filters).toEqual(store.fipsMeshReadModelFilters());
    expect(nostrMock.setRelays).toHaveBeenCalledWith(['ws://localhost:10547/relay'], false);
    expect(nostrMock.connect).toHaveBeenCalledWith(['ws://localhost:10547/relay'], { force: true });
    expect(store.fipsMeshState.bootstrapComplete).toBe(true);
    expect(store.meshEndpoints).toHaveLength(1);
    expect(store.meshNodes[0]).toMatchObject({ pubkey: WORKER, overlayAddress: 'fd00::1', health: 'healthy' });
  });

  it('refuses to bootstrap without a service author to scope on', async () => {
    systemInfoMock.loadSystemInfo.mockResolvedValue({ nostr: { browser_relays: ['ws://relay.example'] } });
    store.resetFipsMeshStore();

    const result = await store.bootstrapFipsMesh();

    expect(result).toEqual({ ok: false, reason: 'No Bahia service pubkey configured for FIPS mesh read models' });
    expect(nostrMock.subscribeWithRecovery).not.toHaveBeenCalled();
  });

  it('applies a live cp-state endpoint; deleted=false is not a tombstone', () => {
    expect(store.applyFipsMeshEvent(liveEndpoint())).toBe(true);

    expect(store.meshEndpoints).toHaveLength(1);
    expect(store.meshEndpoints[0]).toMatchObject({
      coordinate: COORDINATE,
      fqdn: 'checkout.prod.example.com',
      workerPubkey: WORKER,
      address: 'fd00::1',
      port: 443,
      protocol: 'https',
      health: 'healthy',
      projectionStatus: 'projected'
    });
    expect(store.meshNodes[0].dnsHostnames).toEqual(['checkout.prod.example.com']);
  });

  it('removes the endpoint on its tombstone and rejects an older live replay', () => {
    expect(store.applyFipsMeshEvent(liveEndpoint({ created_at: 100 }))).toBe(true);
    expect(store.applyFipsMeshEvent(endpointTombstone({ created_at: 200 }))).toBe(true);
    expect(store.meshEndpoints).toEqual([]);
    expect(store.meshNodes).toEqual([]);

    expect(store.applyFipsMeshEvent(liveEndpoint({ created_at: 150, fqdn: 'late.prod.example.com' }))).toBe(false);
    expect(store.meshEndpoints).toEqual([]);

    expect(store.applyFipsMeshEvent(liveEndpoint({ created_at: 300, fqdn: 'back.prod.example.com' }))).toBe(true);
    expect(store.meshEndpoints.map((endpoint) => endpoint.fqdn)).toEqual(['back.prod.example.com']);
  });

  it('keeps the newest record per (kind, pubkey, d), lowest id on a created_at tie', () => {
    expect(store.applyFipsMeshEvent(liveEndpoint({ created_at: 100, fqdn: 'old.prod.example.com' }))).toBe(true);
    expect(store.applyFipsMeshEvent(liveEndpoint({ created_at: 90, fqdn: 'stale.prod.example.com' }))).toBe(false);
    expect(store.meshEndpoints[0].fqdn).toBe('old.prod.example.com');

    const tieA = liveEndpoint({ created_at: 120, fqdn: 'tie-a.prod.example.com' });
    const tieB = liveEndpoint({ created_at: 120, fqdn: 'tie-b.prod.example.com' });
    const [lower, higher] = tieA.id < tieB.id ? [tieA, tieB] : [tieB, tieA];
    expect(store.applyFipsMeshEvent(higher)).toBe(true);
    expect(store.applyFipsMeshEvent(lower)).toBe(true);
    expect(store.applyFipsMeshEvent(higher)).toBe(false);
    expect(store.meshEndpoints).toHaveLength(1);
    expect(store.meshEndpoints[0].nostrEventId).toBe(lower.id);
  });

  it('rejects endpoint and worker records signed by any author but the service', () => {
    const forgedEndpoint = liveEndpoint({ secret: FORGER_SECRET, fqdn: 'evil.prod.example.com' });
    expect(forgedEndpoint.pubkey).not.toBe(SERVICE);
    expect(store.applyFipsMeshEvent(forgedEndpoint)).toBe(false);

    expect(store.applyFipsMeshEvent(liveEndpoint({ created_at: 100 }))).toBe(true);
    const forgedTombstone = finalizeEvent({ ...endpointTombstone({ created_at: 500 }), id: undefined, sig: undefined, pubkey: undefined }, FORGER_SECRET);
    expect(store.applyFipsMeshEvent(forgedTombstone)).toBe(false);
    expect(store.meshEndpoints.map((endpoint) => endpoint.fqdn)).toEqual(['checkout.prod.example.com']);

    store.fipsMeshState.servicePubkey = '';
    expect(store.applyFipsMeshEvent(liveEndpoint({ created_at: 600, fqdn: 'unscoped.prod.example.com' }))).toBe(false);
  });

  it('ignores non-mesh endpoints and drops a mesh coordinate that stops being mesh', () => {
    expect(store.applyFipsMeshEvent(liveEndpoint({ d: 'endpoint:service:public', workerPubkey: '', fqdn: 'public.example.com' }))).toBe(false);
    expect(store.meshEndpoints).toEqual([]);

    expect(store.applyFipsMeshEvent(liveEndpoint({ created_at: 100 }))).toBe(true);
    expect(store.applyFipsMeshEvent(liveEndpoint({ created_at: 110, workerPubkey: '' }))).toBe(true);
    expect(store.meshEndpoints).toEqual([]);
  });

  it('ignores other cp-state families even on the same REQ', () => {
    const zone = signed({
      created_at: 100,
      tags: [...envelope({ legacyKind: 31975, d: 'zone:prod.example.com', domain: 'dns' }), ['t', 'dns-zone']],
      content: { name: 'prod.example.com', deleted: false }
    });
    const assignment = signed({
      created_at: 100,
      tags: [...envelope({ legacyKind: 32001, d: WORKER, domain: 'worker' }), ['worker', WORKER]],
      content: { worker_pubkey: WORKER, active_assignments: [] }
    });

    expect(store.applyFipsMeshEvent(zone)).toBe(false);
    expect(store.applyFipsMeshEvent(assignment)).toBe(false);
    expect(store.meshNodes).toEqual([]);
  });

  it('merges publisher worker state with its mesh endpoints and honours worker tombstones', () => {
    expect(store.applyFipsMeshEvent(workerState())).toBe(true);
    expect(store.applyFipsMeshEvent(liveEndpoint())).toBe(true);

    expect(store.meshNodes).toHaveLength(1);
    expect(store.meshNodes[0]).toMatchObject({ pubkey: WORKER, name: 'worker-one', status: 'online', overlayAddress: 'fd00::1', health: 'healthy' });
    expect(store.meshNodes[0].dnsHostnames).toEqual(['checkout.prod.example.com']);

    expect(store.applyFipsMeshEvent(workerState({ created_at: 90, name: 'stale-name' }))).toBe(false);
    expect(store.applyFipsMeshEvent(workerState({ created_at: 200, deleted: true }))).toBe(true);
    expect(store.meshNodes).toHaveLength(1);
    expect(store.meshNodes[0].name).not.toBe('worker-one');
  });

  it('builds a mesh node from worker state alone on #t, ignores stale records and drops it on tombstone', () => {
    const [, workerFilter] = store.fipsMeshReadModelFilters();
    const live = workerState({ mesh: MESH_FIELDS });
    expect(live.tags.filter((tag) => tag[0] === 't').map((tag) => tag[1])).toEqual(workerFilter['#t']);

    expect(store.applyFipsMeshEvent(live)).toBe(true);
    expect(store.meshNodes).toHaveLength(1);
    expect(store.meshNodes[0]).toMatchObject({
      pubkey: WORKER,
      name: 'worker-one',
      overlayAddress: 'fd00::42',
      fipsEndpoints: [{ transport: 'udp', address: '203.0.113.7:4242' }],
      health: 'healthy'
    });

    expect(store.applyFipsMeshEvent(workerState({ created_at: 90, name: 'stale-name', mesh: MESH_FIELDS }))).toBe(false);
    expect(store.meshNodes[0].name).toBe('worker-one');

    const degraded = workerState({ created_at: 150, mesh: { ...MESH_FIELDS, mesh_health: { ...MESH_FIELDS.mesh_health, loss: 0.2 } } });
    expect(store.applyFipsMeshEvent(degraded)).toBe(true);
    expect(store.meshNodes[0].health).toBe('degraded');

    expect(store.applyFipsMeshEvent(workerState({ created_at: 200, deleted: true, mesh: MESH_FIELDS }))).toBe(true);
    expect(store.meshNodes).toEqual([]);
    expect(store.applyFipsMeshEvent(workerState({ created_at: 180, mesh: MESH_FIELDS }))).toBe(false);
    expect(store.meshNodes).toEqual([]);
  });

  it('still routes migrated per-family worker records (nostrmigration keeps t=worker-state)', () => {
    const migrated = signed({
      created_at: 100,
      tags: [['d', 'worker:migrated:legacy-1'], ['schema', 'bahia.state.worker.v1'], ['domain', 'worker'], ['t', 'worker-state'], ['legacy-kind', '32000']],
      content: { schema: 'bahia.state.worker.v1', pubkey: WORKER, name: 'migrated-worker', status: 'online', ...MESH_FIELDS }
    });
    expect(store.applyFipsMeshEvent(migrated)).toBe(true);
    expect(store.meshNodes[0]).toMatchObject({ pubkey: WORKER, name: 'migrated-worker', overlayAddress: 'fd00::42' });
  });

  it('describes a worker by its newest record when a migrated record replays after the canonical one', () => {
    expect(store.applyFipsMeshEvent(workerState({ created_at: 300, name: 'canonical-worker', mesh: MESH_FIELDS }))).toBe(true);
    const olderMigrated = signed({
      created_at: 100,
      tags: [['d', 'worker:migrated:legacy-2'], ['schema', 'bahia.state.worker.v1'], ['domain', 'worker'], ['t', 'worker-state'], ['legacy-kind', '32000']],
      content: { schema: 'bahia.state.worker.v1', pubkey: WORKER, name: 'migrated-worker', status: 'online', ...MESH_FIELDS }
    });
    expect(store.applyFipsMeshEvent(olderMigrated)).toBe(true);
    expect(store.meshNodes).toHaveLength(1);
    expect(store.meshNodes[0]).toMatchObject({ pubkey: WORKER, name: 'canonical-worker' });
  });

  it('reads the worker pubkey off the worker:state coordinate, never the raw d', () => {
    const bare = signed({
      created_at: 100,
      tags: [...envelope({ legacyKind: 32000, d: `worker:state:${WORKER}`, domain: 'worker' }), ['t', 'worker-state']],
      content: { name: 'no-pubkey-fields', status: 'online', ...MESH_FIELDS }
    });
    expect(store.applyFipsMeshEvent(bare)).toBe(true);
    expect(store.meshNodes[0]).toMatchObject({ pubkey: WORKER, name: 'no-pubkey-fields' });
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
