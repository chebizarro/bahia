import { describe, it, expect, beforeEach, vi } from 'vitest';
import { queryUntilEose } from '../../src/lib/nostr/pool-query.js';

const poolClientHarness = vi.hoisted(() => ({
  events: [],
  connectedRelays: ['ws://localhost:10547/relay'],
  instance: null,
  closedRelays: [],
  unsubscribe: vi.fn(),
  PoolBackedClient: vi.fn(function PoolBackedClient() {
    const client = {
      connect: vi.fn(async (relays) => {
        poolClientHarness.connectedRelays = [...relays];
        return { connected: relays.length, total: relays.length, failed: 0, connecting: 0, relays: relays.map((url) => ({ url, status: 'connected' })) };
      }),
      getConnectedRelays: vi.fn(() => [...poolClientHarness.connectedRelays]),
      subscribeOnRelays: vi.fn((relays, filters, handlers = {}) => {
        poolClientHarness.lastSubscription = { relays, filters, handlers };
        for (const event of poolClientHarness.events) {
          handlers.onEvent?.(event, relays[0]);
        }
        for (const closure of poolClientHarness.closedRelays) {
          handlers.onClosed?.(closure.reason, closure.relay, closure.meta);
        }
        for (const relay of relays) {
          if (!poolClientHarness.closedRelays.some((closure) => closure.relay === relay && closure.meta?.terminal !== false)) {
            handlers.onEose?.(relay);
          }
        }
        return poolClientHarness.unsubscribe;
      }),
      queryUntilEose: vi.fn((filters, options = {}) => queryUntilEose(client, filters, options)),
      disconnect: vi.fn()
    };
    poolClientHarness.instance = client;
    return client;
  })
}));

vi.mock('../../src/lib/nostr/client.js', async () => {
  const actual = await vi.importActual('../../src/lib/nostr/client.js');
  return {
    ...actual
  };
});

vi.mock('../../src/lib/nostr/pool-client.js', () => ({
  PoolBackedClient: poolClientHarness.PoolBackedClient
}));

const trustedPubkey = 'b'.repeat(64);
const otherPubkey = 'f'.repeat(64);

function nostrEvent({ id, kind, pubkey = trustedPubkey, created_at = 100, tags = [], content = {} }) {
  return {
    id,
    kind,
    pubkey,
    created_at,
    tags,
    content: typeof content === 'string' ? content : JSON.stringify(content)
  };
}

function systemDiscovery(overrides = {}) {
  return nostrEvent({
    id: overrides.id || 'discovery-1',
    kind: 11316,
    pubkey: overrides.pubkey || trustedPubkey,
    created_at: overrides.created_at || 100,
    tags: [['d', 'bahia-system-v1']],
    content: {
      schema: 'bahia.system-discovery.v1',
      control_plane: { version: 'bahia-controlplane-v1' },
      features: { relay_read_models: true, publish_enabled: true },
      registries: [],
      blossom: { enabled: false },
      runtime: { type: 'docker', environments: [] },
      oci: { enabled: false },
      ...(overrides.content || {})
    }
  });
}

function relaySet(d, relays, overrides = {}) {
  return nostrEvent({
    id: overrides.id || `relay-${d}`,
    kind: 30002,
    pubkey: overrides.pubkey || trustedPubkey,
    created_at: overrides.created_at || 100,
    tags: [['d', d], ...relays.map((url) => ['relay', url])],
    content: ''
  });
}

