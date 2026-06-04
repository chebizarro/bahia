import { beforeEach, describe, expect, it, vi } from 'vitest';

import { BahiaClient } from '../../src/lib/api/client.js';

function json(data) {
  return {
    ok: true,
    headers: new Map([['content-type', 'application/json']]),
    json: async () => ({ data })
  };
}

function error(status, statusText) {
  return {
    ok: false,
    status,
    statusText,
    json: async () => { throw new Error('not json'); }
  };
}

describe('BahiaClient retry and edge behavior', () => {
  let client;

  beforeEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
    global.fetch = vi.fn();
    client = new BahiaClient();
  });

  it('retries GET once on network error by default', async () => {
    global.fetch.mockRejectedValueOnce(new Error('network down')).mockResolvedValueOnce(json(['ok']));

    await expect(client.getBlossomServers()).resolves.toEqual(['ok']);

    expect(global.fetch).toHaveBeenCalledTimes(2);
  });

  it('does not retry POST by default on network error', async () => {
    global.fetch.mockRejectedValueOnce(new Error('network down'));

    await expect(client.listBlossomBlobs()).rejects.toThrow('network down');
    expect(global.fetch).toHaveBeenCalledTimes(1);
  });

  it('supports explicit retry override for POST requests', async () => {
    global.fetch.mockRejectedValueOnce(new Error('timeout')).mockResolvedValueOnce(json({ ok: true }));

    await expect(client.fetch('/blossom/list', { method: 'POST', body: '{}', retries: 1, retryDelayMs: 0 })).resolves.toEqual({ ok: true });

    expect(global.fetch).toHaveBeenCalledTimes(2);
  });

  it('retries GET on retriable 5xx responses by default', async () => {
    global.fetch.mockResolvedValueOnce(error(503, 'Service Unavailable')).mockResolvedValueOnce(json({ ok: true }));

    await expect(client.fetch('/blossom/health', { retryDelayMs: 0 })).resolves.toEqual({ ok: true });

    expect(global.fetch).toHaveBeenCalledTimes(2);
  });

  it('does not retry non-retriable status codes by default', async () => {
    global.fetch.mockResolvedValueOnce(error(429, 'Too Many Requests'));

    await expect(client.fetch('/sbom/search', { retries: 3, retryDelayMs: 0 })).rejects.toThrow('HTTP 429: Too Many Requests');
    expect(global.fetch).toHaveBeenCalledTimes(1);
  });

  it('applies exponential backoff delay across retries', async () => {
    vi.useFakeTimers();
    global.fetch
      .mockRejectedValueOnce(new Error('network down'))
      .mockRejectedValueOnce(new Error('network down'))
      .mockResolvedValueOnce(json({ ok: true }));

    const fetchPromise = client.fetch('/blossom/health', { retries: 2, retryDelayMs: 100 });

    await vi.advanceTimersByTimeAsync(99);
    expect(global.fetch).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(1);
    expect(global.fetch).toHaveBeenCalledTimes(2);

    await vi.advanceTimersByTimeAsync(199);
    expect(global.fetch).toHaveBeenCalledTimes(2);

    await vi.advanceTimersByTimeAsync(1);
    await expect(fetchPromise).resolves.toEqual({ ok: true });
    expect(global.fetch).toHaveBeenCalledTimes(3);
  });

  it('supports configurable retry statuses', async () => {
    global.fetch.mockResolvedValueOnce(error(429, 'Too Many Requests')).mockResolvedValueOnce(json({ ok: true }));

    await expect(client.fetch('/sbom/search', { retries: 1, retryDelayMs: 0, retryStatuses: [429] })).resolves.toEqual({ ok: true });

    expect(global.fetch).toHaveBeenCalledTimes(2);
  });

  it('defaults nullable Blossom responses to empty containers', async () => {
    global.fetch
      .mockResolvedValueOnce(json(null))
      .mockResolvedValueOnce(json(null))
      .mockResolvedValueOnce(json(null));

    await expect(client.getBlossomServers()).resolves.toEqual([]);
    await expect(client.checkBlossomHealth()).resolves.toEqual({});
    await expect(client.getBlossomStats()).resolves.toEqual({});
  });
});
