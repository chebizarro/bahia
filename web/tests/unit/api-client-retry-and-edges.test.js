import { describe, it, expect, beforeEach, vi } from 'vitest';

global.window = global;

describe('BahiaClient - Retry and Edge Coverage', () => {
  let BahiaClient;
  let client;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    const module = await import('../../src/lib/api/client.js');
    BahiaClient = module.BahiaClient;
    client = new BahiaClient();
  });

  it('retries GET once on network error by default', async () => {
    global.fetch
      .mockRejectedValueOnce(new Error('network down'))
      .mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: [{ id: 'svc-1' }] })
      });

    const result = await client.listServices();

    expect(global.fetch).toHaveBeenCalledTimes(2);
    expect(result).toEqual([{ id: 'svc-1' }]);
  });

  it('does not retry POST by default on network error', async () => {
    global.fetch.mockRejectedValueOnce(new Error('network down'));

    await expect(client.createService({ name: 'svc' })).rejects.toThrow('network down');
    expect(global.fetch).toHaveBeenCalledTimes(1);
  });

  it('supports explicit retry override', async () => {
    global.fetch
      .mockRejectedValueOnce(new Error('timeout'))
      .mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { ok: true } })
      });

    const result = await client.fetch('/custom', { method: 'POST', retries: 1, retryDelayMs: 0 });

    expect(global.fetch).toHaveBeenCalledTimes(2);
    expect(result).toEqual({ ok: true });
  });

  it('retries GET on retriable 5xx responses by default', async () => {
    global.fetch
      .mockResolvedValueOnce({
        ok: false,
        status: 503,
        statusText: 'Service Unavailable',
        json: async () => {
          throw new Error('not json');
        }
      })
      .mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: [{ id: 'svc-1' }] })
      });

    const result = await client.fetch('/services', { retryDelayMs: 0 });

    expect(global.fetch).toHaveBeenCalledTimes(2);
    expect(result).toEqual([{ id: 'svc-1' }]);
  });

  it('does not retry non-retriable status codes by default', async () => {
    global.fetch.mockResolvedValueOnce({
      ok: false,
      status: 429,
      statusText: 'Too Many Requests',
      json: async () => {
        throw new Error('not json');
      }
    });

    await expect(client.fetch('/rate-limited', { retries: 3, retryDelayMs: 0 })).rejects.toThrow('HTTP 429: Too Many Requests');
    expect(global.fetch).toHaveBeenCalledTimes(1);
  });

  it('applies exponential backoff delay across retries', async () => {
    vi.useFakeTimers();
    global.fetch
      .mockRejectedValueOnce(new Error('network down'))
      .mockRejectedValueOnce(new Error('network down'))
      .mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { ok: true } })
      });

    const fetchPromise = client.fetch('/custom', { retries: 2, retryDelayMs: 100 });

    await vi.advanceTimersByTimeAsync(99);
    expect(global.fetch).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(1);
    expect(global.fetch).toHaveBeenCalledTimes(2);

    await vi.advanceTimersByTimeAsync(199);
    expect(global.fetch).toHaveBeenCalledTimes(2);

    await vi.advanceTimersByTimeAsync(1);
    await expect(fetchPromise).resolves.toEqual({ ok: true });
    expect(global.fetch).toHaveBeenCalledTimes(3);

    vi.useRealTimers();
  });

  it('supports configurable retry statuses', async () => {
    global.fetch
      .mockResolvedValueOnce({
        ok: false,
        status: 429,
        statusText: 'Too Many Requests',
        json: async () => {
          throw new Error('not json');
        }
      })
      .mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { ok: true } })
      });

    const result = await client.fetch('/rate-limited', {
      retries: 1,
      retryDelayMs: 0,
      retryStatuses: [429]
    });

    expect(global.fetch).toHaveBeenCalledTimes(2);
    expect(result).toEqual({ ok: true });
  });

  it('falls back to HTTP status error when non-2xx body is not JSON', async () => {
    global.fetch.mockResolvedValueOnce({
      ok: false,
      status: 502,
      statusText: 'Bad Gateway',
      json: async () => {
        throw new Error('not json');
      }
    });

    await expect(client.fetch('/bad-gateway', { retries: 0 })).rejects.toThrow('HTTP 502: Bad Gateway');
  });

  it('streamLogs wires log/error handlers and close function', () => {
    const handlers = {};
    const close = vi.fn();
    global.EventSource = vi.fn(function EventSourceMock() {
      return {
        addEventListener: vi.fn((name, cb) => {
          handlers[name] = cb;
        }),
        close,
        onerror: null
      };
    });

    const onLog = vi.fn();
    const onError = vi.fn();

    const stop = client.streamLogs('svc', 'env', 25, onLog, onError);

    handlers.log({ data: JSON.stringify({ line: 'hello' }) });
    expect(onLog).toHaveBeenCalledWith({ line: 'hello' });

    const eventSource = global.EventSource.mock.results[0].value;
    eventSource.onerror(new Error('sse failed'));
    expect(onError).toHaveBeenCalled();

    stop();
    expect(close).toHaveBeenCalledTimes(1);
  });

  it('lookupRepositoryCI returns empty array when results are missing', async () => {
    global.fetch.mockResolvedValueOnce({
      ok: true,
      headers: new Map([['content-type', 'application/json']]),
      json: async () => ({ data: {} })
    });

    const result = await client.lookupRepositoryCI('org/repo');
    expect(result).toEqual([]);
  });

  it('listBlossomBlobs sends empty body when pubkey is omitted', async () => {
    global.fetch.mockResolvedValueOnce({
      ok: true,
      headers: new Map([['content-type', 'application/json']]),
      json: async () => ({ data: [] })
    });

    await client.listBlossomBlobs();

    expect(global.fetch).toHaveBeenCalledWith(
      '/api/v1/blossom/list',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({}) })
    );
  });

  it('organization member/invite methods call expected paths', async () => {
    global.fetch.mockResolvedValue({
      ok: true,
      headers: new Map([['content-type', 'application/json']]),
      json: async () => ({ data: [] })
    });

    await client.listOrgMembers('org-1');
    await client.listOrgInvites('org-1');
    await client.getMyInvites();

    expect(global.fetch).toHaveBeenNthCalledWith(1, '/api/v1/orgs/org-1/members', expect.any(Object));
    expect(global.fetch).toHaveBeenNthCalledWith(2, '/api/v1/orgs/org-1/invites', expect.any(Object));
    expect(global.fetch).toHaveBeenNthCalledWith(3, '/api/v1/me/invites', expect.any(Object));
  });
});
