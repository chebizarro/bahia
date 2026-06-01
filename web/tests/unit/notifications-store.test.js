import { beforeEach, describe, expect, it, vi } from 'vitest';

const encryptedRequestsMock = vi.hoisted(() => ({
  requestEncryptedResult: vi.fn(),
  encryptedRequestsAvailable: vi.fn(() => true)
}));

const systemMock = vi.hoisted(() => ({
  currentSystemInfo: vi.fn(() => ({
    nostr: { service_pubkey: 'b'.repeat(64), browser_relays: ['wss://requests.example'] }
  })),
  loadSystemInfo: vi.fn(async () => ({
    nostr: { service_pubkey: 'b'.repeat(64), browser_relays: ['wss://requests.example'] }
  }))
}));

vi.mock('$lib/nostr/encrypted-controlplane.js', () => encryptedRequestsMock);
vi.mock('../../src/lib/nostr/encrypted-controlplane.js', () => encryptedRequestsMock);
vi.mock('$lib/stores/system.svelte.js', () => systemMock);
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

  it('loads channels through encrypted Nostr request operations', async () => {
    encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
      result: { status: 'ok', payload: { channels: [{ id: 'ch-1', name: 'Ops', config: { url: 'https://hook' } }] } }
    });

    await expect(store.listNotificationChannels()).resolves.toEqual([{ id: 'ch-1', name: 'Ops', config: { url: 'https://hook' } }]);

    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenCalledWith({
      operation: store.NOTIFICATION_ENCRYPTED_OPERATIONS.listChannels,
      payload: {}
    });
    expect(store.notificationState.channels).toHaveLength(1);
    expect(store.notificationState.channelsError).toBeNull();
  });

  it('creates, updates, deletes, and tests channels through encrypted operations', async () => {
    encryptedRequestsMock.requestEncryptedResult
      .mockResolvedValueOnce({ result: { status: 'ok', payload: { channel: { id: 'ch-1', name: 'Ops' } } } })
      .mockResolvedValueOnce({ result: { status: 'ok', payload: { channel: { id: 'ch-1', name: 'Ops updated' } } } })
      .mockResolvedValueOnce({ result: { status: 'ok', payload: { status: 'test sent' } } })
      .mockResolvedValueOnce({ result: { status: 'ok', payload: { status: 'deleted', id: 'ch-1' } } });

    await store.createNotificationChannel({ name: 'Ops' });
    await store.updateNotificationChannel('ch-1', { enabled: false });
    await store.testNotificationChannel('ch-1');
    await store.deleteNotificationChannel('ch-1');

    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenNthCalledWith(1, { operation: store.NOTIFICATION_ENCRYPTED_OPERATIONS.createChannel, payload: { name: 'Ops' } });
    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenNthCalledWith(2, { operation: store.NOTIFICATION_ENCRYPTED_OPERATIONS.updateChannel, payload: { id: 'ch-1', enabled: false } });
    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenNthCalledWith(3, { operation: store.NOTIFICATION_ENCRYPTED_OPERATIONS.testChannel, payload: { id: 'ch-1' } });
    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenNthCalledWith(4, { operation: store.NOTIFICATION_ENCRYPTED_OPERATIONS.deleteChannel, payload: { id: 'ch-1' } });
    expect(store.notificationState.channels).toEqual([]);
  });

  it('loads delivery logs only through encrypted result operations', async () => {
    encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
      result: { status: 'ok', payload: { logs: [{ id: 'log-1', payload: { detail: 'private' } }] } }
    });

    await expect(store.listNotificationLogs({ limit: 50 })).resolves.toEqual([{ id: 'log-1', payload: { detail: 'private' } }]);

    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenCalledWith({
      operation: store.NOTIFICATION_ENCRYPTED_OPERATIONS.listLogs,
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
      operation: store.NOTIFICATION_ENCRYPTED_OPERATIONS.listLogs,
      payload: { limit: 25 }
    });
    expect(store.notificationState.logs).toEqual([]);
    expect(store.notificationState.logsError).toBe('failed to list notification logs');
    expect(store.notificationState.logsLoading).toBe(false);
  });

  it('fails before publishing when encrypted Nostr requests are not advertised', async () => {
    encryptedRequestsMock.encryptedRequestsAvailable.mockReturnValue(false);

    await expect(store.listNotificationChannels()).rejects.toThrow('Encrypted Nostr events are not available');

    expect(encryptedRequestsMock.requestEncryptedResult).not.toHaveBeenCalled();
    expect(store.notificationState.channelsError).toContain('Encrypted Nostr events are not available');
  });

  it('surfaces encrypted terminal errors from result events', async () => {
    encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
      result: { status: 'error', error: { code: 'handler_failed', message: 'notification channel not found' } }
    });

    await expect(store.listNotificationChannels()).rejects.toThrow('notification channel not found');
  });
});
