import { describe, expect, it, vi } from 'vitest';
import { createVersionReloadWatcher } from '../../src/lib/version-reload.js';

describe('web version reload watcher', () => {
  it('reloads when the served SvelteKit version changes', async () => {
    const reload = vi.fn();
    const responses = ['old-version', 'new-version'];
    const fetchImpl = vi.fn(async () => ({
      ok: true,
      json: async () => ({ version: responses.shift() })
    }));
    const watcher = createVersionReloadWatcher({
      fetchImpl,
      reload,
      window: {},
      document: {}
    });

    await watcher.checkVersion();
    expect(reload).not.toHaveBeenCalled();

    await watcher.checkVersion();
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it('ignores transient version fetch failures', async () => {
    const reload = vi.fn();
    const fetchImpl = vi
      .fn()
      .mockRejectedValueOnce(new Error('network down'))
      .mockResolvedValueOnce({ ok: true, json: async () => ({ version: 'current' }) });
    const watcher = createVersionReloadWatcher({
      fetchImpl,
      reload,
      window: {},
      document: {}
    });

    await watcher.checkVersion();
    await watcher.checkVersion();

    expect(reload).not.toHaveBeenCalled();
  });
});
