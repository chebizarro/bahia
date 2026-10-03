import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';
import {
  KIND_GIFT_WRAP,
  createEncryptedNotificationsSystemInfo,
  installEncryptedNotificationHarness
} from './harnesses/notifications-encrypted.js';

const systemInfo = createEncryptedNotificationsSystemInfo();
const createError = {
  code: 'handler_failed',
  message: 'failed to create notification channel'
};
const updateError = {
  code: 'handler_failed',
  message: 'failed to update notification channel'
};
const existingChannel = {
  id: 'ch-1',
  org_id: '0199c749-9300-7444-8444-444444444444',
  name: 'Ops Webhook',
  channel_type: 'webhook',
  config: { url: 'https://hooks.example.com/ops' },
  event_filter: { types: ['deployment.failed'] },
  enabled: true,
  created_at: '2026-05-03T10:00:00.000Z',
  updated_at: '2026-05-03T10:00:00.000Z'
};

async function installFailureHarness(page, { initialChannels = [], operationErrors = {} } = {}) {
  await installE2EMocks(page, { systemInfo });
  await installEncryptedNotificationHarness(page, {
    initialChannels,
    initialLogs: [],
    operationErrors
  });
}

test.describe('Notifications intent failures and accessibility', () => {
  test('submits a create intent and reports a rejected 30315 rather than an RPC error', async ({ page }) => {
    await installFailureHarness(page, {
      initialChannels: [existingChannel],
      operationErrors: {
        'notification.intent.create': createError
      }
    });

    await page.goto('/notifications');
    await expect(page.getByText('Ops Webhook')).toBeVisible();
    await page.getByRole('button', { name: 'Create channel' }).click();

    await expect(page.getByRole('heading', { name: 'Create notification channel' })).toBeVisible();

    const nameInput = page.getByLabel('Name *');
    const webhookUrlInput = page.getByLabel('Webhook URL *');

    await nameInput.fill('PagerDuty Webhook');
    await webhookUrlInput.fill('https://hooks.example.com/pagerduty');
    await page.locator('form').getByRole('button', { name: 'Create channel' }).click();

    await expect(page).toHaveURL(/\/notifications$/);
    await expect.poll(() => page.evaluate(() => window.__BAHIA_E2E_INTENT_STATUS_EVENTS.at(-1)?.tags))
      .toEqual(expect.arrayContaining([['status', 'rejected'], ['reason', createError.message]]));
    await expect(page.getByRole('status')).toContainText(createError.message);
    expect(await page.evaluate(() => window.__BAHIA_E2E_INTENT_WRAPS.at(-1)?.outer.kind)).toBe(KIND_GIFT_WRAP);
  });

  test('submits an update intent and reports a rejected 30315', async ({ page }) => {
    await installFailureHarness(page, {
      initialChannels: [existingChannel],
      operationErrors: {
        'notification.intent.update': updateError
      }
    });

    await page.goto('/notifications/ch-1/edit');

    await expect(page.getByRole('heading', { name: 'Edit notification channel' })).toBeVisible();

    const nameInput = page.getByLabel('Name *');
    const webhookUrlInput = page.getByLabel('Webhook URL *');

    await expect(nameInput).toHaveValue('Ops Webhook');
    await expect(webhookUrlInput).toHaveValue('https://hooks.example.com/ops');

    await nameInput.fill('Ops Webhook Updated');
    await webhookUrlInput.fill('https://hooks.example.com/ops-updated');
    await page.locator('form').getByRole('button', { name: 'Save channel' }).click();

    await expect(page).toHaveURL(/\/notifications$/);
    await expect.poll(() => page.evaluate(() => window.__BAHIA_E2E_INTENT_STATUS_EVENTS.at(-1)?.tags))
      .toEqual(expect.arrayContaining([['status', 'rejected'], ['reason', updateError.message]]));
  });

  test('exposes labelled controls, headings, and alert regions on notifications routes', async ({ page }) => {
    await installFailureHarness(page, {
      initialChannels: [existingChannel],
      operationErrors: {
        'notification.intent.create': createError
      }
    });

    await page.goto('/notifications');

    await expect(page.getByRole('heading', { name: 'Notifications' })).toBeVisible();
    await expect(page.getByText('Ops Webhook')).toBeVisible();
    await expect(page.getByLabel('Status')).toBeVisible();
    await expect(page.getByLabel('Channel type')).toBeVisible();
    await expect(page.getByLabel('Search channels')).toBeVisible();

    await page.locator('.header-actions').getByRole('button', { name: 'Create channel' }).click();
    await expect(page.getByRole('heading', { name: 'Create notification channel' })).toBeVisible();
    await expect(page.getByLabel('Name *')).toBeVisible();
    await expect(page.getByLabel('Channel type *')).toBeVisible();
    await expect(page.getByLabel('Webhook URL *')).toBeVisible();
    await expect(page.getByLabel('Signing secret')).toBeVisible();
    await expect(page.getByLabel('Custom headers JSON')).toBeVisible();
    await expect(page.getByLabel('Filter mode')).toBeVisible();

    await page.getByLabel('Name *').fill('PagerDuty Webhook');
    await page.getByLabel('Webhook URL *').fill('https://hooks.example.com/pagerduty');
    await page.locator('form').getByRole('button', { name: 'Create channel' }).click();

    await expect(page).toHaveURL(/\/notifications$/);
    await expect.poll(() => page.evaluate(() => window.__BAHIA_E2E_INTENT_STATUS_EVENTS.at(-1)?.tags))
      .toEqual(expect.arrayContaining([['status', 'rejected']]));
  });
});
