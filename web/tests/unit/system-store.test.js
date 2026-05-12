import { describe, it, expect, beforeEach, vi } from 'vitest';

vi.mock('../../src/lib/stores/discovery.svelte.js', () => ({
  discoverSystemInfo: vi.fn()
}));

describe('Nostr discovery store', () => {
  let discovery;
  let store;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    discovery = await import('../../src/lib/stores/discovery.svelte.js');
    store = await import('../../src/lib/stores/system.svelte.js');
    store.resetSystemInfoStore();
  });

  it('loads public Nostr discovery once and serves cached bootstrap data', async () => {
    const info = {
      features: { relay_read_models: true, direct_nostr_http_auth: true },
      nostr: { browser_relays: ['ws://relay.test/relay'], service_pubkey: 'a'.repeat(64) }
    };
    discovery.discoverSystemInfo.mockResolvedValue(info);

    await expect(store.loadSystemInfo()).resolves.toEqual(info);
    await expect(store.loadSystemInfo()).resolves.toEqual(info);

    expect(discovery.discoverSystemInfo).toHaveBeenCalledTimes(1);
    expect(store.currentSystemInfo()).toEqual(info);
    expect(store.systemInfo.data).toEqual(info);
    expect(store.systemInfo.loading).toBe(false);
    expect(store.systemInfo.error).toBeNull();
    expect(store.systemInfo.loadedAt).toEqual(expect.any(String));
  });

  it('deduplicates concurrent Nostr discovery loads', async () => {
    let resolveInfo;
    const infoPromise = new Promise((resolve) => {
      resolveInfo = resolve;
    });
    discovery.discoverSystemInfo.mockReturnValue(infoPromise);

    const first = store.loadSystemInfo();
    const second = store.loadSystemInfo();
    resolveInfo({ features: { relay_read_models: true } });

    await expect(Promise.all([first, second])).resolves.toHaveLength(2);
    expect(discovery.discoverSystemInfo).toHaveBeenCalledTimes(1);
  });

  it('reloads when force is requested', async () => {
    discovery.discoverSystemInfo
      .mockResolvedValueOnce({ version: 'old' })
      .mockResolvedValueOnce({ version: 'new' });

    await store.loadSystemInfo();
    await store.loadSystemInfo({ force: true });

    expect(discovery.discoverSystemInfo).toHaveBeenCalledTimes(2);
    expect(store.currentSystemInfo()).toEqual({ version: 'new' });
  });
});
