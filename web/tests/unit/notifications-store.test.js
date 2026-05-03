import { beforeEach, describe, expect, it, vi } from 'vitest';

const privateMock = vi.hoisted(() => ({
  requestPrivateResult: vi.fn(),
  privateTransportAvailable: vi.fn(() => true)
}));

const systemMock = vi.hoisted(() => ({
  currentSystemInfo: vi.fn(() => ({
    nostr: { service_pubkey: 'b'.repeat(64), private_browser_relays: ['wss://private.example'] }
  })),
  loadSystemInfo: vi.fn(async () => ({
    nostr: { service_pubkey: 'b'.repeat(64), private_browser_relays: ['wss://private.example'] }
  }))
}));

vi.mock('$lib/nostr/private-controlplane.js', () => privateMock);
vi.mock('../../src/lib/nostr/private-controlplane.js', () => privateMock);
vi.mock('$lib/stores/system.svelte.js', () => systemMock);
vi.mock('../../src/lib/stores/system.svelte.js', () => systemMock);

describe('notifications private store', () => {
  let store;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    privateMock.privateTransportAvailable.mockReturnValue(true);
    systemMock.currentSystemInfo.mockReturnValue({
      nostr: { service_pubkey: 'b'.repeat(64), private_browser_relays: ['wss://private.example'] }
    });
    store = await import('../../src/lib/stores/notifications.svelte.js');
    store.resetNotificationStore();
  });

  it('loads channels through encrypted private transport operations', async () => {
    privateMock.requestPrivateResult.mockResolvedValueOnce({
      result: { status: 'ok', payload: { channels: [{ id: 'ch-1', name: 'Ops', config: { url: 'https://hook' } }] } }
    });

    await expect(store.listNotificationChannels()).resolves.toEqual([{ id: 'ch-1', name: 'Ops', config: { url: 'https://hook' } }]);

    expect(privateMock.requestPrivateResult).toHaveBeenCalledWith({
      operation: store.NOTIFICATION_PRIVATE_OPERATIONS.listChannels,
      payload: {}
    });
    expect(store.notificationState.channels).toHaveLength(1);
    expect(store.notificationState.channelsError).toBeNull();
  });

  it('creates, updates, deletes, and tests channels through private operations', async () => {
    privateMock.requestPrivateResult
      .mockResolvedValueOnce({ result: { status: 'ok', payload: { channel: { id: 'ch-1', name: 'Ops' } } } })
      .mockResolvedValueOnce({ result: { status: 'ok', payload: { channel: { id: 'ch-1', name: 'Ops updated' } } } })
      .mockResolvedValueOnce({ result: { status: 'ok', payload: { status: 'test sent' } } })
      .mockResolvedValueOnce({ result: { status: 'ok', payload: { status: 'deleted', id: 'ch-1' } } });

    await store.createNotificationChannel({ name: 'Ops' });
    await store.updateNotificationChannel('ch-1', { enabled: false });
    await store.testNotificationChannel('ch-1');
    await store.deleteNotificationChannel('ch-1');

    expect(privateMock.requestPrivateResult).toHaveBeenNthCalledWith(1, { operation: store.NOTIFICATION_PRIVATE_OPERATIONS.createChannel, payload: { name: 'Ops' } });
    expect(privateMock.requestPrivateResult).toHaveBeenNthCalledWith(2, { operation: store.NOTIFICATION_PRIVATE_OPERATIONS.updateChannel, payload: { id: 'ch-1', enabled: false } });
    expect(privateMock.requestPrivateResult).toHaveBeenNthCalledWith(3, { operation: store.NOTIFICATION_PRIVATE_OPERATIONS.testChannel, payload: { id: 'ch-1' } });
    expect(privateMock.requestPrivateResult).toHaveBeenNthCalledWith(4, { operation: store.NOTIFICATION_PRIVATE_OPERATIONS.deleteChannel, payload: { id: 'ch-1' } });
    expect(store.notificationState.channels).toEqual([]);
  });

  it('loads delivery logs only through private encrypted result operations', async () => {
    privateMock.requestPrivateResult.mockResolvedValueOnce({
      result: { status: 'ok', payload: { logs: [{ id: 'log-1', payload: { detail: 'private' } }] } }
    });

    await expect(store.listNotificationLogs({ limit: 50 })).resolves.toEqual([{ id: 'log-1', payload: { detail: 'private' } }]);

    expect(privateMock.requestPrivateResult).toHaveBeenCalledWith({
      operation: store.NOTIFICATION_PRIVATE_OPERATIONS.listLogs,
      payload: { limit: 50 }
    });
    expect(store.notificationState.logs).toHaveLength(1);
  });

  it('fails before publishing when private transport is not advertised', async () => {
    privateMock.privateTransportAvailable.mockReturnValue(false);

    await expect(store.listNotificationChannels()).rejects.toThrow('Private Nostr transport is not available');

    expect(privateMock.requestPrivateResult).not.toHaveBeenCalled();
    expect(store.notificationState.channelsError).toContain('Private Nostr transport is not available');
  });

  it('surfaces encrypted terminal errors from private results', async () => {
    privateMock.requestPrivateResult.mockResolvedValueOnce({
      result: { status: 'error', error: { code: 'handler_failed', message: 'notification channel not found' } }
    });

    await expect(store.listNotificationChannels()).rejects.toThrow('notification channel not found');
  });
});
