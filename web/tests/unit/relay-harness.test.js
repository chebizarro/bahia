import { EventEmitter } from 'node:events';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const spawnMock = vi.fn();

function healthFixture() {
  return {
    ok: true,
    service_pubkey: 'a'.repeat(64),
    events: 3
  };
}

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

  async function stderrListenerAttached() {
    await new Promise((resolve) => {
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
  }

  async function stopRelay(relay) {
    const stopped = relay.stop();
    child.exitCode = 0;
    child.emit('exit', 0);
    await stopped;
  }

  it('uses the relay address assigned for an ephemeral port', async () => {
    const health = healthFixture();
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => health
    });

    const { startBahiaTestRelay } = await import('../e2e/relay-harness.js');
    const listening = stderrListenerAttached();
    const started = startBahiaTestRelay({ addr: '127.0.0.1:0', spawnImpl: spawnMock });
    await listening;
    child.stderr.emit('data', Buffer.from('bahia test relay listening on 127.0.0.1:49123\n'));

    const relay = await started;
    expect(relay).toMatchObject({
      addr: '127.0.0.1:49123',
      httpUrl: 'http://127.0.0.1:49123',
      wsUrl: 'ws://127.0.0.1:49123',
      servicePubkey: health.service_pubkey,
      eventCount: 3
    });
    expect(spawnMock).toHaveBeenCalledWith(
      'go',
      ['run', './cmd/bahia-test-relay', '--addr', '127.0.0.1:0'],
      expect.objectContaining({ detached: process.platform !== 'win32' })
    );
    expect(globalThis.fetch).toHaveBeenCalledOnce();
    expect(globalThis.fetch).toHaveBeenCalledWith('http://127.0.0.1:49123/healthz');

    await stopRelay(relay);
    expect(child.kill).toHaveBeenCalledWith('SIGTERM');
    expect(vi.getTimerCount()).toBe(0);
  });

  it('starts a fresh relay instead of adopting an existing healthy address', async () => {
    const health = healthFixture();
    const killImpl = vi.fn((_, signal) => {
      if (signal === 0) {
        const error = new Error('no such process group');
        error.code = 'ESRCH';
        throw error;
      }
    });
    child.pid = 4242;
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => health
    });

    const { startBahiaTestRelay } = await import('../e2e/relay-harness.js');
    const listening = stderrListenerAttached();
    const started = startBahiaTestRelay({
      addr: '127.0.0.1:48629',
      spawnImpl: spawnMock,
      killImpl
    });
    expect(spawnMock).toHaveBeenCalledOnce();
    expect(globalThis.fetch).not.toHaveBeenCalled();

    await listening;
    child.stderr.emit('data', Buffer.from('bahia test relay listening on 127.0.0.1:48629\n'));
    const relay = await started;
    expect(globalThis.fetch).toHaveBeenCalledOnce();

    await stopRelay(relay);
    expect(killImpl).toHaveBeenCalledWith(-4242, 'SIGTERM');
    expect(killImpl).toHaveBeenCalledWith(-4242, 0);
    expect(child.kill).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });

  it('force kills a relay process group that does not exit after SIGTERM', async () => {
    const health = healthFixture();
    const killImpl = vi.fn((_, signal) => {
      if (signal === 0) {
        const error = new Error('no such process group');
        error.code = 'ESRCH';
        throw error;
      }
    });
    child.pid = 4242;
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => health
    });

    const { startBahiaTestRelay } = await import('../e2e/relay-harness.js');
    const listening = stderrListenerAttached();
    const started = startBahiaTestRelay({
      addr: '127.0.0.1:0',
      spawnImpl: spawnMock,
      killImpl
    });
    await listening;
    child.stderr.emit('data', Buffer.from('bahia test relay listening on 127.0.0.1:49123\n'));
    const relay = await started;

    const stopped = relay.stop();
    await vi.advanceTimersByTimeAsync(5_000);
    expect(killImpl).toHaveBeenCalledWith(-4242, 'SIGKILL');
    child.exitCode = 0;
    child.emit('exit', 0);
    await stopped;
    expect(vi.getTimerCount()).toBe(0);
  });
});
