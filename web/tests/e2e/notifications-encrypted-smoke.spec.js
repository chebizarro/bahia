import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';
import {
  ENCRYPTED_RELAY,
  KIND_GIFT_WRAP,
  createEncryptedNotificationsSystemInfo,
  installEncryptedNotificationHarness,
  notificationRelayFixtures
} from './harnesses/notifications-encrypted.js';

const now = new Date().toISOString();

const systemInfo = createEncryptedNotificationsSystemInfo();

const initialChannels = [
  {
    id: 'ch-1',
    org_id: '0199c749-9300-7444-8444-444444444444',
    name: 'Ops Webhook',
    channel_type: 'webhook',
    config: { url: 'https://hooks.example.com/ops' },
    event_filter: { types: ['deployment.failed'] },
    enabled: true,
    created_at: now,
    updated_at: now
  }
];

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page, { systemInfo, nostrEvents: notificationRelayFixtures(initialChannels) });
  await installEncryptedNotificationHarness(page, { initialChannels, initialLogs: [] });
});

test.describe('Notifications encrypted transport smoke', () => {
  test('channel create uses a 1059 intent; reads and test-channel stay interactive', async ({ page }) => {
    await page.goto('/notifications');

    await expect(page.getByRole('heading', { name: 'Notifications' })).toBeVisible();
    await expect(page.getByText('Ops Webhook')).toBeVisible();
    await expect(page.getByText('https://hooks.example.com/ops')).toBeVisible();

    await page.getByRole('button', { name: 'Create channel' }).click();
    await expect(page).toHaveURL(/\/notifications\/new$/);
    await expect(page.getByRole('heading', { name: 'Create notification channel' })).toBeVisible();

    await page.locator('#notification-channel-name').fill('PagerDuty Webhook');
    await page.locator('#webhook-url').fill('https://hooks.example.com/pagerduty');
    await page.locator('form').getByRole('button', { name: 'Create channel' }).click();

    await expect(page).toHaveURL(/\/notifications$/);
    const row = page.locator('tr', { hasText: 'PagerDuty Webhook' });
    await expect(row).toBeVisible();
    await expect(page.getByText('PagerDuty Webhook created')).toBeVisible();

    await row.getByRole('button', { name: 'Test' }).click();
    await expect(page.getByText('Test notification sent to PagerDuty Webhook')).toBeVisible();

    const normalizeRelay = (relay) => String(relay || '').replace(/\/$/, '');
    const transportTrace = await page.evaluate(() => ({
      relays: window.__BAHIA_E2E_ENCRYPTED_WIRE_PUBLISHES.map((entry) => entry.relay),
      operations: [...window.__BAHIA_E2E_ENCRYPTED_OPERATIONS],
      intentWraps: window.__BAHIA_E2E_INTENT_WRAPS,
      statuses: window.__BAHIA_E2E_INTENT_STATUS_EVENTS
    }));

    const normalizedRelays = transportTrace.relays.map(normalizeRelay);
    expect(normalizedRelays).toHaveLength(2);
    expect(normalizedRelays.every((relay) => relay === ENCRYPTED_RELAY)).toBe(true);
    expect(transportTrace.operations).toEqual(expect.arrayContaining([
      'notification.intent.create',
      'notification.intent.channel-test'
    ]));
    expect(transportTrace.intentWraps).toHaveLength(2);
    expect(transportTrace.intentWraps.map(({ outer, inner }) => ({ kind: outer.kind,
      domain: inner.tags.find(tag => tag[0] === 'domain')?.[1], op: inner.tags.find(tag => tag[0] === 'op')?.[1] })))
      .toEqual([{ kind: KIND_GIFT_WRAP, domain: 'notification', op: 'create' },
        { kind: KIND_GIFT_WRAP, domain: 'notification', op: 'channel-test' }]);
    expect(transportTrace.statuses).toHaveLength(2);
    expect(transportTrace.statuses.every(status => status.tags.some(tag => tag[0] === 'status' && tag[1] === 'accepted'))).toBe(true);
  });
});
