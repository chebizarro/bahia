// Regression for bahia-ncjun: a mutation on the gift-wrapped (sensitive)
// transport resolves its organization through the same readiness contract as
// every other signed intent. While the session is still deriving membership
// (the async OCK trial-decrypt), the control is disabled with the pending
// reason; it never reports "Select an organization" for a state that resolves
// by itself, and it submits once the organization is known locally.
//
// The test holds the key-envelope decrypt until it releases it, so the
// resolving state is observed deterministically; CPU throttling reproduces the
// slow runners that first exposed the notice. Nothing here depends on speed.
import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';
import {
  KIND_GIFT_WRAP,
  createEncryptedNotificationsSystemInfo,
  installEncryptedNotificationHarness,
  notificationRelayFixtures
} from './harnesses/notifications-encrypted.js';

const CPU_THROTTLING_RATE = 6;
const ORG_ID = '0199c749-9300-7444-8444-444444444444';
const CONNECTING = 'Connecting…';
const now = new Date().toISOString();

const initialChannels = [{
  id: 'ch-1', org_id: ORG_ID, name: 'Ops Webhook', channel_type: 'webhook',
  config: { url: 'https://hooks.example.com/ops' }, event_filter: { types: ['deployment.failed'] },
  enabled: true, created_at: now, updated_at: now
}];

async function throttleCPU(page) {
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Emulation.setCPUThrottlingRate', { rate: CPU_THROTTLING_RATE });
}

/**
 * Make the signer's NIP-44 decrypt of the organization key envelope wait until
 * window.__releaseRoleDerivation() runs. Role derivation stays active, with
 * no organization known, for exactly that long. Install after the harness
 * that defines window.nostr.nip44.
 */
async function holdRoleDerivation(page) {
  await page.addInitScript(() => {
    let release;
    const released = new Promise(resolve => { release = resolve; });
    window.__releaseRoleDerivation = () => release();
    const provider = window.nostr?.nip44;
    const decrypt = provider.decrypt.bind(provider);
    provider.decrypt = async (sender, ciphertext) => {
      if (String(ciphertext).includes('bahia.ock-wrap.v1')) await released;
      return decrypt(sender, ciphertext);
    };
  });
}

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page, { systemInfo: createEncryptedNotificationsSystemInfo(),
    nostrEvents: notificationRelayFixtures(initialChannels) });
  await installEncryptedNotificationHarness(page, { initialChannels, initialLogs: [] });
  await holdRoleDerivation(page);
  await throttleCPU(page);
});

test('notification channel create waits for membership to be derived, then publishes the gift wrap', async ({ page }) => {
  await page.goto('/notifications/new');
  await expect(page.getByRole('heading', { name: 'Create notification channel' })).toBeVisible();

  const form = page.locator('form');
  const submit = form.getByRole('button', { name: 'Create channel' });
  const gate = form.locator('fieldset[data-intent-domain="notification"]');

  // Only the submitting control waits; the form can be filled in meanwhile.
  await page.locator('#notification-channel-name').fill('PagerDuty Webhook');
  await page.locator('#webhook-url').fill('https://hooks.example.com/pagerduty');

  // Membership is in the local store and being decrypted: the control is
  // disabled with the pending reason, next to it and to assistive technology.
  await expect(gate).toHaveAccessibleDescription(CONNECTING);
  await expect(gate.getByText(CONNECTING)).toBeVisible();
  await expect(submit).toBeDisabled();
  await expect(gate).toHaveAttribute('data-intent-ready', 'false');
  await expect(page.getByText(/Select an organization/)).toHaveCount(0);
  await expect(form.getByRole('alert')).toHaveCount(0);

  // A user who clicks now waits for the control instead of getting a failure.
  const clicked = submit.click();
  await page.evaluate(() => window.__releaseRoleDerivation());
  await clicked;

  await expect(page).toHaveURL(/\/notifications$/);
  await expect(page.locator('tr', { hasText: 'PagerDuty Webhook' })).toBeVisible();
  await expect(page.getByText('PagerDuty Webhook created')).toBeVisible();
  await expect(page.getByText(/Select an organization/)).toHaveCount(0);

  await expect.poll(() => page.evaluate(() => window.__BAHIA_E2E_INTENT_WRAPS.map(({ outer, inner }) => ({
    kind: outer.kind,
    domain: inner.tags.find(tag => tag[0] === 'domain')?.[1],
    op: inner.tags.find(tag => tag[0] === 'op')?.[1],
    org: inner.tags.find(tag => tag[0] === 'org')?.[1],
    orgId: JSON.parse(inner.content).org_id
  })))).toEqual([{ kind: KIND_GIFT_WRAP, domain: 'notification', op: 'create', org: ORG_ID, orgId: ORG_ID }]);
});

test('a channel the session already reads is ready to change as soon as it is on screen', async ({ page }) => {
  await page.goto('/notifications');
  // Reading the channel needs the organization key, so releasing the decrypt
  // is what puts the row on screen; its org id then makes the control ready
  // without waiting on anything else.
  await page.evaluate(() => window.__releaseRoleDerivation());
  const row = page.locator('tr', { hasText: 'Ops Webhook' });
  await expect(row).toBeVisible();
  const gate = row.locator('fieldset[data-intent-domain="notification"]').first();
  await expect(gate).toHaveAttribute('data-intent-ready', 'true');
  await expect(page.getByText(/Select an organization/)).toHaveCount(0);

  await row.getByRole('button', { name: 'Disable' }).click();
  await expect(page.getByText('Ops Webhook disabled')).toBeVisible();
  // The outbox delivers the wrap to the relay after the store has accepted it.
  await expect.poll(() => page.evaluate(() => window.__BAHIA_E2E_INTENT_WRAPS.map(({ inner }) => ({
    domain: inner.tags.find(tag => tag[0] === 'domain')?.[1],
    op: inner.tags.find(tag => tag[0] === 'op')?.[1],
    org: inner.tags.find(tag => tag[0] === 'org')?.[1]
  })))).toEqual([{ domain: 'notification', op: 'update', org: ORG_ID }]);
});
