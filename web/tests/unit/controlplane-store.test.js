import { describe, it, expect, beforeEach, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const canonicalDiscoveryFixture = JSON.parse(
  readFileSync(resolve(process.cwd(), '../test/fixtures/system_discovery_sidecar_first.json'), 'utf8')
);

const systemInfoMock = vi.hoisted(() => ({
  loadSystemInfo: vi.fn()
}));

const discoveryMock = vi.hoisted(() => ({
  getBootstrapSeed: vi.fn()
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
    subscribeWithRecovery: vi.fn()
  };
});

vi.mock('../../src/lib/stores/system.svelte.js', () => ({
  loadSystemInfo: systemInfoMock.loadSystemInfo
}));

vi.mock('../../src/lib/stores/discovery.svelte.js', () => ({
  getBootstrapSeed: discoveryMock.getBootstrapSeed
}));

vi.mock('../../src/lib/nostr/client.js', async () => {
  const actual = await vi.importActual('../../src/lib/nostr/client.js');
  return {
    ...actual,
    nostr: nostrMock
  };
});

function event({ id, kind, pubkey = 'a'.repeat(64), created_at = 100, tags = [], content = {} }) {
  return {
    id,
    kind,
    pubkey,
    created_at,
    tags,
    content: typeof content === 'string' ? content : JSON.stringify(content)
  };
}

function canonicalTags(domain, schema, tags = []) {
  return [['domain', domain], ['schema', schema], ...tags];
}

describe('controlplane store', () => {
  let store;
  let KINDS;
  let CAS_STATE_KIND;
  let NIP38_STATUS;
  let BAHIA_STATE_SCHEMAS;
  let subscriptionHandlers;
  let subscriptionRegistered;
  let resolveSubscriptionRegistered;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    nostrMock.connected.set(false);
    nostrMock.connect.mockImplementation(async (relays = []) => {
      nostrMock.connected.set(true);
      return {
        total: relays.length,
        connected: relays.length,
        failed: 0,
        connecting: 0,
        relays: relays.map((url) => ({ url, status: 'connected' }))
      };
    });
    nostrMock.queryUntilEose.mockResolvedValue([]);
    subscriptionHandlers = [];
    subscriptionRegistered = new Promise((resolve) => {
      resolveSubscriptionRegistered = resolve;
    });
    nostrMock.subscribeWithRecovery.mockImplementation((_filters, handlers) => {
      subscriptionHandlers.push(handlers);
      resolveSubscriptionRegistered(handlers);
      return vi.fn();
    });

    systemInfoMock.loadSystemInfo.mockResolvedValue({
      nostr: {
        browser_relays: ['http://localhost:10547/relay'],
        service_pubkey: 'b'.repeat(64)
      },
      features: {
        relay_read_models: true,
        legacy_sse: false
      }
    });
    discoveryMock.getBootstrapSeed.mockReturnValue({
      relay_urls: ['http://localhost:10547/relay'],
      service_pubkeys: ['b'.repeat(64)]
    });

    const nostrClient = await import('../../src/lib/nostr/client.js');
    KINDS = nostrClient.KINDS;
    CAS_STATE_KIND = nostrClient.CASCADIA_CONTROLPLANE_STATE;
    NIP38_STATUS = nostrClient.NIP38_STATUS;
    BAHIA_STATE_SCHEMAS = nostrClient.BAHIA_STATE_SCHEMAS;
    store = await import('../../src/lib/stores/controlplane.svelte.js');
    store.resetControlplaneStore();
  });

  async function startBootstrapAndWaitForSubscription(options) {
    const bootstrap = store.bootstrapControlplane(options);
    await subscriptionRegistered;
    return { bootstrap };
  }

  function completeBootstrapEose(relays = ['ws://localhost:10547/relay'], handlers = subscriptionHandlers[0]) {
    for (const relay of relays) handlers.onEose(relay);
  }

  async function bootstrapWithEose(options, relays) {
    const { bootstrap } = await startBootstrapAndWaitForSubscription(options);
    completeBootstrapEose(relays);
    return bootstrap;
  }

  it('resolves browser relay discovery from Nostr discovery', () => {
    expect(store.resolveBrowserRelays({
      nostr: {
        browser_relays: ['wss://relay.example'],
        sidecar_url: 'http://localhost:10547/relay'
      }
    })).toEqual(['wss://relay.example', 'ws://localhost:10547/relay']);
  });

  it('does not treat raw nostr.relays as approved browser bootstrap input', () => {
    expect(store.resolveBrowserRelays({ nostr: { relays: ['wss://backend.example'] } })).toEqual([]);
  });

  it('bootstraps from deployment configuration without waiting for discovery', async () => {
    systemInfoMock.loadSystemInfo.mockResolvedValueOnce(structuredClone(canonicalDiscoveryFixture));
    discoveryMock.getBootstrapSeed.mockReturnValueOnce({
      relay_urls: ['wss://public.example', 'http://localhost:3000/relay'],
      service_pubkeys: ['b'.repeat(64)]
    });

    const result = await bootstrapWithEose(undefined, ['wss://public.example', 'ws://localhost:3000/relay']);

    expect(result.ok).toBe(true);
    expect(store.controlplaneConnection.relays).toEqual(['wss://public.example', 'ws://localhost:3000/relay']);
    expect(nostrMock.setRelays).toHaveBeenCalledWith(['wss://public.example', 'ws://localhost:3000/relay'], false);
    expect(store.controlplaneConnection.servicePubkey).toBe('b'.repeat(64));
  });

  it('applies canonical replaceable latest-wins dedupe and tombstones by schema', () => {
    const serviceTags = (extra = []) => canonicalTags('service', BAHIA_STATE_SCHEMAS.SERVICE_REGISTRY, [['d', 'svc-1'], ...extra]);
    const older = event({ id: 'svc-old', kind: CAS_STATE_KIND, created_at: 100, tags: serviceTags([['deleted', 'false']]), content: { id: 'svc-1', name: 'Old Service', deleted: false } });
    const stale = event({ id: 'svc-stale', kind: CAS_STATE_KIND, created_at: 90, tags: serviceTags([['deleted', 'false']]), content: { id: 'svc-1', name: 'Stale Service', deleted: false } });
    const newer = event({ id: 'svc-new', kind: CAS_STATE_KIND, created_at: 120, tags: serviceTags([['deleted', 'false']]), content: { id: 'svc-1', name: 'New Service', deleted: false } });
    const tombstone = event({ id: 'svc-delete', kind: CAS_STATE_KIND, created_at: 130, tags: serviceTags([['deleted', 'true']]), content: { id: 'svc-1', deleted: true } });
    const lateOlderReplay = event({ id: 'svc-late-replay', kind: CAS_STATE_KIND, created_at: 120, tags: serviceTags([['deleted', 'false']]), content: { id: 'svc-1', name: 'Late Replay', deleted: false } });

    expect(store.applyControlplaneEvent(older)).toBe(true);
    expect(store.services[0].name).toBe('Old Service');
    expect(store.applyControlplaneEvent(stale)).toBe(false);
    expect(store.services[0].name).toBe('Old Service');
    expect(store.applyControlplaneEvent(newer)).toBe(true);
    expect(store.services[0].name).toBe('New Service');
    expect(store.applyControlplaneEvent(tombstone)).toBe(true);
    expect(store.services).toEqual([]);
    expect(store.applyControlplaneEvent(lateOlderReplay)).toBe(false);
    expect(store.services).toEqual([]);
  });

  it('streams bootstrap events immediately and marks live only after EOSE', async () => {
    const bootstrapEvents = [
      event({
        id: 'svc-1-event',
        kind: CAS_STATE_KIND,
        pubkey: 'b'.repeat(64),
        tags: canonicalTags('service', BAHIA_STATE_SCHEMAS.SERVICE_REGISTRY, [['d', 'svc-1'], ['deleted', 'false']]),
        content: { id: 'svc-1', name: 'API', deleted: false }
      }),
      event({
        id: 'env-1-event',
        kind: CAS_STATE_KIND,
        pubkey: 'b'.repeat(64),
        tags: canonicalTags('environment', BAHIA_STATE_SCHEMAS.ENVIRONMENT_REGISTRY, [['d', 'env-1'], ['deleted', 'false']]),
        content: { id: 'env-1', name: 'Prod', deleted: false }
      }),
      event({
        id: 'state-1-event',
        kind: CAS_STATE_KIND,
        pubkey: 'b'.repeat(64),
        tags: canonicalTags('service', BAHIA_STATE_SCHEMAS.SERVICE_STATE, [['d', 'svc-1:env-1'], ['service', 'svc-1'], ['environment', 'env-1'], ['deleted', 'false']]),
        content: { service_id: 'svc-1', environment_id: 'env-1', drift_status: 'in_sync', deleted: false }
      }),
      event({ id: 'worker-1-event', kind: KINDS.LOOM_WORKER_AD, pubkey: 'c'.repeat(64), content: { name: 'Worker 1', description: 'test worker' } })
    ];

    const { bootstrap: resultPromise } = await startBootstrapAndWaitForSubscription();

    expect(store.controlplaneConnection.ready).toBe(true);
    expect(store.controlplaneConnection.bootstrapComplete).toBe(false);
    expect(store.controlplaneConnection.status).toBe('syncing');
    expect(store.services).toHaveLength(0);
    expect(systemInfoMock.loadSystemInfo).not.toHaveBeenCalled();
    expect(nostrMock.setRelays).toHaveBeenCalledWith(['ws://localhost:10547/relay'], false);
    expect(nostrMock.connect).toHaveBeenCalledWith(['ws://localhost:10547/relay'], { force: true });
    expect(nostrMock.queryUntilEose).not.toHaveBeenCalled();
    expect(nostrMock.subscribeWithRecovery).toHaveBeenCalledWith(
      expect.arrayContaining([
        expect.objectContaining({ kinds: expect.arrayContaining([CAS_STATE_KIND]), authors: ['b'.repeat(64)], limit: 1000 }),
        expect.objectContaining({ kinds: [10100], limit: 1000 }),
        expect.objectContaining({ kinds: expect.arrayContaining([30315, 4903, 30078]), authors: ['b'.repeat(64)], limit: 100 })
      ]),
      expect.objectContaining({ onEvent: expect.any(Function), onEose: expect.any(Function), onHealth: expect.any(Function), onClosed: expect.any(Function) })
    );
    for (const relayEvent of bootstrapEvents) subscriptionHandlers[0].onEvent(relayEvent);

    expect(store.controlplaneConnection.bootstrapComplete).toBe(false);
    expect(store.controlplaneConnection.status).toBe('syncing');
    expect(store.services).toHaveLength(1);
    expect(store.environments).toHaveLength(1);
    expect(store.states).toHaveLength(1);
    expect(store.workers).toHaveLength(1);

    subscriptionHandlers[0].onHealth({
      lastEoseAt: '2026-07-30T12:00:00.000Z',
      resubscribeAttempts: 2,
      lastClosedReason: 'rate-limited'
    });
    subscriptionHandlers[0].onEose('ws://localhost:10547/relay');
    const result = await resultPromise;

    expect(result.ok).toBe(true);
    expect(store.controlplaneConnection).toMatchObject({
      lastEoseAt: '2026-07-30T12:00:00.000Z',
      resubscribeAttempts: 2,
      lastClosedReason: 'rate-limited'
    });
    expect(store.controlplaneConnection.bootstrapComplete).toBe(true);
    expect(store.controlplaneConnection.status).toBe('live');
  });

  it('requires EOSE from every connected bootstrap relay before marking live', async () => {
    discoveryMock.getBootstrapSeed.mockReturnValueOnce({
      relay_urls: ['wss://relay-one.example', 'wss://relay-two.example'],
      service_pubkeys: ['b'.repeat(64)]
    });

    const { bootstrap: resultPromise } = await startBootstrapAndWaitForSubscription();

    expect(store.controlplaneConnection.ready).toBe(true);
    expect(store.controlplaneConnection.status).toBe('syncing');

    subscriptionHandlers[0].onEose('wss://relay-one.example');
    expect(store.controlplaneConnection.bootstrapComplete).toBe(false);

    subscriptionHandlers[0].onEose('wss://relay-two.example');
    const result = await resultPromise;
    expect(result.ok).toBe(true);
    expect(store.controlplaneConnection.bootstrapComplete).toBe(true);
    expect(store.controlplaneConnection.status).toBe('live');
  });

  it('fails closed when connected relay URLs are unavailable for EOSE tracking', async () => {
    nostrMock.connect.mockImplementation(async (relays = []) => {
      nostrMock.connected.set(true);
      return { total: relays.length, connected: 1, failed: 0, connecting: 0, relays: [] };
    });

    const result = await store.bootstrapControlplane();

    expect(result.ok).toBe(false);
    expect(result.reason).toBe('Unable to determine connected relay URLs for bootstrap EOSE tracking');
    expect(store.controlplaneConnection.status).toBe('error');
    expect(nostrMock.subscribeWithRecovery).not.toHaveBeenCalled();
  });

  it('applies LLM route, route-state, worker state, and eligibility read models from schema-routed relay events', async () => {
    await bootstrapWithEose();
    const workerPubkey = 'c'.repeat(64);

    expect(store.applyControlplaneEvent(event({
      id: 'llm-route-1-event',
      kind: CAS_STATE_KIND,
      pubkey: 'b'.repeat(64),
      tags: canonicalTags('llm', BAHIA_STATE_SCHEMAS.LLM_ROUTE_REGISTRY, [['d', 'route-1'], ['route', 'route-1'], ['model', 'chat-public'], ['deleted', 'false']]),
      content: { id: 'route-1', name: 'chat', public_model: 'chat-public', deleted: false }
    }))).toBe(true);
    expect(store.applyControlplaneEvent(event({
      id: 'llm-state-1-event',
      kind: CAS_STATE_KIND,
      pubkey: 'b'.repeat(64),
      tags: canonicalTags('llm', BAHIA_STATE_SCHEMAS.LLM_ROUTE_STATE, [['d', 'route-1:env-1'], ['route', 'route-1'], ['environment', 'env-1'], ['deleted', 'false']]),
      content: { route_id: 'route-1', environment_id: 'env-1', gateway_status: 'synced', deleted: false }
    }))).toBe(true);
    expect(store.applyControlplaneEvent(event({
      id: 'worker-state-1-event',
      kind: CAS_STATE_KIND,
      pubkey: 'b'.repeat(64),
      tags: canonicalTags('worker', BAHIA_STATE_SCHEMAS.WORKER_STATE, [['d', workerPubkey], ['worker', workerPubkey], ['deleted', 'false']]),
      content: { worker_pubkey: workerPubkey, name: 'Worker 1', scheduling_state: 'cordoned', labels: { role: 'inference' }, deleted: false }
    }))).toBe(true);
    expect(store.applyControlplaneEvent(event({
      id: 'worker-preview-1-event',
      kind: CAS_STATE_KIND,
      pubkey: 'b'.repeat(64),
      tags: canonicalTags('worker', BAHIA_STATE_SCHEMAS.WORKER_ELIGIBILITY_PREVIEW, [['d', 'preview-1'], ['deleted', 'false']]),
      content: { preview_id: 'preview-1', workload_type: 'ml_inference', eligible_workers: [{ worker_pubkey: workerPubkey }], rejected_workers: [] }
    }))).toBe(true);

    expect(store.llmRoutes[0]).toMatchObject({ id: 'route-1', route_id: 'route-1', name: 'chat' });
    expect(store.llmRouteStates[0]).toMatchObject({ id: 'route-1:env-1', route_id: 'route-1', environment_id: 'env-1', gateway_status: 'synced' });
    expect(store.workers[0]).toMatchObject({ pubkey: workerPubkey, scheduling_state: 'cordoned', labels: { role: 'inference' } });
    expect(store.workerEligibilityPreviews[0]).toMatchObject({ preview_id: 'preview-1', workload_type: 'ml_inference' });
  });

  it('bridges canonical status events into relay-backed activity state', async () => {
    let liveHandlers;
    nostrMock.subscribeWithRecovery.mockImplementation((_filters, handlers) => {
      liveHandlers = handlers;
      subscriptionHandlers.push(handlers);
      resolveSubscriptionRegistered(handlers);
      return vi.fn();
    });

    await bootstrapWithEose(undefined, ['ws://localhost:10547/relay']);
    liveHandlers.onEvent(event({
      id: 'audit-1',
      kind: NIP38_STATUS,
      pubkey: 'b'.repeat(64),
      created_at: 200,
      tags: [['domain', 'llm'], ['schema', 'bahia.status.llm.v1'], ['route', 'route-1'], ['environment', 'env-1'], ['intent', 'intent-1'], ['status', 'processing']],
      content: { status: 'processing', step: 'provisioning', route_id: 'route-1', environment_id: 'env-1' }
    }));

    expect(store.events).toHaveLength(1);
    expect(store.events[0]).toMatchObject({ id: 'audit-1', type: 'llm.status', entity_id: 'route-1' });
  });

  it('ignores canonical Bahia events not authored by the advertised service pubkey', async () => {
    await bootstrapWithEose();

    expect(store.applyControlplaneEvent(event({
      id: 'spoofed-service',
      kind: CAS_STATE_KIND,
      pubkey: 'f'.repeat(64),
      tags: canonicalTags('service', BAHIA_STATE_SCHEMAS.SERVICE_REGISTRY, [['d', 'svc-spoof'], ['deleted', 'false']]),
      content: { id: 'svc-spoof', name: 'Spoofed Service', deleted: false }
    }))).toBe(false);

    expect(store.services).toEqual([]);
  });

  it('does not gate configured relay read models on optional discovery metadata', async () => {
    systemInfoMock.loadSystemInfo.mockResolvedValueOnce({
      nostr: { browser_relays: ['http://localhost:10547/relay'], service_pubkey: 'b'.repeat(64) },
      features: { relay_read_models: false, legacy_sse: false }
    });

    const resultPromise = bootstrapWithEose();
    const result = await resultPromise;

    expect(result.ok).toBe(true);
    expect(store.controlplaneConnection.status).toBe('live');
    expect(nostrMock.connect).toHaveBeenCalled();
  });

  it('fails clearly when deployment config has no browser bootstrap relays', async () => {
    discoveryMock.getBootstrapSeed.mockReturnValueOnce({
      relay_urls: [],
      service_pubkeys: ['b'.repeat(64)]
    });

    const result = await store.bootstrapControlplane();

    expect(result.ok).toBe(false);
    expect(result.reason).toBe('No browser Nostr relays configured by deployment bootstrap');
    expect(store.controlplaneConnection.status).toBe('error');
    expect(nostrMock.setRelays).not.toHaveBeenCalled();
    expect(nostrMock.connect).not.toHaveBeenCalled();
  });

  it('fails clearly when deployment config has no trusted service pubkey', async () => {
    discoveryMock.getBootstrapSeed.mockReturnValueOnce({
      relay_urls: ['wss://relay.example'],
      service_pubkeys: []
    });

    const result = await store.bootstrapControlplane();

    expect(result.ok).toBe(false);
    expect(result.reason).toBe('No trusted Bahia service pubkey configured by deployment bootstrap');
    expect(nostrMock.connect).not.toHaveBeenCalled();
  });
});
