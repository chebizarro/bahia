import { describe, expect, it } from 'vitest';
import {
  buildNotificationChannelPayload,
  createNotificationChannelForm,
  parseEventTypes
} from '../../src/routes/notifications/form-utils.js';

describe('notifications form utils', () => {
  it('builds webhook payloads with type-specific config and event types', () => {
    expect(buildNotificationChannelPayload({
      name: ' Deployments ',
      channel_type: 'webhook',
      enabled: true,
      webhook_url: ' https://hooks.example/bahia ',
      webhook_secret: ' secret ',
      webhook_headers: '{"X-Team":"platform"}',
      event_filter_mode: 'types',
      event_types: 'deployment.succeeded, deployment.failed\ndeployment.failed'
    })).toEqual({
      name: 'Deployments',
      channel_type: 'webhook',
      enabled: true,
      config: {
        url: 'https://hooks.example/bahia',
        secret: 'secret',
        headers: { 'X-Team': 'platform' }
      },
      event_filter: { types: ['deployment.succeeded', 'deployment.failed'] }
    });
  });

  it('builds nostr dm payloads with all-events filter', () => {
    const pubkey = 'A'.repeat(64);

    expect(buildNotificationChannelPayload({
      name: 'Ops DMs',
      channel_type: 'nostr_dm',
      enabled: false,
      nostr_pubkey: pubkey,
      event_filter_mode: 'all'
    })).toEqual({
      name: 'Ops DMs',
      channel_type: 'nostr_dm',
      enabled: false,
      config: { pubkey: pubkey.toLowerCase() },
      event_filter: {}
    });
  });

  it('hydrates edit form state from existing channels', () => {
    const form = createNotificationChannelForm({
      name: 'Existing hook',
      channel_type: 'webhook',
      enabled: false,
      config: { url: 'https://hooks.example', headers: { Authorization: 'Bearer token' } },
      event_filter: { types: ['test', 'deploy'] }
    });

    expect(form).toMatchObject({
      name: 'Existing hook',
      channel_type: 'webhook',
      enabled: false,
      webhook_url: 'https://hooks.example',
      event_filter_mode: 'types',
      event_types: 'test\ndeploy'
    });
    expect(JSON.parse(form.webhook_headers)).toEqual({ Authorization: 'Bearer token' });
  });

  it('validates required type-specific fields and JSON objects', () => {
    expect(() => buildNotificationChannelPayload({ name: '', channel_type: 'webhook' })).toThrow('Name is required');
    expect(() => buildNotificationChannelPayload({ name: 'Hook', channel_type: 'webhook', event_filter_mode: 'all' })).toThrow('Webhook URL is required');
    expect(() => buildNotificationChannelPayload({
      name: 'Hook',
      channel_type: 'webhook',
      webhook_url: 'https://hooks.example',
      webhook_headers: '{"X-Retry":3}',
      event_filter_mode: 'all'
    })).toThrow('Webhook header X-Retry must be a string');
    expect(() => buildNotificationChannelPayload({
      name: 'DM',
      channel_type: 'nostr_dm',
      nostr_pubkey: 'npub1...',
      event_filter_mode: 'all'
    })).toThrow('Recipient pubkey must be a 64-character hex public key');
    expect(() => buildNotificationChannelPayload({
      name: 'Hook',
      channel_type: 'webhook',
      webhook_url: 'https://hooks.example',
      event_filter_mode: 'json',
      event_filter_json: '[]'
    })).toThrow('Event filter must be a JSON object');
  });

  it('parses event types from comma or newline separated text', () => {
    expect(parseEventTypes('alpha, beta\nalpha\n gamma ')).toEqual(['alpha', 'beta', 'gamma']);
  });
});
