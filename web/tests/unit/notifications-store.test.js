import { beforeEach, describe, expect, it, vi } from 'vitest';

const encryptedRequestsMock = vi.hoisted(() => ({
  requestEncryptedResult: vi.fn(),
  encryptedRequestsAvailable: vi.fn(() => true),
  servicePubkeyFromSystemInfo: vi.fn(() => 'b'.repeat(64))
}));

const systemMock = vi.hoisted(() => ({
  currentSystemInfo: vi.fn(() => ({
    nostr: { service_pubkey: 'b'.repeat(64), browser_relays: ['wss://requests.example'] }
  })),
  loadSystemInfo: vi.fn(async () => ({
    nostr: { service_pubkey: 'b'.repeat(64), browser_relays: ['wss://requests.example'] }
  }))
}));

const bootstrapMock = vi.hoisted(() => vi.fn(async () => ({ ok: true })));
const publishCommandMock = vi.hoisted(() => vi.fn());

vi.mock('$lib/nostr/encrypted-controlplane.js', () => ({
  ...encryptedRequestsMock,
  CONTEXTVM_MESSAGE_KIND: 25910,
  publishEncryptedRequest: vi.fn()
}));
vi.mock('../../src/lib/nostr/encrypted-controlplane.js', () => ({
  ...encryptedRequestsMock,
  CONTEXTVM_MESSAGE_KIND: 25910,
  publishEncryptedRequest: vi.fn()
}));
vi.mock('$lib/stores/system.svelte.js', () => systemMock);
vi.mock('$lib/stores/controlplane.svelte.js', () => ({ bootstrapControlplane: bootstrapMock }));
vi.mock('../../src/lib/stores/controlplane.svelte.js', () => ({ bootstrapControlplane: bootstrapMock }));
vi.mock('$app/navigation', () => ({ goto: vi.fn() }));
vi.mock('$lib/nostr/retained-domain-subscription.js', () => ({
  subscribeToDomainRefresh: vi.fn(async () => vi.fn())
}));
vi.mock('../../src/lib/stores/system.svelte.js', () => systemMock);

describe('notifications encrypted store', () => {
  let store;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    encryptedRequestsMock.encryptedRequestsAvailable.mockReturnValue(true);
    systemMock.currentSystemInfo.mockReturnValue({
      nostr: { service_pubkey: 'b'.repeat(64), browser_relays: ['wss://requests.example'] }
    });
    store = await import('../../src/lib/stores/notifications.svelte.js');
    store.resetNotificationStore();
  });

  it('loads channels through ContextVM request operations', async () => {
    encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
      result: { status: 'ok', payload: { channels: [{ id: 'ch-1', name: 'Ops', config: { url: 'https://hook' } }] } }
    });

    await expect(store.listNotificationChannels()).resolves.toEqual([{ id: 'ch-1', name: 'Ops', config: { url: 'https://hook' } }]);

    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenCalledWith({
      operation: 'notifications.channels.list',
      payload: {}
    });
    expect(store.notificationState.channels).toHaveLength(1);
    expect(store.notificationState.channelsError).toBeNull();
  });

  it('creates, updates, deletes, and tests channels through intent publishing and encrypted operations', async () => {
    // Phase 3 N1: create/update/delete go through publishCommand (intent publishing).
    // test still uses requestEncryptedResult (ContextVM read path).
    encryptedRequestsMock.requestEncryptedResult
      // publishCommand uses requestEncryptedResult internally for create
      .mockResolvedValueOnce({ requestEventId: 'req-1', result: { channel: { id: 'ch-1', name: 'Ops' }, status: 'created' } })
      // publishCommand for update
      .mockResolvedValueOnce({ requestEventId: 'req-2', result: { channel: { id: 'ch-1', name: 'Ops updated' }, status: 'updated' } })
      // testChannel via ContextVM
      .mockResolvedValueOnce({ result: { status: 'ok', payload: { status: 'test sent' } } })
      // publishCommand for delete
      .mockResolvedValueOnce({ requestEventId: 'req-3', result: { status: 'deleted', channel_id: 'ch-1' } });

    await store.createNotificationChannel({ name: 'Ops' });
    await store.updateNotificationChannel('ch-1', { enabled: false });
    await store.testNotificationChannel('ch-1');
    await store.deleteNotificationChannel('ch-1');

    // Create uses publishCommand → requestEncryptedResult with notification/create operation
    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenNthCalledWith(1, expect.objectContaining({
      operation: 'notification/create',
      payload: { name: 'Ops' }
    }));
    // Update uses publishCommand → requestEncryptedResult with notification/update operation
    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenNthCalledWith(2, expect.objectContaining({
      operation: 'notification/update',
      payload: { id: 'ch-1', enabled: false }
    }));
    // Test still uses ContextVM encrypted operations
    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenNthCalledWith(3, { operation: 'notifications.channels.test', payload: { id: 'ch-1' } });
    // Delete uses publishCommand → requestEncryptedResult with notification/delete operation
    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenNthCalledWith(4, expect.objectContaining({
      operation: 'notification/delete',
      payload: { id: 'ch-1' }
    }));
    expect(store.notificationState.channels).toEqual([]);
  });

  it('loads delivery logs only through encrypted result operations', async () => {
    encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
      result: { status: 'ok', payload: { logs: [{ id: 'log-1', payload: { detail: 'private' } }] } }
    });

    await expect(store.listNotificationLogs({ limit: 50 })).resolves.toEqual([{ id: 'log-1', payload: { detail: 'private' } }]);

    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenCalledWith({
      operation: 'notifications.logs.list',
      payload: { limit: 50 }
    });
    expect(store.notificationState.logs).toHaveLength(1);
    expect(store.notificationState.logsError).toBeNull();
    expect(store.notificationState.logsLoading).toBe(false);
  });

  it('clears stale log entries and sets logsError when encrypted log retrieval fails', async () => {
    encryptedRequestsMock.requestEncryptedResult
      .mockResolvedValueOnce({
        result: { status: 'ok', payload: { logs: [{ id: 'log-1', payload: { detail: 'private' } }] } }
      })
      .mockResolvedValueOnce({
        result: { status: 'error', error: { code: 'handler_failed', message: 'failed to list notification logs' } }
      });

    await expect(store.listNotificationLogs({ limit: 50 })).resolves.toHaveLength(1);
    expect(store.notificationState.logs).toHaveLength(1);

    await expect(store.listNotificationLogs({ limit: 25 })).rejects.toThrow('failed to list notification logs');

    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenNthCalledWith(2, {
      operation: 'notifications.logs.list',
      payload: { limit: 25 }
    });
    expect(store.notificationState.logs).toEqual([]);
    expect(store.notificationState.logsError).toBe('failed to list notification logs');
    expect(store.notificationState.logsLoading).toBe(false);
  });

  it('fails before publishing when ContextVM requests are not advertised', async () => {
    encryptedRequestsMock.encryptedRequestsAvailable.mockReturnValue(false);

    await expect(store.listNotificationChannels()).rejects.toThrow('ContextVM requests are not available');

    expect(encryptedRequestsMock.requestEncryptedResult).not.toHaveBeenCalled();
    expect(store.notificationState.channelsError).toContain('ContextVM requests are not available');
  });

  it('surfaces encrypted terminal errors from result events', async () => {
    encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
      result: { status: 'error', error: { code: 'handler_failed', message: 'notification channel not found' } }
    });

    await expect(store.listNotificationChannels()).rejects.toThrow('notification channel not found');
  });
});
