// bahia-fbyo5: the daemon publishes its operator allowlists as fleet-OCK
// encrypted cp-state. A session holding the fleet OCK trusts the other
// authorized operators' documents; one without it keeps trusting only the
// signed-in key. Relays see neither list.
import { test, expect } from '@playwright/test';
import { E2E_SERVICE_PUBKEY, TEST_PUBKEY, e2eTestPubkey, installE2EMocks } from './helpers.js';
import { fleetOCKKeyWrapFixture, operatorAllowlistFixture } from './fleet-ock-fixtures.js';

const now = Math.floor(Date.now() / 1000);
const otherOperator = e2eTestPubkey('authorized-other-operator');
const stranger = e2eTestPubkey('non-listed-signer');

const status = (service) => ({
  kind: 30351, pubkey: E2E_SERVICE_PUBKEY, created_at: now,
  tags: [['d', `continuity-status:${service}`], ['service', service], ['t', 'continuity'], ['t', 'continuity-status']],
  content: JSON.stringify({ service_key: service, active_profile: 'full', operation_state: 'steady' })
});
const failoverRequest = (service, pubkey) => ({
  kind: 38430, pubkey, created_at: now,
  tags: [['service', service], ['worker', 'standby-a']], content: JSON.stringify({ reason: 'primary unavailable' })
});

const allowlist = operatorAllowlistFixture({ scope: 'continuity', pubkeys: [otherOperator], createdAt: now });
const operatorEvents = [
  status('svc-cached'),
  failoverRequest('svc-mine', TEST_PUBKEY),
  failoverRequest('svc-other-operator', otherOperator),
  failoverRequest('svc-stranger', stranger)
];

test('the allowlist record names no operator in the clear', () => {
  const serialized = JSON.stringify(allowlist).toLowerCase();
  expect(serialized).not.toContain(otherOperator);
  expect(allowlist.tags.some((tag) => tag[0] === 'p')).toBe(false);
  const tags = Object.fromEntries(allowlist.tags.filter(([name]) => ['d', 't', 'legacy_kind', 'domain'].includes(name)));
  expect(tags).toEqual({ d: 'operators:continuity', t: 'operator-allowlist', legacy_kind: '32029', domain: 'operator' });
  expect(JSON.parse(allowlist.content).schema).toBe('bahia.confidential.aead.v1');
});

test('a fleet-OCK holder sees another authorized operator\'s request and still not a non-listed signer\'s', async ({ page }) => {
  await installE2EMocks(page, { nostrEvents: [...operatorEvents, fleetOCKKeyWrapFixture({ recipient: TEST_PUBKEY, createdAt: now }), allowlist] });
  await page.goto('/continuity');
  await expect(page.getByRole('heading', { name: 'svc-cached' })).toBeVisible();
  await page.getByRole('button', { name: /^Requests/ }).click();
  await expect(page.getByRole('heading', { name: 'svc-mine' })).toBeVisible();
  // The decrypted allowlist widens the trusted operator set and the relay REQ.
  await expect(page.getByRole('heading', { name: 'svc-other-operator' })).toBeVisible();
  await expect(page.getByTestId('continuity-operator-scope-note')).toContainText('authorized fleet operators');
  await expect(page.getByRole('heading', { name: 'svc-stranger' })).toHaveCount(0);
});

test('without the fleet OCK the allowlist is opaque and only the signed-in key is trusted', async ({ page }) => {
  // The record is served, but no key wrap addresses this session.
  await installE2EMocks(page, { nostrEvents: [...operatorEvents, allowlist] });
  await page.goto('/continuity');
  await expect(page.getByRole('heading', { name: 'svc-cached' })).toBeVisible();
  await page.getByRole('button', { name: /^Requests/ }).click();
  await expect(page.getByRole('heading', { name: 'svc-mine' })).toBeVisible();
  await expect(page.getByTestId('continuity-operator-scope-note')).toContainText('other fleet operators are not shown');
  await expect(page.getByRole('heading', { name: 'svc-other-operator' })).toHaveCount(0);
  await expect(page.getByRole('heading', { name: 'svc-stranger' })).toHaveCount(0);
});
