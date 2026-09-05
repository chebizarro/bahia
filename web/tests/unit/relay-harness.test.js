import { EventEmitter } from 'node:events';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const spawnMock = vi.fn();

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
    // React to listener attachment instead of racing the harness's internal
    // await chain with a fixed number of microtask ticks: emitting before the
    // harness subscribes would drop the readiness line and hang the test.
    const stderrListenerAttached = new Promise((resolve) => {
      const originalOn = child.stderr.on.bind(child.stderr);
      child.stderr.on = (eventName, listener) => {
        const result = originalOn(eventName, listener);
        if (eventName === 'data') {
          child.stderr.on = originalOn;
          resolve();
        }
        return result;
      };
    });
    // spawn is injected rather than mocked via vi.mock('node:child_process'):
    // builtin-module mocks do not intercept this import graph, which made the
    // old test silently launch a real `go run ./cmd/bahia-test-relay` process
    // locally and hang in CI where no Go toolchain exists.
    const started = startBahiaTestRelay({ addr: '127.0.0.1:48630', spawnImpl: spawnMock });
    await stderrListenerAttached;

    child.stderr.emit('data', Buffer.from('bahia test relay listening on 127.0.0.1:48630\n'));

    await expect(started).resolves.toMatchObject({
      addr: '127.0.0.1:48630',
      servicePubkey: health.service_pubkey,
      eventCount: 3
    });
    expect(spawnMock).toHaveBeenCalledTimes(1);
    expect(globalThis.fetch).toHaveBeenCalledTimes(2);
    expect(child.kill).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });
});
