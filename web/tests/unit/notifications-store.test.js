import { beforeEach, describe, expect, it, vi } from 'vitest';

const intentMock = vi.hoisted(() => vi.fn(async request => ({ id: request.coordinate, intentId: request.intentId, pending: true })));
const acceptedMock = vi.hoisted(() => vi.fn(async () => ({ data: { status: 'test sent' } })));
const topicMock = vi.hoisted(() => ({ channels: [], logs: [], error: null }));
vi.mock('../../src/lib/stores/sensitive-intents.svelte.js', () => ({
  orgIdFor: record => record?.org_id || '0199c749-9300-7444-8444-444444444444',
  submitSensitiveIntent: intentMock
}));
vi.mock('../../src/lib/nostr/intent-client.svelte.js', () => ({ acceptedIntentStatus: acceptedMock }));
vi.mock('../../src/lib/stores/collections/confidential-records.js', () => ({
  readConfidentialTopic: topic => {
    if (topicMock.error) throw topicMock.error;
    return { rows: topic === 'notification-log' ? topicMock.logs : topicMock.channels };
  }
}));
vi.mock('../../src/lib/nostr/boot.js', () => ({ onStoreRefresh: () => () => {} }));
vi.mock('../../src/lib/stores/auth-roles.svelte.js', () => ({ onContentKeyChange: () => () => {} }));

const ORG_ID = '0199c749-9300-7444-8444-444444444444';

describe('notification canonical read model and signed intents', () => {
  let store;
  beforeEach(async () => {
    vi.resetModules(); vi.clearAllMocks();
    topicMock.channels = []; topicMock.logs = []; topicMock.error = null;
    store = await import('../../src/lib/stores/notifications.svelte.js');
    store.resetNotificationStore();
  });

  it('loads channels from the confidential event-store view', async () => {
    topicMock.channels = [{ id: 'ch-1', name: 'Ops', org_id: ORG_ID }];
    await expect(store.listNotificationChannels()).resolves.toEqual(topicMock.channels);
    expect(store.notificationState.channelsError).toBeNull();
  });

  it('submits channel CRUD as gift-wrapped intents', async () => {
    const created = await store.createNotificationChannel({ name: 'Ops', org_id: ORG_ID });
    await store.updateNotificationChannel(created.id, { enabled: false });
    await store.deleteNotificationChannel(created.id);
    expect(intentMock.mock.calls.map(([request]) => request.op)).toEqual(['create', 'update', 'delete']);
  });

  it('tests a loaded channel with a gift-wrapped intent and scoped status data', async () => {
    topicMock.channels = [{ id: 'ch-1', org_id: ORG_ID }];
    await store.listNotificationChannels();
    await expect(store.testNotificationChannel('ch-1')).resolves.toEqual({ status: 'test sent' });
    const request = intentMock.mock.calls[0][0];
    expect(request).toMatchObject({ domain: 'notification', op: 'channel-test', coordinate: 'ch-1', orgId: ORG_ID,
      content: { id: 'ch-1', intent_id: request.intentId } });
    expect(acceptedMock).toHaveBeenCalledWith({ coordinate: 'ch-1', intentId: request.intentId });
  });

  it('does not test a channel absent from the canonical view', async () => {
    await expect(store.testNotificationChannel('missing')).rejects.toThrow('Load the canonical notification channel');
    expect(intentMock).not.toHaveBeenCalled();
  });

  it('reads delivery logs from confidential cp-state, sorted and bounded', async () => {
    topicMock.logs = [{ channel_id: 'ch-1', logs: [
      { id: 'old', channel_id: 'ch-1', created_at: '2026-01-01' },
      { id: 'new', channel_id: 'ch-1', created_at: '2026-02-01' }
    ] }, { channel_id: 'ch-2', logs: [{ id: 'other', channel_id: 'ch-2', created_at: '2026-03-01' }] }];
    await expect(store.listNotificationLogs({ channel_id: 'ch-1', limit: 1 })).resolves.toMatchObject([{ id: 'new' }]);
    expect(store.notificationState.logsLoading).toBe(false);
  });

  it('clears stale logs and exposes confidential read failures', async () => {
    topicMock.logs = [{ channel_id: 'ch-1', logs: [{ id: 'log-1' }] }];
    await store.listNotificationLogs();
    topicMock.error = new Error('unreadable log projection');
    await expect(store.listNotificationLogs()).rejects.toThrow('unreadable log projection');
    expect(store.notificationState.logs).toEqual([]);
    expect(store.notificationState.logsError).toBe('unreadable log projection');
  });
});
