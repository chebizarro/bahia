import { EventEmitter } from 'node:events';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const spawnMock = vi.hoisted(() => vi.fn());

vi.mock('node:child_process', async (importOriginal) => ({
  ...await importOriginal(),
  spawn: spawnMock
}));

describe('relay harness', () => {
  let child;
  let originalFetch;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    originalFetch = globalThis.fetch;
    child = Object.assign(new EventEmitter(), {
      stdout: new EventEmitter(),
      stderr: new EventEmitter(),
      exitCode: null,
      kill: vi.fn()
    });
    spawnMock.mockReturnValue(child);
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    vi.useRealTimers();
  });

  it('waits for the relay readiness signal without a polling sleep', async () => {
    const health = {
      ok: true,
      service_pubkey: 'a'.repeat(64),
      events: 3
    };
    globalThis.fetch = vi.fn()
      .mockRejectedValueOnce(new Error('not started'))
      .mockResolvedValueOnce({
        ok: true,
        json: async () => health
      });

    const { startBahiaTestRelay } = await import('../e2e/relay-harness.js');
    const started = startBahiaTestRelay({ addr: '127.0.0.1:48630' });
    await Promise.resolve();

    child.stderr.emit('data', Buffer.from('bahia test relay listening on 127.0.0.1:48630\n'));

    await expect(started).resolves.toMatchObject({
      addr: '127.0.0.1:48630',
      servicePubkey: health.service_pubkey,
      eventCount: 3
    });
    expect(globalThis.fetch).toHaveBeenCalledTimes(2);
    expect(child.kill).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });
});
