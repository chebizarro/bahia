import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';
import {
  PUBLIC_RELAY,
  ENCRYPTED_RELAY,
  createEncryptedNotificationsSystemInfo,
  installEncryptedNotificationHarness
} from './harnesses/notifications-encrypted.js';

const now = new Date().toISOString();

const systemInfo = createEncryptedNotificationsSystemInfo();

const initialChannels = [
  {
    id: 'ch-1',
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
  await installE2EMocks(page, { systemInfo });
  await installEncryptedNotificationHarness(page, { initialChannels, initialLogs: [] });
});

test.describe('Notifications encrypted transport smoke', () => {
  test('browser notifications flow uses encrypted request/result transport end-to-end', async ({ page }) => {
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

    const transportTrace = await page.evaluate(() => ({
      relays: window.__BAHIA_E2E_ENCRYPTED_PUBLISHES.map((entry) => entry.relay),
      requests: window.__BAHIA_E2E_ENCRYPTED_REQUESTS,
      oks: window.__BAHIA_E2E_ENCRYPTED_OKS,
      results: window.__BAHIA_E2E_ENCRYPTED_RESULTS,
      operations: [...window.__BAHIA_E2E_ENCRYPTED_OPERATIONS]
    }));

    expect(transportTrace.relays.length).toBeGreaterThanOrEqual(3);
    expect(transportTrace.relays.every((relay) => relay === ENCRYPTED_RELAY)).toBe(true);
    expect(transportTrace.relays.some((relay) => relay === PUBLIC_RELAY)).toBe(false);
    expect(transportTrace.operations).toEqual(expect.arrayContaining([
      'notifications.channels.list',
      'notifications.channels.create',
      'notifications.channels.test'
    ]));

    for (const request of transportTrace.requests) {
      expect(request.kind).toBe(5980);
      expect(request.relay).toBe(ENCRYPTED_RELAY);
      expect(request.relay).not.toBe(PUBLIC_RELAY);
      expect(transportTrace.oks).toEqual(expect.arrayContaining([
        expect.objectContaining({ eventId: request.eventId, kind: 5980, accepted: true })
      ]));
      expect(transportTrace.results).toEqual(expect.arrayContaining([
        expect.objectContaining({ requestEventId: request.eventId, kind: 7980, pubkey: 'b'.repeat(64), status: 'ok' })
      ]));
    }
  });
});
