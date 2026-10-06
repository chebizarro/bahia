import { onStoreRefresh } from '$lib/nostr/boot.js';
import { NOTIFICATION_CHANNEL_REGISTRY, NOTIFICATION_LOG_STATE, CP_STATE_TOPICS } from '$lib/nostr/kinds.gen.js';
import { acceptedIntentStatus } from '$lib/nostr/intent-client.svelte.js';
import { notificationChannelTestIntent } from '$lib/nostr/final-ops-intents.js';
import { onContentKeyChange } from './auth-roles.svelte.js';
import { readConfidentialTopic } from './collections/confidential-records.js';
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
  const orgId = orgIdFor(payload);
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
  const channel = notificationState.channels.find(row => row.id === id);
  if (!channel) throw new Error('Load the canonical notification channel before testing it');
  const submitted = await submitSensitiveIntent(notificationChannelTestIntent(id, orgIdFor(channel)));
  const status = await acceptedIntentStatus({ coordinate: id, intentId: submitted.intentId });
  return status.data || { status: 'accepted' };
}

export async function listNotificationLogs(params = {}) {
  notificationState.logsLoading = true;
  notificationState.logsError = null;
  try {
    const logs = readConfidentialTopic(CP_STATE_TOPICS.NOTIFICATION_LOG, NOTIFICATION_LOG_STATE).rows
      .filter(window => !params.channel_id || window.channel_id === params.channel_id)
      .flatMap(window => Array.isArray(window.logs) ? window.logs : [])
      .sort((a, b) => String(b.created_at || '').localeCompare(String(a.created_at || '')))
      .slice(0, Math.min(Math.max(Number(params.limit) || 50, 1), 500));
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
