import { describe, it, expect, beforeEach, vi } from 'vitest';

vi.mock('../../src/lib/api/client.js', () => ({
  api: {
    getSystemInfo: vi.fn()
  }
}));

describe('system info store', () => {
  let api;
  let store;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    api = (await import('../../src/lib/api/client.js')).api;
    store = await import('../../src/lib/stores/system.svelte.js');
    store.resetSystemInfoStore();
  });

  it('loads public system info once and serves cached bootstrap data', async () => {
    const info = {
      features: { relay_read_models: true, direct_nostr_http_auth: true },
      nostr: { browser_relays: ['ws://relay.test/relay'], service_pubkey: 'a'.repeat(64) }
    };
    api.getSystemInfo.mockResolvedValue(info);

    await expect(store.loadSystemInfo()).resolves.toEqual(info);
    await expect(store.loadSystemInfo()).resolves.toEqual(info);

    expect(api.getSystemInfo).toHaveBeenCalledTimes(1);
    expect(store.currentSystemInfo()).toEqual(info);
    expect(store.systemInfo.data).toEqual(info);
    expect(store.systemInfo.loading).toBe(false);
    expect(store.systemInfo.error).toBeNull();
    expect(store.systemInfo.loadedAt).toEqual(expect.any(String));
  });

  it('deduplicates concurrent system info loads', async () => {
    let resolveInfo;
    const infoPromise = new Promise((resolve) => {
      resolveInfo = resolve;
    });
    api.getSystemInfo.mockReturnValue(infoPromise);

    const first = store.loadSystemInfo();
    const second = store.loadSystemInfo();
    resolveInfo({ features: { relay_read_models: true } });

    await expect(Promise.all([first, second])).resolves.toHaveLength(2);
    expect(api.getSystemInfo).toHaveBeenCalledTimes(1);
  });

  it('reloads when force is requested', async () => {
    api.getSystemInfo
      .mockResolvedValueOnce({ version: 'old' })
      .mockResolvedValueOnce({ version: 'new' });

    await store.loadSystemInfo();
    await store.loadSystemInfo({ force: true });

    expect(api.getSystemInfo).toHaveBeenCalledTimes(2);
    expect(store.currentSystemInfo()).toEqual({ version: 'new' });
  });
});
