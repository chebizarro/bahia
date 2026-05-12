import { describe, it, expect, beforeEach, vi } from 'vitest';

const nostrMock = vi.hoisted(() => ({
  setRelays: vi.fn(),
  connect: vi.fn(),
  queryUntilEose: vi.fn()
}));

vi.mock('../../src/lib/nostr/client.js', async () => {
  const actual = await vi.importActual('../../src/lib/nostr/client.js');
  return {
    ...actual,
    nostr: nostrMock
  };
});

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
    kind: 31974,
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
    nostrMock.connect.mockResolvedValue(undefined);
    nostrMock.queryUntilEose.mockResolvedValue([
      systemDiscovery(),
      relaySet('bahia-browser-v1', ['http://localhost:10547/relay', 'wss://public.example']),
      relaySet('bahia-requests-v1', ['wss://requests.example'])
    ]);
    store = await import('../../src/lib/stores/discovery.svelte.js');
    store.resetDiscoveryStore();
  });

  it('reads bootstrap seed, subscribes until EOSE, and normalizes discovery into systemInfo shape', async () => {
    const info = await store.discoverSystemInfo();

    expect(nostrMock.setRelays).toHaveBeenCalledWith(['ws://localhost:10547/relay'], false);
    expect(nostrMock.connect).toHaveBeenCalledWith(['ws://localhost:10547/relay']);
    expect(nostrMock.queryUntilEose).toHaveBeenCalledWith([
      { kinds: [31974, 30002], authors: [trustedPubkey] }
    ]);
    expect(info.features.relay_read_models).toBe(true);
    expect(info.nostr.service_pubkey).toBe(trustedPubkey);
    expect(info.nostr.browser_relays).toEqual(['ws://localhost:10547/relay', 'wss://public.example']);
    expect(info.nostr.browser_encrypted_request_relays).toEqual(['wss://requests.example']);
    expect(info._discovery.relay_sets['bahia-browser-v1']).toHaveLength(2);
  });

  it('fails closed when the bootstrap seed is absent', async () => {
    delete window.__BAHIA_BOOTSTRAP__;

    await expect(store.discoverSystemInfo({ force: true })).rejects.toThrow('Missing Bahia bootstrap relay seed');
    expect(nostrMock.queryUntilEose).not.toHaveBeenCalled();
  });

  it('fails closed when EOSE completes without a trusted system discovery event', async () => {
    nostrMock.queryUntilEose.mockResolvedValue([
      systemDiscovery({ pubkey: otherPubkey }),
      relaySet('bahia-browser-v1', ['wss://relay.example'])
    ]);

    await expect(store.discoverSystemInfo({ force: true })).rejects.toThrow('No trusted Bahia system discovery event received before EOSE');
  });

  it('fails closed when EOSE completes without a browser relay set', async () => {
    nostrMock.queryUntilEose.mockResolvedValue([systemDiscovery()]);

    await expect(store.discoverSystemInfo({ force: true })).rejects.toThrow('No trusted Bahia browser relay set received before EOSE');
  });

  it('applies latest-wins replaceable semantics for discovery and relay sets', () => {
    const normalized = store.normalizeDiscoveryEvents([
      systemDiscovery({ id: 'old', created_at: 1, content: { features: { relay_read_models: false } } }),
      systemDiscovery({ id: 'new', created_at: 2, content: { features: { relay_read_models: true } } }),
      relaySet('bahia-browser-v1', ['wss://old.example'], { id: 'old-relay', created_at: 1 }),
      relaySet('bahia-browser-v1', ['wss://new.example'], { id: 'new-relay', created_at: 2 })
    ], [trustedPubkey]);

    expect(normalized.features.relay_read_models).toBe(true);
    expect(normalized.nostr.browser_relays).toEqual(['wss://new.example']);
  });
});
