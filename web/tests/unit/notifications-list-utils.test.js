import { describe, expect, it } from 'vitest';
import {
  channelDestination,
  channelLabel,
  channelTypeLabel,
  eventFilterSummary,
  filterChannels,
  filterNotificationLogs,
  getChannelTypeOptions,
  getNotificationLogChannelOptions,
  getNotificationLogEventTypeOptions,
  normalizeChannels,
  normalizeNotificationLogs,
  rawChannelDestination,
  truncateMiddle
} from '../../src/routes/notifications/list-utils.js';

describe('notifications list utils', () => {
  const channels = [
    {
      id: 'channel-webhook-alpha',
      name: 'Deployments webhook',
      channel_type: 'webhook',
      config: { url: 'https://hooks.example/deployments' },
      event_filter: { types: ['deployment.succeeded', 'deployment.failed'] },
      enabled: true
    },
    {
      id: 'channel-nostr-beta',
      name: 'Ops Nostr DM',
      channel_type: 'nostr_dm',
      config: { pubkey: '1234567890abcdef1234567890abcdef' },
      event_filter: { type: '*' },
      enabled: false
    }
  ];

  it('normalizes common response shapes', () => {
    expect(normalizeChannels(channels)).toEqual(channels);
    expect(normalizeChannels({ channels })).toEqual(channels);
    expect(normalizeChannels({ data: channels })).toEqual(channels);
    expect(normalizeChannels(null)).toEqual([]);
    expect(() => normalizeChannels({ unexpected: channels })).toThrow('Unexpected notification channel response');
  });

  it('builds stable display labels', () => {
    expect(channelTypeLabel('webhook')).toBe('Webhook');
    expect(channelTypeLabel('nostr_dm')).toBe('Nostr DM');
    expect(truncateMiddle('1234567890abcdef', 4, 4)).toBe('1234...cdef');
    expect(channelDestination(channels[0])).toBe('https://hooks.example/deployments');
    expect(rawChannelDestination(channels[1])).toBe('1234567890abcdef1234567890abcdef');
    expect(channelDestination(channels[1])).toBe('1234567890abcdef...90abcdef');
    expect(eventFilterSummary(channels[0].event_filter)).toBe('deployment.succeeded, deployment.failed');
    expect(eventFilterSummary({ type: '*' })).toBe('All events');
    expect(eventFilterSummary({})).toBe('All events');
  });

  it('builds sorted channel type options', () => {
    expect(getChannelTypeOptions(channels)).toEqual([
      { value: 'nostr_dm', label: 'Nostr DM' },
      { value: 'webhook', label: 'Webhook' }
    ]);
  });

  it('filters by enabled state, type, and search text', () => {
    expect(filterChannels(channels, { status: 'enabled' }).map((channel) => channel.id)).toEqual(['channel-webhook-alpha']);
    expect(filterChannels(channels, { status: 'disabled' }).map((channel) => channel.id)).toEqual(['channel-nostr-beta']);
    expect(filterChannels(channels, { type: 'nostr_dm' }).map((channel) => channel.id)).toEqual(['channel-nostr-beta']);
    expect(filterChannels(channels, { search: 'deployment.failed' }).map((channel) => channel.id)).toEqual(['channel-webhook-alpha']);
    expect(filterChannels(channels, { search: '90abcdef1234' }).map((channel) => channel.id)).toEqual(['channel-nostr-beta']);
    expect(filterChannels(channels, { search: 'ops' }).map((channel) => channel.id)).toEqual(['channel-nostr-beta']);
    expect(filterChannels(channels, { status: 'enabled', search: 'ops' })).toEqual([]);
  });

  describe('notification log utilities', () => {
    const logs = [
      {
        id: 'log-1',
        channel_id: 'channel-webhook-alpha',
        event_type: 'deployment.succeeded',
        status: 'sent'
      },
      {
        id: 'log-2',
        channel_id: 'channel-nostr-beta',
        event_type: 'deployment.failed',
        status: 'retrying'
      },
      {
        id: 'log-3',
        channel_id: 'deleted-channel-gamma',
        event_type: 'billing.payment_failed',
        status: 'failed'
      }
    ];

    it('normalizes common log response shapes', () => {
      expect(normalizeNotificationLogs(logs)).toEqual(logs);
      expect(normalizeNotificationLogs({ logs })).toEqual(logs);
      expect(normalizeNotificationLogs({ data: logs })).toEqual(logs);
      expect(normalizeNotificationLogs(null)).toEqual([]);
      expect(() => normalizeNotificationLogs({ unexpected: logs })).toThrow('Unexpected notification log response');
    });

    it('builds channel labels and sorted filter options from logs', () => {
      expect(channelLabel('channel-webhook-alpha', channels)).toBe('Deployments webhook');
      expect(channelLabel('deleted-channel-gamma', channels)).toBe('deleted-...-gamma');
      expect(getNotificationLogChannelOptions(logs, channels)).toEqual([
        { value: 'deleted-channel-gamma', label: 'deleted-...-gamma' },
        { value: 'channel-webhook-alpha', label: 'Deployments webhook' },
        { value: 'channel-nostr-beta', label: 'Ops Nostr DM' }
      ]);
    });

    it('builds event type filter options from logs', () => {
      expect(getNotificationLogEventTypeOptions(logs)).toEqual([
        { value: 'billing.payment_failed', label: 'billing.payment_failed' },
        { value: 'deployment.failed', label: 'deployment.failed' },
        { value: 'deployment.succeeded', label: 'deployment.succeeded' }
      ]);
    });

    it('filters logs by channel and event type only', () => {
      expect(filterNotificationLogs(logs, { channel: 'channel-webhook-alpha' }).map((log) => log.id)).toEqual(['log-1']);
      expect(filterNotificationLogs(logs, { eventType: 'deployment.failed' }).map((log) => log.id)).toEqual(['log-2']);
      expect(filterNotificationLogs(logs, { channel: 'channel-nostr-beta', eventType: 'deployment.failed' }).map((log) => log.id)).toEqual(['log-2']);
      expect(filterNotificationLogs(logs, { channel: 'channel-webhook-alpha', eventType: 'deployment.failed' })).toEqual([]);
    });
  });
});
