import { afterEach, describe, expect, it, vi } from 'vitest';
import { createVersionReloadWatcher } from '../../src/lib/version-reload.js';

const environment = vi.hoisted(() => ({ dev: false }));
vi.mock('$app/environment', () => environment);
afterEach(() => { environment.dev = false; });

describe('web version reload watcher', () => {
  it('does not request production version metadata or schedule polling in dev mode', async () => {
    environment.dev = true;
    const fetchImpl = vi.fn();
    const window = { setInterval: vi.fn(), addEventListener: vi.fn() };
    const document = { addEventListener: vi.fn() };
    const watcher = createVersionReloadWatcher({ fetchImpl, window, document });

    const stop = watcher.start();
    await watcher.checkVersion();
    stop();

    expect(fetchImpl).not.toHaveBeenCalled();
    expect(window.setInterval).not.toHaveBeenCalled();
    expect(window.addEventListener).not.toHaveBeenCalled();
    expect(document.addEventListener).not.toHaveBeenCalled();
  });

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
