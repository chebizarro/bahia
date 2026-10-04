import { beforeEach, describe, expect, it, vi } from 'vitest';

const trustedPubkey = 'b'.repeat(64);
const otherPubkey = 'f'.repeat(64);
const bridge = vi.hoisted(() => ({ events: [], handlers: null, pool: { subscribe: vi.fn(() => ({ unsubscribe: vi.fn() })) }, boot: vi.fn(async () => undefined) }));
vi.mock('../../src/lib/nostr/boot.js', () => ({
  boot: bridge.boot,
  getEventStore: () => ({ query: () => [...bridge.events] }),
  getPool: () => bridge.pool
}));

const discovery = await import('../../src/lib/stores/discovery.svelte.js');
function event({ id, kind, pubkey = trustedPubkey, created_at = 100, tags = [], content = '' }) {
  return { id, kind, pubkey, created_at, tags, content: typeof content === 'string' ? content : JSON.stringify(content) };
}
function system(overrides = {}) {
  return event({ id: overrides.id || 'system-1', kind: 11316, pubkey: overrides.pubkey || trustedPubkey,
    created_at: overrides.created_at || 100, tags: [['d', 'bahia-system-v1']],
    content: { schema: 'bahia.system-discovery.v1', features: { relay_read_models: true }, nostr: {}, ...(overrides.content || {}) } });
}
function relaySet(d, relays, overrides = {}) {
  return event({ id: overrides.id || d, kind: 30002, pubkey: overrides.pubkey || trustedPubkey,
    created_at: overrides.created_at || 100, tags: [['d', d], ...relays.map(url => ['relay', url])] });
}

beforeEach(() => {
  vi.clearAllMocks();
  window.__BAHIA_BOOTSTRAP__ = { relay_urls: ['https://relay.example'], service_pubkeys: [trustedPubkey] };
  bridge.events = [system(), relaySet('bahia-browser-v1', ['https://relay.example']), relaySet('bahia-contextvm-v1', ['wss://contextvm.example'])];
  bridge.handlers = null;
  bridge.pool.subscribe.mockImplementation(({ onEvent, onEose, onClosed, ...request }) => {
    bridge.handlers = { onEvent, onEose, onClosed, request };
    return { unsubscribe: vi.fn() };
  });
  discovery.resetDiscoveryStore();
});

describe('Nostr system discovery store', () => {
  it('renders persisted signed discovery without waiting for network and keeps one REQ', async () => {
    const info = await discovery.discoverSystemInfo();
    expect(bridge.boot).toHaveBeenCalledOnce();
    expect(bridge.pool.subscribe).toHaveBeenCalledOnce();
    expect(bridge.handlers.request).toMatchObject({
      relays: ['wss://relay.example'],
      filters: [expect.objectContaining({ kinds: [11316, 30002], authors: [trustedPubkey] })]
    });
    expect(info.nostr.browser_relays).toEqual(['wss://relay.example']);
    expect(info.nostr.contextvm_relays).toEqual(['wss://contextvm.example']);
    expect(info.nostr.service_pubkey).toBe(trustedPubkey);
  });

  it('waits for EOSE when no persisted discovery exists', async () => {
    bridge.events = [];
    const pending = discovery.discoverSystemInfo();
    await vi.waitFor(() => expect(bridge.handlers).not.toBeNull());
    bridge.events.push(system(), relaySet('bahia-browser-v1', ['wss://late.example']));
    bridge.handlers.onEvent(bridge.events[0], 'wss://relay.example');
    bridge.handlers.onEose('wss://relay.example');
    await expect(pending).resolves.toMatchObject({ nostr: { browser_relays: ['wss://late.example'] } });
  });

  it('rejects a terminal closure before EOSE when there is no local snapshot', async () => {
    bridge.events = [];
    const pending = discovery.discoverSystemInfo();
    await vi.waitFor(() => expect(bridge.handlers).not.toBeNull());
    bridge.handlers.onClosed('auth-required: restricted', 'wss://relay.example');
    await expect(pending).rejects.toThrow('Discovery relays closed before EOSE');
  });

  it('keeps a persisted snapshot when the relay closes before EOSE', async () => {
    const info = await discovery.discoverSystemInfo();
    bridge.handlers.onClosed('unavailable', 'wss://relay.example');
    expect(discovery.discoveryState.info).toEqual(info);
  });

  it('fails closed without a deployment bootstrap seed', async () => {
    delete window.__BAHIA_BOOTSTRAP__;
    await expect(discovery.discoverSystemInfo()).rejects.toThrow('requires deployment bootstrap relay URLs');
    expect(bridge.pool.subscribe).not.toHaveBeenCalled();
  });

  it('ignores untrusted relay sets and reports degraded ContextVM fallback', () => {
    const result = discovery.normalizeDiscoveryEvents([
      system(), relaySet('bahia-browser-v1', ['https://browser.example']),
      relaySet('bahia-contextvm-v1', ['wss://evil.example'], { pubkey: otherPubkey })
    ], [trustedPubkey]);
    expect(result.nostr.contextvm_relays).toEqual(['wss://browser.example']);
    expect(result.nostr.contextvm_relay_metadata).toMatchObject({ degraded: true, reason: 'missing_contextvm_relay_set' });
  });

  it('applies replaceable latest-wins and carries NIP-34 policy', () => {
    const result = discovery.normalizeDiscoveryEvents([
      system({ id: 'old', created_at: 1, content: { features: { relay_read_models: false } } }),
      system({ id: 'new', created_at: 2, content: { nostr: { nip34_relays: ['https://nip34.example'] } } }),
      relaySet('bahia-browser-v1', ['wss://old.example'], { id: 'old-relay', created_at: 1 }),
      relaySet('bahia-browser-v1', ['wss://new.example'], { id: 'new-relay', created_at: 2 })
    ], [trustedPubkey]);
    expect(result.features.relay_read_models).toBe(true);
    expect(result.nostr.browser_relays).toEqual(['wss://new.example']);
    expect(result.nostr.nip34_relays).toEqual(['wss://nip34.example']);
  });
});
