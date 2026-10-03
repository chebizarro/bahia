import { test, expect } from '@playwright/test';
import { E2E_SERVICE_PUBKEY, installE2EMocks } from './helpers.js';

test('backup repository intent stays pending until a scoped 30315 acceptance', async ({ page }) => {
  await installE2EMocks(page, { authenticated: true, extension: true, systemInfo: {
    nostr: { browser_relays: ['ws://relay.test.local'], service_pubkey: E2E_SERVICE_PUBKEY },
    features: { relay_sidecar: true, relay_read_models: true, encrypted_nostr_requests: true, legacy_sse: false }
  } });
  await page.goto('/backup/repositories');
  await page.evaluate(() => {
    window.__BAHIA_E2E_BACKUP_INTENTS = [];
    window.__BAHIA_E2E_NEXT_BACKUP_INTENT = new Promise(resolve => { window.__BAHIA_E2E_BACKUP_INTENT_READY = resolve; });
    const send = window.WebSocket.prototype.send;
    window.WebSocket.prototype.send = function captureIntent(data) {
      const message = JSON.parse(data);
      if (message[0] === 'EVENT' && message[1]?.kind === 30900
        && message[1]?.tags?.some(tag => tag[0] === 'domain' && tag[1] === 'backup')) {
        window.__BAHIA_E2E_BACKUP_INTENTS.push(message[1]);
        window.__BAHIA_E2E_BACKUP_INTENT_READY?.(message[1]);
        window.__BAHIA_E2E_BACKUP_INTENT_READY = null;
      }
      return send.call(this, data);
    };
  });

  await page.getByRole('button', { name: 'Register repository' }).first().click();
  await page.getByPlaceholder('primary-kopia').fill('archive');
  await page.getByPlaceholder('kopia://primary').fill('kopia://archive');
  await page.locator('.mutation-panel form').getByRole('button', { name: 'Register repository' }).click();

  await expect(page.getByTestId('backup-pending-intents')).toContainText('Pending');
  const intent = await page.evaluate(() => window.__BAHIA_E2E_NEXT_BACKUP_INTENT);
  expect(intent.tags).toEqual(expect.arrayContaining([
    ['domain', 'backup'], ['schema', 'bahia.intent.backup.v1'], ['op', 'repository-register'],
    ['org', 'f1e7f1e7-f1e7-51e7-a11e-f1e7f1e7f1e7']
  ]));
  expect(JSON.parse(intent.content)).toMatchObject({ name: 'archive', backend: 'kopia', repository_uri: 'kopia://archive' });
  await page.evaluate((intent) => {
    window.__bahiaPushNostrEvent(window.__BAHIA_E2E_MAKE_INTENT_STATUS(intent,
      { id: `accepted-${intent.tags.find(tag => tag[0] === 'intent_id')[1]}` }));
  }, intent);
  await expect(page.getByTestId('backup-pending-intents')).toHaveCount(0);
});