describe('Nostr system discovery store', () => {
  let store;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    window.__BAHIA_BOOTSTRAP__ = {
      schema: 'bahia.bootstrap.v1',
      relay_urls: ['http://localhost:10547/relay'],
      service_pubkeys: [trustedPubkey]
    };
    poolClientHarness.events = [
      systemDiscovery(),
      relaySet('bahia-browser-v1', ['http://localhost:10547/relay', 'wss://public.example']),
      relaySet('bahia-contextvm-v1', ['https://contextvm.example/relay'])
    ];
    poolClientHarness.connectedRelays = ['ws://localhost:10547/relay'];
    poolClientHarness.instance = null;
    poolClientHarness.lastSubscription = null;
    poolClientHarness.closedRelays = [];
    poolClientHarness.unsubscribe.mockClear();
    store = await import('../../src/lib/stores/discovery.svelte.js');
    store.resetDiscoveryStore();
  });

  it('reads bootstrap seed, subscribes until EOSE, and normalizes discovery into systemInfo shape', async () => {
    const info = await store.discoverSystemInfo();

    expect(poolClientHarness.instance.connect).toHaveBeenCalledWith(['ws://localhost:10547/relay'], { force: true });
    expect(poolClientHarness.instance.queryUntilEose).toHaveBeenCalledWith([
      { kinds: [11316, 30002], authors: [trustedPubkey] }
    ]);
    expect(poolClientHarness.instance.subscribeOnRelays).toHaveBeenCalledWith(
      ['ws://localhost:10547/relay'],
      [{ kinds: [11316, 30002], authors: [trustedPubkey] }],
      expect.objectContaining({
        onEvent: expect.any(Function),
        onEose: expect.any(Function),
        onClosed: expect.any(Function)
      })
    );
    expect(poolClientHarness.instance.disconnect).toHaveBeenCalledTimes(1);
    expect(info.features.relay_read_models).toBe(true);
    expect(info.nostr.service_pubkey).toBe(trustedPubkey);
    expect(info.nostr.browser_relays).toEqual(['ws://localhost:10547/relay', 'wss://public.example']);
    expect(info.nostr.contextvm_relays).toEqual(['wss://contextvm.example/relay']);
    expect(info.nostr.contextvm_relay_metadata).toEqual({
      source: 'bahia-contextvm-v1',
      degraded: false,
      reason: ''
    });
    expect(info._discovery.relay_sets['bahia-browser-v1']).toHaveLength(2);
    expect(info._discovery.relay_sets['bahia-contextvm-v1']).toEqual(['wss://contextvm.example/relay']);
  });

  it('falls back to browser relays with degraded metadata when the ContextVM relay set is absent', async () => {
    poolClientHarness.events = [
      systemDiscovery(),
      relaySet('bahia-browser-v1', ['http://localhost:10547/relay', 'wss://public.example'])
    ];

    const info = await store.discoverSystemInfo({ force: true });

    expect(info.nostr.contextvm_relays).toEqual(['ws://localhost:10547/relay', 'wss://public.example']);
    expect(info.nostr.contextvm_relay_metadata).toEqual({
      source: 'bahia-browser-v1',
      degraded: true,
      reason: 'missing_contextvm_relay_set'
    });
    expect(info.features.encrypted_nostr_requests).toBeUndefined();
  });

  it('ignores untrusted ContextVM relay sets and reports browser-relay fallback metadata', async () => {
    poolClientHarness.events = [
      systemDiscovery(),
      relaySet('bahia-browser-v1', ['wss://public.example']),
      relaySet('bahia-contextvm-v1', ['wss://untrusted-contextvm.example'], { pubkey: otherPubkey })
    ];

    const info = await store.discoverSystemInfo({ force: true });

    expect(info.nostr.contextvm_relays).toEqual(['wss://public.example']);
    expect(info.nostr.contextvm_relay_metadata.degraded).toBe(true);
    expect(info._discovery.relay_sets['bahia-contextvm-v1']).toBeUndefined();
  });

  it('fails closed when an auth-required CLOSED arrives before EOSE', async () => {
    poolClientHarness.closedRelays = [{ relay: 'ws://localhost:10547/relay', reason: 'auth-required: restricted discovery', meta: { terminal: true, source: 'auth', authRequired: true } }];

    await expect(store.discoverSystemInfo({ force: true })).rejects.toThrow('All Nostr query relays require AUTH');
    expect(poolClientHarness.instance.subscribeOnRelays).toHaveBeenCalledWith(
      ['ws://localhost:10547/relay'],
      [{ kinds: [11316, 30002], authors: [trustedPubkey] }],
      expect.objectContaining({
        onEvent: expect.any(Function),
        onEose: expect.any(Function),
        onClosed: expect.any(Function)
      })
    );
  });

  it('fails closed when the bootstrap seed is absent', async () => {
    delete window.__BAHIA_BOOTSTRAP__;

    await expect(store.discoverSystemInfo({ force: true })).rejects.toThrow('No relay URLs configured');
    expect(poolClientHarness.instance).toBeNull();
  });

  it('fails closed when EOSE completes without a trusted system discovery event', async () => {
    poolClientHarness.events = [
      systemDiscovery({ pubkey: otherPubkey }),
      relaySet('bahia-browser-v1', ['wss://relay.example'])
    ];

    await expect(store.discoverSystemInfo({ force: true })).rejects.toThrow('No trusted Bahia system discovery event received before EOSE');
  });

  it('fails closed when EOSE completes without a browser relay set', async () => {
    poolClientHarness.events = [systemDiscovery()];

    await expect(store.discoverSystemInfo({ force: true })).rejects.toThrow('No trusted Bahia browser relay set received before EOSE');
  });

  it('applies latest-wins replaceable semantics for discovery and relay sets', () => {
    const normalized = store.normalizeDiscoveryEvents([
      systemDiscovery({ id: 'old', created_at: 1, content: { features: { relay_read_models: false } } }),
      systemDiscovery({ id: 'new', created_at: 2, content: { features: { relay_read_models: true } } }),
      relaySet('bahia-browser-v1', ['wss://old.example'], { id: 'old-relay', created_at: 1 }),
      relaySet('bahia-browser-v1', ['wss://new.example'], { id: 'new-relay', created_at: 2 }),
      relaySet('bahia-contextvm-v1', ['wss://old-contextvm.example'], { id: 'old-contextvm-relay', created_at: 1 }),
      relaySet('bahia-contextvm-v1', ['wss://new-contextvm.example'], { id: 'new-contextvm-relay', created_at: 2 })
    ], [trustedPubkey]);

    expect(normalized.features.relay_read_models).toBe(true);
    expect(normalized.nostr.browser_relays).toEqual(['wss://new.example']);
    expect(normalized.nostr.contextvm_relays).toEqual(['wss://new-contextvm.example']);
  });
});
