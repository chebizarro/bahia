import { readFileSync } from 'node:fs';
import { test, expect } from '@playwright/test';
import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';
import { verifyEvent } from 'nostr-tools';
import { base64Encode, CONFIDENTIAL_SCHEMA, CONFIDENTIAL_ALGORITHM } from '../../src/lib/nostr/confidential.js';
import { confidentialCpStateFixture } from './cp-state-fixtures.js';
import { E2E_SERVICE_PUBKEY, TEST_PUBKEY, installE2EMocks } from './helpers.js';
import { membershipFixtureEvents } from './membership-fixtures.js';

const ORG_ID = '0199c749-9300-7444-8444-444444444444';
const OCK = Uint8Array.from(Buffer.from(JSON.parse(readFileSync(new URL('../fixtures/cross-language-crypto.json', import.meta.url))).ock.key_b64, 'base64'));

function orgRecord() {
  const d = `org:${ORG_ID}`;
  const ad = { d, key_org: ORG_ID, key_ref: `ock:${ORG_ID}`, key_version: 'v1',
    legacy_kind: '32005', schema: CONFIDENTIAL_SCHEMA, t: 'org' };
  const nonce = new Uint8Array(24);
  nonce[0] = 9;
  const content = { id: ORG_ID, name: 'rekey-org', display_name: 'Rekey Org',
    strict_revocation: false, updated_at: '2026-10-04T12:00:00Z' };
  const ciphertext = xchacha20poly1305(OCK, nonce, new TextEncoder().encode(JSON.stringify(ad)))
    .encrypt(new TextEncoder().encode(JSON.stringify(content)));
  return confidentialCpStateFixture({ d, topic: 'org', legacyKind: 32005,
    content: JSON.stringify({ schema: CONFIDENTIAL_SCHEMA, algorithm: CONFIDENTIAL_ALGORITHM,
      key_org: ORG_ID, key_ref: `ock:${ORG_ID}`, key_version: 'v1', nonce: base64Encode(nonce),
      ciphertext: base64Encode(ciphertext), associated_data: ad }) });
}

async function openOrg(page, role) {
  const { keyEnvelope, member } = membershipFixtureEvents({ orgID: ORG_ID,
    memberPubkey: TEST_PUBKEY, servicePubkey: E2E_SERVICE_PUBKEY, role });
  await installE2EMocks(page, { backendRole: null, nostrEvents: [keyEnvelope, member, orgRecord()] });
  await page.goto(`/orgs/${ORG_ID}`);
  await expect(page.getByRole('heading', { name: 'Members (1)' })).toBeVisible();
}

test('owner sees rekey and strict-revocation controls; accepted data renders', async ({ page }) => {
  await openOrg(page, 'owner');
  await expect(page.getByRole('button', { name: 'Rotate and re-encrypt' })).toBeVisible();
  await expect(page.getByRole('checkbox', { name: /Strict revocation/ })).toBeVisible();

  await page.evaluate(() => {
    window.__BAHIA_E2E_ORIGINAL_STATUS = window.__BAHIA_E2E_MAKE_INTENT_STATUS;
    window.__BAHIA_E2E_MAKE_INTENT_STATUS = (intent, options) =>
      window.__BAHIA_E2E_ORIGINAL_STATUS(intent, { ...options, status: 'queued' });
    const signed = window.__BAHIA_E2E_SIGNED_INTENTS;
    const push = signed.push;
    window.__BAHIA_E2E_SIGNED_SNAPSHOT = [];
    signed.push = function (...events) {
      window.__BAHIA_E2E_SIGNED_SNAPSHOT.push(...events);
      return push.apply(this, events);
    };
  });
  await page.getByPlaceholder('Why are you rotating this key?').fill('Former member left');
  await page.getByRole('button', { name: 'Rotate and re-encrypt' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Re-encryption pending' })).toBeVisible();
  await expect.poll(() => page.evaluate(() => window.__BAHIA_E2E_INTENT_WRAPS.length)).toBe(1);

  const { inner, outer } = await page.evaluate(() => window.__BAHIA_E2E_INTENT_WRAPS[0]);
  expect(verifyEvent(inner)).toBe(true);
  expect(verifyEvent(outer)).toBe(true);
  expect(outer.kind).toBe(1059);
  expect(inner.tags).toContainEqual(['domain', 'org']);
  expect(inner.tags).toContainEqual(['schema', 'bahia.intent.org.v1']);
  expect(inner.tags).toContainEqual(['op', 'rekey']);
  expect(inner.tags).toContainEqual(['d', ORG_ID]);
  expect(JSON.parse(inner.content)).toEqual({ org_id: ORG_ID, reason: 'Former member left' });
  expect(await page.evaluate(() => window.__BAHIA_E2E_SIGNED_SNAPSHOT[0]?.id)).toBe(inner.id);

  await page.evaluate(intent => {
    window.__bahiaPushNostrEvent(window.__BAHIA_E2E_ORIGINAL_STATUS(intent, {
      id: `accepted-${intent.id}`, createdAt: Math.floor(Date.now() / 1000) + 10,
      data: { key_version: 'v2', records_republished: 7 }
    }));
  }, inner);
  await expect(page.getByText('Key version v2 · 7 records re-encrypted')).toBeVisible();
});

test('ordinary member sees neither rekey nor strict-revocation mutation', async ({ page }) => {
  await openOrg(page, 'viewer');
  await expect(page.getByRole('button', { name: 'Rotate and re-encrypt' })).toHaveCount(0);
  await expect(page.getByRole('checkbox', { name: /Strict revocation/ })).toHaveCount(0);
});

test('strict-revocation toggle publishes a revisioned org/update intent', async ({ page }) => {
  await openOrg(page, 'admin');
  await page.getByRole('checkbox', { name: /Strict revocation/ }).check();
  await expect.poll(() => page.evaluate(() => window.__BAHIA_E2E_INTENT_WRAPS.length)).toBe(1);
  const { inner, outer } = await page.evaluate(() => window.__BAHIA_E2E_INTENT_WRAPS[0]);
  expect(verifyEvent(inner)).toBe(true);
  expect(outer.kind).toBe(1059);
  expect(inner.tags).toContainEqual(['domain', 'org']);
  expect(inner.tags).toContainEqual(['op', 'update']);
  expect(inner.tags).toContainEqual(['schema', 'bahia.intent.org.v1']);
  expect(JSON.parse(inner.content)).toEqual({ id: ORG_ID, strict_revocation: true,
    expected_updated_at: '2026-10-04T12:00:00Z' });
});
