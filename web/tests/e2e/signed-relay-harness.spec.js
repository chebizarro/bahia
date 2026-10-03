import { test, expect } from '@playwright/test';
import { getEventHash, nip44, verifyEvent } from 'nostr-tools';
import { cpStateFixture } from './cp-state-fixtures.js';
import { RELAY_OPERATOR_PUBKEY, RELAY_SERVICE_PUBKEY, e2eSecretKeyForPubkey } from './e2e-keyring.js';
import { observeSignedIntent, publishDaemonOutcome, publishFixtureEvent } from './intent-helpers.js';
import { membershipFixtureEvents } from './membership-fixtures.js';
import { installRelayBackedBrowserContext, startBahiaTestRelay } from './relay-harness.js';

let relay;

test.beforeEach(async () => {
  relay = await startBahiaTestRelay();
});

test.afterEach(async () => { await relay?.stop(); });

test('cached service renders after the relay goes offline, without a backend gate', async ({ page }) => {
  await installRelayBackedBrowserContext(page, relay);
  await page.goto('/services');
  await expect(page.getByText('Checkout API')).toBeVisible();
  await relay.stop();
  relay = null;
  await page.reload();
  await expect(page.getByText('Checkout API')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Services', exact: true })).toBeVisible();
  expect(await page.locator('body').innerText()).not.toContain('Checking authentication');
});

test('service create publishes a signed intent and resolves only after daemon status', async ({ page }) => {
  await installRelayBackedBrowserContext(page, relay);
  await page.goto('/services');
  await expect(page.getByText('Checkout API')).toBeVisible();
  const observer = await observeSignedIntent(relay, { domain: 'service' });
  try {
    await page.getByRole('button', { name: 'Create Service' }).first().click();
    const dialog = page.getByRole('dialog', { name: 'Create Service' });
    const orgField = dialog.locator('#service-org-id');
    if (await orgField.evaluate(element => element.tagName) === 'SELECT') {
      await orgField.selectOption({ index: 1 });
    } else {
      await orgField.fill('0199c749-9300-7444-8444-444444444444');
    }
    await dialog.locator('#service-name').fill('signed-relay-service');
    await dialog.locator('#artifact-repo-path').fill('ghcr.io/test/signed-relay-service');
    await dialog.getByRole('button', { name: 'Create', exact: true }).click();
    await expect(dialog).not.toBeVisible();

    const intent = await observer.event;
    expect(verifyEvent(intent)).toBe(true);
    expect(intent.pubkey).toBe(RELAY_OPERATOR_PUBKEY);
    expect(intent.tags).toContainEqual(['domain', 'service']);
    const row = page.getByRole('row', { name: /signed-relay-service/ });
    await expect(row).toContainText(/pending \d+ s/);

    const content = JSON.parse(intent.content);
    const coordinate = intent.tags.find(tag => tag[0] === 'd')[1];
    await publishDaemonOutcome(relay, intent, { canonical: cpStateFixture({
      pubkey: RELAY_SERVICE_PUBKEY, schema: 'bahia.registry.service.v1', d: coordinate,
      content: { ...content, name: 'signed-relay-service', deleted: false }
    }) });
    await expect(row).toContainText('confirmed');
  } finally {
    observer.close();
  }
});

test('service-signed OCK envelope and member record derive the operator role', async ({ page }) => {
  await installRelayBackedBrowserContext(page, relay, { roleOverride: false });
  const { keyEnvelope, member } = membershipFixtureEvents({
    orgID: 'org-crypto-e2e', memberPubkey: RELAY_OPERATOR_PUBKEY,
    servicePubkey: RELAY_SERVICE_PUBKEY, role: 'owner'
  });
  await publishFixtureEvent(relay, keyEnvelope);
  await publishFixtureEvent(relay, member);
  await page.goto('/settings');
  await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toBeVisible();
  await expect(async () => {
    expect(await page.evaluate(async () => {
      const roles = await import('/src/lib/stores/auth-roles.svelte.js');
      return roles.roleForOrg('org-crypto-e2e');
    })).toBe('owner');
  }).toPass();
  expect(await page.evaluate(() => window.__BAHIA_E2E_USER_ROLES)).toBeUndefined();
});

test('NIP-59 intent has a verified gift wrap, seal, and bound rumor', async ({ page }) => {
  await installRelayBackedBrowserContext(page, relay);
  await page.goto('/services');
  const wrapped = await page.evaluate(async ({ servicePubkey }) => {
    const { giftWrapIntent } = await import('/src/lib/nostr/intent-giftwrap.js');
    const inner = await window.nostr.signEvent({ kind: 30900, created_at: Math.floor(Date.now() / 1000),
      tags: [['d', 'org:crypto-e2e'], ['domain', 'org'], ['schema', 'bahia.intent.org.v1'],
        ['t', 'bahia-intent'], ['op', 'update'], ['org', 'org-crypto-e2e'], ['intent_id', crypto.randomUUID()]],
      content: JSON.stringify({ id: 'org-crypto-e2e', name: 'Crypto E2E' }) });
    const signer = { getPublicKey: window.nostr.getPublicKey, signEvent: window.nostr.signEvent,
      encryptNip44: window.nostr.nip44.encrypt };
    return { inner, outer: await giftWrapIntent(inner, servicePubkey, signer) };
  }, { servicePubkey: relay.servicePubkey });
  expect(verifyEvent(wrapped.inner)).toBe(true);
  expect(verifyEvent(wrapped.outer)).toBe(true);
  const decryptFrom = (pubkey, content) => nip44.v2.decrypt(content,
    nip44.v2.utils.getConversationKey(e2eSecretKeyForPubkey(RELAY_SERVICE_PUBKEY), pubkey));
  const seal = JSON.parse(decryptFrom(wrapped.outer.pubkey, wrapped.outer.content));
  expect(verifyEvent(seal)).toBe(true);
  const rumor = JSON.parse(decryptFrom(seal.pubkey, seal.content));
  expect(getEventHash(rumor)).toBe(wrapped.inner.id);
  expect(rumor.pubkey).toBe(RELAY_OPERATOR_PUBKEY);
});
