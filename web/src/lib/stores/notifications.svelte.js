import { requestEncryptedResult, encryptedRequestsAvailable } from '$lib/nostr/encrypted-controlplane.js';
import { onStoreRefresh } from '$lib/nostr/boot.js';
import { NOTIFICATION_CHANNEL_REGISTRY } from '$lib/nostr/kinds.gen.js';
import { onContentKeyChange } from './auth-roles.svelte.js';
import { readConfidentialTopic } from './collections/confidential-records.js';
import { currentSystemInfo, loadSystemInfo } from './system.svelte.js';
import { mintEntityId } from '$lib/entity-id.js';
import { orgIdFor, submitSensitiveIntent } from './sensitive-intents.svelte.js';

export const notificationState = $state({
  channels: [],
  channelsLoading: false,
  channelsError: null,
  logs: [],
  logsLoading: false,
  logsError: null
});

let notificationSubscription = null;
let keySubscription = null;
let subscribedLogParams = null;

export const NOTIFICATION_ENCRYPTED_OPERATIONS = {
  testChannel: 'notifications.channels.test',
  listLogs: 'notifications.logs.list'
};

async function ensureEncryptedNotifications() {
  let info = currentSystemInfo();
  if (!info) {
    info = await loadSystemInfo();
  }
  if (!encryptedRequestsAvailable(info)) {
    throw new Error('ContextVM requests are not available. Ensure Bahia discovery advertises standard relay URLs and a Bahia service pubkey before using notification settings.');
  }
  return info;
}

function extractEncryptedPayload(response, fallback = {}) {
  const envelope = response?.result ?? response;
  if (envelope?.status === 'error') {
    throw new Error(envelope?.error?.message || 'Encrypted notification operation failed');
  }
  return envelope?.payload ?? fallback;
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
    const channels = readConfidentialTopic('notification-channel', NOTIFICATION_CHANNEL_REGISTRY).rows;
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
  await listNotificationChannels();
  return notificationState.channels.find((channel) => channel.id === id) || null;
}

export async function createNotificationChannel(payload) {
  const id = mintEntityId();
  const orgId = orgIdFor(payload, notificationState.channels);
  const intent = await submitSensitiveIntent({ domain: 'notification', op: 'create', coordinate: id, orgId,
    content: { ...payload, id, org_id: orgId } });
  const channel = { ...payload, id, org_id: orgId, pending: true, pendingIntentId: intent.intentId };
  upsertChannel(channel);
  return channel;
}

export async function updateNotificationChannel(id, patch) {
  const current = notificationState.channels.find(channel => channel.id === id);
  if (!current) throw new Error('Load the canonical notification channel before updating it');
  const channel = { ...current, ...patch, id };
  const intent = await submitSensitiveIntent({ domain: 'notification', op: 'update', coordinate: id,
    orgId: orgIdFor(channel), content: channel, currentRecord: current });
  upsertChannel({ ...channel, pending: true, pendingIntentId: intent.intentId });
  return { ...channel, pending: true, pendingIntentId: intent.intentId };
}

export async function deleteNotificationChannel(id) {
  const current = notificationState.channels.find(channel => channel.id === id);
  const intent = await submitSensitiveIntent({ domain: 'notification', op: 'delete', coordinate: id,
    orgId: orgIdFor(current), content: { id } });
  upsertChannel({ ...current, id, pending: true, pendingDelete: true, pendingIntentId: intent.intentId });
  return { id, pending: true };
}

export async function testNotificationChannel(id) {
  await ensureEncryptedNotifications();
  const response = await requestEncryptedResult({ operation: NOTIFICATION_ENCRYPTED_OPERATIONS.testChannel, payload: { id } });
  return extractEncryptedPayload(response);
}

export async function listNotificationLogs(params = {}) {
  notificationState.logsLoading = true;
  notificationState.logsError = null;
  try {
    await ensureEncryptedNotifications();
    const response = await requestEncryptedResult({ operation: NOTIFICATION_ENCRYPTED_OPERATIONS.listLogs, payload: params });
    const logs = normalizeLogsPayload(extractEncryptedPayload(response));
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

export function unsubscribeFromNotificationUpdates() {
  notificationSubscription?.();
  keySubscription?.();
  notificationSubscription = null;
  keySubscription = null;
  subscribedLogParams = null;
}

export async function refreshNotificationStore({ logParams = subscribedLogParams } = {}) {
  const requests = [listNotificationChannels()];
  if (logParams) requests.push(listNotificationLogs(logParams));
  await Promise.all(requests);
  return notificationState;
}

export async function subscribeToNotificationUpdates({ logParams = null } = {}) {
  subscribedLogParams = logParams ? { ...logParams } : null;
  if (!notificationSubscription) notificationSubscription = onStoreRefresh(() => { void listNotificationChannels(); });
  if (!keySubscription) keySubscription = onContentKeyChange(() => { void listNotificationChannels(); });
  await refreshNotificationStore();
  return unsubscribeFromNotificationUpdates;
}

export function resetNotificationStore() {
  unsubscribeFromNotificationUpdates();
  notificationState.channels = [];
  notificationState.channelsLoading = false;
  notificationState.channelsError = null;
  notificationState.logs = [];
  notificationState.logsLoading = false;
  notificationState.logsError = null;
}
