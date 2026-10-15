import { test, expect } from '@playwright/test';
import { E2E_SERVICE_PUBKEY, TEST_PUBKEY, installE2EMocks } from './helpers.js';
import { buildDaemonIntentStatus } from './intent-helpers.js';
import { intentStatusMatches } from '../../src/lib/nostr/intent-envelope.js';

test('backup-run refusals use only a private request-correlated status coordinate', () => {
  const intent = { id: 'backup-run-request', kind: 30900, pubkey: TEST_PUBKEY, created_at: 1,
    tags: [['d', 'backup-run:repository-one'], ['domain', 'backup'], ['intent_id', 'run-intent-one']] };
  const request = { domain: 'backup', coordinate: 'backup-run:repository-one', intentId: 'run-intent-one', eventId: intent.id };
  for (const status of ['rejected', 'conflict']) {
    const outcome = buildDaemonIntentStatus(intent, { servicePubkey: E2E_SERVICE_PUBKEY, status, reason: 'access_denied' });
    expect(outcome.tags).toContainEqual(['d', `intent-status:${TEST_PUBKEY}:backup:run-intent-one`]);
    expect(JSON.parse(outcome.content)).not.toHaveProperty('coordinate');
    expect(intentStatusMatches(outcome, TEST_PUBKEY, request)).toBe(true);
    const publicRefusal = { ...outcome, tags: outcome.tags.map(tag => tag[0] === 'd'
      ? ['d', `intent-status:${TEST_PUBKEY}:backup-run:repository-one`] : tag) };
    expect(intentStatusMatches(publicRefusal, TEST_PUBKEY, request)).toBe(false);
  }
  const accepted = buildDaemonIntentStatus(intent, { servicePubkey: E2E_SERVICE_PUBKEY });
  expect(accepted.tags).toContainEqual(['d', `intent-status:${TEST_PUBKEY}:backup-run:repository-one`]);
  expect(JSON.parse(accepted.content).coordinate).toBe(request.coordinate);
  expect(intentStatusMatches(accepted, TEST_PUBKEY, request)).toBe(true);
});

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
  await page.getByPlaceholder('Owning organization UUID').fill('f1e7f1e7-f1e7-51e7-a11e-f1e7f1e7f1e7');
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
