import { describe, it, expect, beforeEach, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const canonicalDiscoveryFixture = JSON.parse(
  readFileSync(resolve(process.cwd(), '../test/fixtures/system_discovery_sidecar_first.json'), 'utf8')
);

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
      },
      get value() {
        return value;
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

describe('controlplane store', () => {
  let store;
  let KINDS;
  let subscriptionHandlers;

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
    nostrMock.subscribe.mockImplementation((_filters, handlers) => {
      subscriptionHandlers.push(handlers);
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

    const nostrClient = await import('../../src/lib/nostr/client.js');
    KINDS = nostrClient.KINDS;
    store = await import('../../src/lib/stores/controlplane.svelte.js');
    store.resetControlplaneStore();
  });

  it('resolves browser relay discovery from Nostr discovery', () => {
    expect(store.resolveBrowserRelays({
      nostr: {
        browser_relays: ['wss://relay.example'],
        sidecar_url: 'http://localhost:10547/relay'
      }
    })).toEqual(['wss://relay.example', 'ws://localhost:10547/relay']);
  });

  it('does not treat raw nostr.relays as approved browser bootstrap input', () => {
    expect(store.resolveBrowserRelays({
      nostr: {
        relays: ['wss://backend.example']
      }
    })).toEqual([]);
  });

  it('bootstraps from the canonical Nostr discovery fixture used by other consumers', async () => {
    systemInfoMock.loadSystemInfo.mockResolvedValueOnce(structuredClone(canonicalDiscoveryFixture));

    const result = await store.bootstrapControlplane();

    expect(result.ok).toBe(true);
    expect(store.controlplaneConnection.relays).toEqual(['wss://public.example', 'ws://localhost:3000/relay']);
    expect(nostrMock.setRelays).toHaveBeenCalledWith(['wss://public.example', 'ws://localhost:3000/relay'], false);
    expect(store.controlplaneConnection.servicePubkey).toBe('b'.repeat(64));
  });

  it('applies replaceable latest-wins dedupe and tombstones', () => {
    const older = event({
      id: 'svc-old',
      kind: KINDS.BAHIA_SERVICE_REGISTRY,
      created_at: 100,
      tags: [['d', 'svc-1'], ['deleted', 'false']],
      content: { id: 'svc-1', name: 'Old Service', deleted: false }
    });
    const stale = event({
      id: 'svc-stale',
      kind: KINDS.BAHIA_SERVICE_REGISTRY,
      created_at: 90,
      tags: [['d', 'svc-1'], ['deleted', 'false']],
      content: { id: 'svc-1', name: 'Stale Service', deleted: false }
    });
    const newer = event({
      id: 'svc-new',
      kind: KINDS.BAHIA_SERVICE_REGISTRY,
      created_at: 120,
      tags: [['d', 'svc-1'], ['deleted', 'false']],
      content: { id: 'svc-1', name: 'New Service', deleted: false }
    });
    const tombstone = event({
      id: 'svc-delete',
      kind: KINDS.BAHIA_SERVICE_REGISTRY,
      created_at: 130,
      tags: [['d', 'svc-1'], ['deleted', 'true']],
      content: { id: 'svc-1', deleted: true }
    });
    const lateOlderReplay = event({
      id: 'svc-late-replay',
      kind: KINDS.BAHIA_SERVICE_REGISTRY,
      created_at: 120,
      tags: [['d', 'svc-1'], ['deleted', 'false']],
      content: { id: 'svc-1', name: 'Late Replay', deleted: false }
    });

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
        kind: KINDS.BAHIA_SERVICE_REGISTRY,
        pubkey: 'b'.repeat(64),
        tags: [['d', 'svc-1'], ['deleted', 'false']],
        content: { id: 'svc-1', name: 'API', deleted: false }
      }),
      event({
        id: 'env-1-event',
        kind: KINDS.BAHIA_ENVIRONMENT_REGISTRY,
        pubkey: 'b'.repeat(64),
        tags: [['d', 'env-1'], ['deleted', 'false']],
        content: { id: 'env-1', name: 'Prod', deleted: false }
      }),
      event({
        id: 'state-1-event',
        kind: KINDS.BAHIA_SERVICE_STATE,
        pubkey: 'b'.repeat(64),
        tags: [['d', 'svc-1:env-1'], ['service', 'svc-1'], ['environment', 'env-1'], ['deleted', 'false']],
        content: { service_id: 'svc-1', environment_id: 'env-1', drift_status: 'in_sync', deleted: false }
      }),
      event({
        id: 'worker-1-event',
        kind: KINDS.LOOM_WORKER_AD,
        pubkey: 'c'.repeat(64),
        content: { name: 'Worker 1', description: 'test worker' }
      })
    ];

    const result = await store.bootstrapControlplane();

    expect(result.ok).toBe(true);
    expect(systemInfoMock.loadSystemInfo).toHaveBeenCalledTimes(1);
    expect(nostrMock.setRelays).toHaveBeenCalledWith(['ws://localhost:10547/relay'], false);
    expect(nostrMock.connect).toHaveBeenCalledWith(['ws://localhost:10547/relay'], { force: true });
    expect(nostrMock.queryUntilEose).not.toHaveBeenCalled();
    expect(nostrMock.subscribe).toHaveBeenCalledWith(
      expect.arrayContaining([
        expect.objectContaining({ kinds: expect.arrayContaining([31961, 31962, 31963, 31964, 31965]), authors: ['b'.repeat(64)], limit: 1000 }),
        expect.objectContaining({ kinds: [10100], limit: 1000 }),
        expect.objectContaining({ kinds: expect.arrayContaining([6961, 6962, 6973, 7961, 7971, 7972, 7973]), authors: ['b'.repeat(64)], limit: 100 })
      ]),
      expect.objectContaining({ onEvent: expect.any(Function), onEose: expect.any(Function), onClosed: expect.any(Function) })
    );
    expect(store.controlplaneConnection.ready).toBe(true);
    expect(store.controlplaneConnection.bootstrapComplete).toBe(false);
    expect(store.controlplaneConnection.status).toBe('syncing');
    expect(store.services).toHaveLength(0);

    for (const relayEvent of bootstrapEvents) {
      subscriptionHandlers[0].onEvent(relayEvent);
    }

    expect(store.controlplaneConnection.bootstrapComplete).toBe(false);
    expect(store.controlplaneConnection.status).toBe('syncing');
    expect(store.services).toHaveLength(1);
    expect(store.environments).toHaveLength(1);
    expect(store.states).toHaveLength(1);
    expect(store.workers).toHaveLength(1);

    subscriptionHandlers[0].onEose('ws://localhost:10547/relay');

    expect(store.controlplaneConnection.bootstrapComplete).toBe(true);
    expect(store.controlplaneConnection.status).toBe('live');
  });

  it('requires EOSE from every connected bootstrap relay before marking live', async () => {
    systemInfoMock.loadSystemInfo.mockResolvedValueOnce({
      nostr: {
        browser_relays: ['wss://relay-one.example', 'wss://relay-two.example'],
        service_pubkey: 'b'.repeat(64)
      },
      features: {
        relay_read_models: true,
        legacy_sse: false
      }
    });

    const result = await store.bootstrapControlplane();

    expect(result.ok).toBe(true);
    expect(store.controlplaneConnection.status).toBe('syncing');

    subscriptionHandlers[0].onEose('wss://relay-one.example');
    expect(store.controlplaneConnection.bootstrapComplete).toBe(false);
    expect(store.controlplaneConnection.status).toBe('syncing');

    subscriptionHandlers[0].onEose('wss://relay-two.example');
    expect(store.controlplaneConnection.bootstrapComplete).toBe(true);
    expect(store.controlplaneConnection.status).toBe('live');
  });

  it('fails closed when connected relay URLs are unavailable for EOSE tracking', async () => {
    nostrMock.connect.mockImplementation(async (relays = []) => {
      nostrMock.connected.set(true);
      return {
        total: relays.length,
        connected: 1,
        failed: 0,
        connecting: 0,
        relays: []
      };
    });

    const result = await store.bootstrapControlplane();

    expect(result.ok).toBe(false);
    expect(result.reason).toBe('Unable to determine connected relay URLs for bootstrap EOSE tracking');
    expect(store.controlplaneConnection.status).toBe('error');
    expect(nostrMock.subscribe).not.toHaveBeenCalled();
  });

  it('ignores stale EOSE from a pre-reconnect syncing subscription', async () => {
    await store.bootstrapControlplane();
    const staleHandlers = subscriptionHandlers[0];

    nostrMock.connected.set(false);
    expect(store.controlplaneConnection.status).toBe('disconnected');

    staleHandlers.onEose('ws://localhost:10547/relay');
    expect(store.controlplaneConnection.bootstrapComplete).toBe(false);
    expect(store.controlplaneConnection.status).toBe('disconnected');

    nostrMock.connected.set(true);
    expect(subscriptionHandlers).toHaveLength(2);
    expect(store.controlplaneConnection.status).toBe('syncing');

    staleHandlers.onEose('ws://localhost:10547/relay');
    expect(store.controlplaneConnection.bootstrapComplete).toBe(false);

    subscriptionHandlers[1].onEose('ws://localhost:10547/relay');
    expect(store.controlplaneConnection.bootstrapComplete).toBe(true);
    expect(store.controlplaneConnection.status).toBe('live');
  });

  it('applies LLM route and route-state read models from relay events', async () => {
    await store.bootstrapControlplane();

    expect(store.applyControlplaneEvent(event({
      id: 'llm-route-1-event',
      kind: KINDS.BAHIA_LLM_ROUTE_REGISTRY,
      pubkey: 'b'.repeat(64),
      tags: [['d', 'route-1'], ['route', 'route-1'], ['model', 'chat-public'], ['deleted', 'false']],
      content: { id: 'route-1', name: 'chat', public_model: 'chat-public', deleted: false }
    }))).toBe(true);
    expect(store.applyControlplaneEvent(event({
      id: 'llm-state-1-event',
      kind: KINDS.BAHIA_LLM_ROUTE_STATE,
      pubkey: 'b'.repeat(64),
      tags: [['d', 'route-1:env-1'], ['route', 'route-1'], ['environment', 'env-1'], ['deleted', 'false']],
      content: { route_id: 'route-1', environment_id: 'env-1', gateway_status: 'synced', deleted: false }
    }))).toBe(true);

    expect(store.llmRoutes).toHaveLength(1);
    expect(store.llmRoutes[0]).toMatchObject({ id: 'route-1', route_id: 'route-1', name: 'chat' });
    expect(store.llmRouteStates).toHaveLength(1);
    expect(store.llmRouteStates[0]).toMatchObject({ id: 'route-1:env-1', route_id: 'route-1', environment_id: 'env-1', gateway_status: 'synced' });
  });

  it('applies worker state and eligibility read models from relay events', async () => {
    await store.bootstrapControlplane();
    const workerPubkey = 'c'.repeat(64);

    expect(store.applyControlplaneEvent(event({
      id: 'worker-state-1-event',
      kind: KINDS.BAHIA_WORKER_STATE,
      pubkey: 'b'.repeat(64),
      tags: [['d', workerPubkey], ['worker', workerPubkey], ['deleted', 'false']],
      content: { worker_pubkey: workerPubkey, name: 'Worker 1', scheduling_state: 'cordoned', labels: { role: 'inference' }, deleted: false }
    }))).toBe(true);
    expect(store.applyControlplaneEvent(event({
      id: 'worker-preview-1-event',
      kind: KINDS.BAHIA_WORKER_ELIGIBILITY_PREVIEW,
      pubkey: 'b'.repeat(64),
      tags: [['d', 'preview-1'], ['deleted', 'false']],
      content: {
        preview_id: 'preview-1',
        workload_type: 'ml_inference',
        eligible_workers: [{ worker_pubkey: workerPubkey, worker_name: 'Worker 1', eligible: true, score: 10, reason: 'eligible' }],
        rejected_workers: []
      }
    }))).toBe(true);

    expect(store.workers).toHaveLength(1);
    expect(store.workers[0]).toMatchObject({ pubkey: workerPubkey, scheduling_state: 'cordoned', labels: { role: 'inference' } });
    expect(store.workerEligibilityPreviews).toHaveLength(1);
    expect(store.workerEligibilityPreviews[0]).toMatchObject({ preview_id: 'preview-1', workload_type: 'ml_inference' });
  });

  it('bridges live subscription events into relay-backed state', async () => {
    let liveHandlers;
    nostrMock.subscribe.mockImplementation((_filters, handlers) => {
      liveHandlers = handlers;
      return vi.fn();
    });

    await store.bootstrapControlplane();
    liveHandlers.onEvent(event({
      id: 'audit-1',
      kind: KINDS.BAHIA_LLM_DEPLOYMENT_STATUS,
      pubkey: 'b'.repeat(64),
      created_at: 200,
      tags: [['route', 'route-1'], ['environment', 'env-1'], ['intent', 'intent-1'], ['status', 'processing']],
      content: { status: 'processing', step: 'provisioning', route_id: 'route-1', environment_id: 'env-1' }
    }));

    expect(store.events).toHaveLength(1);
    expect(store.events[0]).toMatchObject({ id: 'audit-1', type: 'llm_deployment.status', entity_id: 'route-1' });
  });

  it('ignores canonical Bahia events not authored by the advertised service pubkey', async () => {
    await store.bootstrapControlplane();

    expect(store.applyControlplaneEvent(event({
      id: 'spoofed-service',
      kind: KINDS.BAHIA_SERVICE_REGISTRY,
      pubkey: 'f'.repeat(64),
      tags: [['d', 'svc-spoof'], ['deleted', 'false']],
      content: { id: 'svc-spoof', name: 'Spoofed Service', deleted: false }
    }))).toBe(false);

    expect(store.services).toEqual([]);
  });

  it('ignores spoofed non-Bahia LLM route, state, and activity events', async () => {
    await store.bootstrapControlplane();

    expect(store.applyControlplaneEvent(event({
      id: 'spoofed-llm-route',
      kind: KINDS.BAHIA_LLM_ROUTE_REGISTRY,
      pubkey: 'f'.repeat(64),
      tags: [['d', 'route-spoof'], ['route', 'route-spoof'], ['deleted', 'false']],
      content: { id: 'route-spoof', name: 'Spoofed Route', deleted: false }
    }))).toBe(false);

    expect(store.applyControlplaneEvent(event({
      id: 'spoofed-llm-state',
      kind: KINDS.BAHIA_LLM_ROUTE_STATE,
      pubkey: 'f'.repeat(64),
      tags: [['d', 'route-spoof:env-spoof'], ['route', 'route-spoof'], ['environment', 'env-spoof'], ['deleted', 'false']],
      content: { route_id: 'route-spoof', environment_id: 'env-spoof', gateway_status: 'synced', deleted: false }
    }))).toBe(false);

    expect(store.applyControlplaneEvent(event({
      id: 'spoofed-llm-status',
      kind: KINDS.BAHIA_LLM_DEPLOYMENT_STATUS,
      pubkey: 'f'.repeat(64),
      tags: [['route', 'route-spoof'], ['environment', 'env-spoof'], ['status', 'processing']],
      content: { status: 'processing', route_id: 'route-spoof', environment_id: 'env-spoof' }
    }))).toBe(false);

    expect(store.llmRoutes).toEqual([]);
    expect(store.llmRouteStates).toEqual([]);
    expect(store.events).toEqual([]);
  });

  it('tracks reconnect status from the shared Nostr client connection store', async () => {
    await store.bootstrapControlplane();
    expect(store.controlplaneConnection.status).toBe('syncing');

    subscriptionHandlers[0].onEose('ws://localhost:10547/relay');
    expect(store.controlplaneConnection.status).toBe('live');

    nostrMock.connected.set(false);
    expect(store.controlplaneConnection.status).toBe('disconnected');

    nostrMock.connected.set(true);
    expect(store.controlplaneConnection.status).toBe('live');
    expect(store.controlplaneConnection.reconnects).toBe(1);
  });

  it('marks bootstrap as an error when relay bootstrap fails', async () => {
    nostrMock.connect.mockImplementation(async () => {
      nostrMock.connected.set(false);
    });

    const result = await store.bootstrapControlplane();

    expect(result.ok).toBe(false);
    expect(result.reason).toBe('Unable to connect to any advertised browser relay');
    expect(store.controlplaneConnection.status).toBe('error');
  });

  it('fails closed when relay read models are not advertised', async () => {
    systemInfoMock.loadSystemInfo.mockResolvedValueOnce({
      nostr: {
        browser_relays: ['http://localhost:10547/relay'],
        service_pubkey: 'b'.repeat(64)
      },
      features: {
        relay_read_models: false,
        legacy_sse: false
      }
    });

    const result = await store.bootstrapControlplane();

    expect(result.ok).toBe(false);
    expect(result.reason).toBe('Relay read models are not advertised by Nostr system discovery');
    expect(store.controlplaneConnection.status).toBe('error');
    expect(nostrMock.setRelays).not.toHaveBeenCalled();
    expect(nostrMock.connect).not.toHaveBeenCalled();
  });

  it('fails closed when no browser bootstrap relays are advertised', async () => {
    systemInfoMock.loadSystemInfo.mockResolvedValueOnce({
      nostr: {
        service_pubkey: 'b'.repeat(64)
      },
      features: {
        relay_read_models: true,
        legacy_sse: false
      }
    });

    const result = await store.bootstrapControlplane();

    expect(result.ok).toBe(false);
    expect(result.reason).toBe('No browser Nostr relays advertised by Nostr system discovery');
    expect(store.controlplaneConnection.status).toBe('error');
    expect(nostrMock.setRelays).not.toHaveBeenCalled();
    expect(nostrMock.connect).not.toHaveBeenCalled();
  });
});
