import { beforeEach, describe, expect, it, vi } from 'vitest';

const bridge = vi.hoisted(() => ({
  store: { query: vi.fn(() => []) },
  pool: { getConnectedRelays: vi.fn(() => []), onConnectionStatus: vi.fn(() => vi.fn()) },
  relays: ['https://relay.example'],
  pubkey: 'b'.repeat(64),
  handlers: null,
  active: false,
  boot: vi.fn(async () => undefined),
  init: vi.fn((handlers) => { bridge.handlers = handlers; bridge.active = true; return { unsubscribe: vi.fn() }; }),
  teardown: vi.fn(() => { bridge.active = false; bridge.handlers = null; })
}));
vi.mock('../../src/lib/nostr/boot.js', () => ({
  boot: bridge.boot,
  getEventStore: () => bridge.store,
  getPool: () => bridge.pool,
  getRelayUrls: () => bridge.relays,
  getServicePubkey: () => bridge.pubkey
}));
vi.mock('../../src/lib/stores/collections/store-first-subscriptions.js', () => ({
  initStoreFirstSubscriptions: bridge.init,
  teardownStoreFirstSubscriptions: bridge.teardown
}));
vi.mock('../../src/lib/stores/collections/index.svelte.js', () => ({}));

const controlplane = await import('../../src/lib/stores/controlplane.svelte.js');

beforeEach(() => {
  vi.clearAllMocks();
  bridge.relays = ['https://relay.example'];
  bridge.pubkey = 'b'.repeat(64);
  bridge.handlers = null;
  bridge.active = false;
  bridge.boot.mockResolvedValue(undefined);
  bridge.pool.getConnectedRelays.mockReturnValue([]);
  controlplane.resetControlplaneStore();
  vi.clearAllMocks();
});

describe('store-first controlplane bootstrap', () => {
  it('starts one trusted shared REQ without waiting for EOSE before views become ready', async () => {
    await expect(controlplane.bootstrapControlplane()).resolves.toEqual({ ok: true });
    expect(bridge.boot).toHaveBeenCalledOnce();
    expect(bridge.init).toHaveBeenCalledOnce();
    expect(controlplane.controlplaneConnection).toMatchObject({
      ready: true, bootstrapComplete: false, status: 'syncing',
      relays: ['wss://relay.example'], servicePubkey: 'b'.repeat(64)
    });
    await controlplane.bootstrapControlplane();
    expect(bridge.init).toHaveBeenCalledOnce();
  });

  it('marks live only after every relay reaches EOSE', async () => {
    bridge.relays = ['wss://one.example', 'wss://two.example'];
    await controlplane.bootstrapControlplane();
    bridge.handlers.onEose('wss://one.example');
    expect(controlplane.controlplaneConnection.bootstrapComplete).toBe(false);
    bridge.handlers.onEose('wss://one.example');
    bridge.handlers.onEose('wss://two.example');
    expect(controlplane.controlplaneConnection).toMatchObject({ bootstrapComplete: true, status: 'live' });
  });

  it('tears down and can bootstrap again after disconnect', async () => {
    await controlplane.bootstrapControlplane();
    controlplane.disconnectControlplane();
    expect(bridge.teardown).toHaveBeenCalledOnce();
    expect(controlplane.controlplaneConnection.ready).toBe(false);
    await controlplane.bootstrapControlplane();
    expect(bridge.init).toHaveBeenCalledTimes(2);
  });

  it('fails closed when the trusted service pubkey or relays are absent', async () => {
    bridge.pubkey = '';
    await expect(controlplane.bootstrapControlplane()).resolves.toMatchObject({ ok: false });
    expect(bridge.init).not.toHaveBeenCalled();
    bridge.pubkey = 'b'.repeat(64);
    bridge.relays = [];
    await expect(controlplane.bootstrapControlplane()).resolves.toMatchObject({ ok: false });
    expect(bridge.init).not.toHaveBeenCalled();
  });

  it('surfaces relay closure without treating it as a completed historical read', async () => {
    await controlplane.bootstrapControlplane();
    bridge.handlers.onClosed('auth-required: denied', 'wss://relay.example');
    expect(controlplane.controlplaneConnection).toMatchObject({ bootstrapComplete: false, status: 'disconnected' });
    expect(controlplane.controlplaneConnection.lastError).toContain('auth-required');
  });
});
