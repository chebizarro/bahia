import { requestPrivateResult, privateTransportAvailable } from '$lib/nostr/private-controlplane.js';
import { currentSystemInfo, loadSystemInfo } from './system.svelte.js';

export const notificationState = $state({
  channels: [],
  channelsLoading: false,
  channelsError: null,
  logs: [],
  logsLoading: false,
  logsError: null
});

export const NOTIFICATION_PRIVATE_OPERATIONS = {
  listChannels: 'notifications.channels.list',
  getChannel: 'notifications.channels.get',
  createChannel: 'notifications.channels.create',
  updateChannel: 'notifications.channels.update',
  deleteChannel: 'notifications.channels.delete',
  testChannel: 'notifications.channels.test',
  listLogs: 'notifications.logs.list'
};

async function ensurePrivateNotificationsTransport() {
  let info = currentSystemInfo();
  if (!info) {
    info = await loadSystemInfo();
  }
  if (!privateTransportAvailable(info)) {
    throw new Error('Private Nostr transport is not available. Configure nostr.private_browser_relays and a Bahia service pubkey before using notification settings.');
  }
  return info;
}

function extractPrivatePayload(response, fallback = {}) {
  const envelope = response?.result ?? response;
  if (envelope?.status === 'error') {
    throw new Error(envelope?.error?.message || 'Private notification operation failed');
  }
  return envelope?.payload ?? fallback;
}

function normalizeChannelsPayload(payload) {
  if (Array.isArray(payload)) return payload;
  if (Array.isArray(payload?.channels)) return payload.channels;
  if (Array.isArray(payload?.data)) return payload.data;
  return [];
}

function normalizeLogsPayload(payload) {
  if (Array.isArray(payload)) return payload;
  if (Array.isArray(payload?.logs)) return payload.logs;
  if (Array.isArray(payload?.data)) return payload.data;
  return [];
}

function upsertChannel(channel) {
  if (!channel?.id) return;
  const index = notificationState.channels.findIndex((candidate) => candidate.id === channel.id);
  if (index === -1) {
    notificationState.channels = [channel, ...notificationState.channels];
    return;
  }
  notificationState.channels = notificationState.channels.map((candidate, i) => i === index ? { ...candidate, ...channel } : candidate);
}

export async function listNotificationChannels() {
  notificationState.channelsLoading = true;
  notificationState.channelsError = null;
  try {
    await ensurePrivateNotificationsTransport();
    const response = await requestPrivateResult({ operation: NOTIFICATION_PRIVATE_OPERATIONS.listChannels, payload: {} });
    const channels = normalizeChannelsPayload(extractPrivatePayload(response));
    notificationState.channels = channels;
    return channels;
  } catch (error) {
    notificationState.channels = [];
    notificationState.channelsError = error?.message || 'Failed to load notification channels';
    throw error;
  } finally {
    notificationState.channelsLoading = false;
  }
}

export async function getNotificationChannel(id) {
  await ensurePrivateNotificationsTransport();
  const response = await requestPrivateResult({ operation: NOTIFICATION_PRIVATE_OPERATIONS.getChannel, payload: { id } });
  const channel = extractPrivatePayload(response)?.channel ?? null;
  if (channel) upsertChannel(channel);
  return channel;
}

export async function createNotificationChannel(payload) {
  await ensurePrivateNotificationsTransport();
  const response = await requestPrivateResult({ operation: NOTIFICATION_PRIVATE_OPERATIONS.createChannel, payload });
  const channel = extractPrivatePayload(response)?.channel ?? null;
  if (channel) upsertChannel(channel);
  return channel;
}

export async function updateNotificationChannel(id, patch) {
  await ensurePrivateNotificationsTransport();
  const response = await requestPrivateResult({ operation: NOTIFICATION_PRIVATE_OPERATIONS.updateChannel, payload: { id, ...patch } });
  const channel = extractPrivatePayload(response)?.channel ?? null;
  if (channel) upsertChannel(channel);
  return channel;
}

export async function deleteNotificationChannel(id) {
  await ensurePrivateNotificationsTransport();
  const response = await requestPrivateResult({ operation: NOTIFICATION_PRIVATE_OPERATIONS.deleteChannel, payload: { id } });
  notificationState.channels = notificationState.channels.filter((channel) => channel.id !== id);
  return extractPrivatePayload(response);
}

export async function testNotificationChannel(id) {
  await ensurePrivateNotificationsTransport();
  const response = await requestPrivateResult({ operation: NOTIFICATION_PRIVATE_OPERATIONS.testChannel, payload: { id } });
  return extractPrivatePayload(response);
}

export async function listNotificationLogs(params = {}) {
  notificationState.logsLoading = true;
  notificationState.logsError = null;
  try {
    await ensurePrivateNotificationsTransport();
    const response = await requestPrivateResult({ operation: NOTIFICATION_PRIVATE_OPERATIONS.listLogs, payload: params });
    const logs = normalizeLogsPayload(extractPrivatePayload(response));
    notificationState.logs = logs;
    return logs;
  } catch (error) {
    notificationState.logs = [];
    notificationState.logsError = error?.message || 'Failed to load notification log';
    throw error;
  } finally {
    notificationState.logsLoading = false;
  }
}

export function resetNotificationStore() {
  notificationState.channels = [];
  notificationState.channelsLoading = false;
  notificationState.channelsError = null;
  notificationState.logs = [];
  notificationState.logsLoading = false;
  notificationState.logsError = null;
}
