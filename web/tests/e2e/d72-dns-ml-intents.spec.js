import { test, expect } from '@playwright/test';
import { E2E_SERVICE_PUBKEY, installE2EMocks } from './helpers.js';
import { cpStateFixture } from './cp-state-fixtures.js';
import { BAHIA_STATE_SCHEMAS } from '../../src/lib/nostr/kinds.gen.js';

const id = n => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`;
const revision = '2026-10-03T00:00:00Z';
let acceptedSequence = 0;
const systemInfo = { organization_id: id(1), nostr: { browser_relays: ['ws://relay.test.local'], service_pubkey: E2E_SERVICE_PUBKEY },
  features: { relay_sidecar: true, relay_read_models: true, encrypted_nostr_requests: true, legacy_sse: false } };

async function acceptedAfterPending(page, domain, op, coordinate, expected) {
  const overlay = page.getByTestId(`${domain}-pending-intents`);
  await expect(overlay).toContainText('Pending');
  await expect.poll(() => page.evaluate(({ domain, op }) => window.__BAHIA_E2E_SIGNED_INTENTS.some(event =>
    event.tags.some(tag => tag[0] === 'domain' && tag[1] === domain) &&
    event.tags.some(tag => tag[0] === 'op' && tag[1] === op)), { domain, op })).toBe(true);
  const intent = await page.evaluate(({ domain, op }) => window.__BAHIA_E2E_SIGNED_INTENTS.find(event =>
    event.tags.some(tag => tag[0] === 'domain' && tag[1] === domain) &&
    event.tags.some(tag => tag[0] === 'op' && tag[1] === op)), { domain, op });
  expect(intent.kind).toBe(30900);
  expect(intent.tags).toContainEqual(['d', coordinate]);
  expect(JSON.parse(intent.content)).toMatchObject(expected);
  const createdAt = Math.floor(Date.now() / 1000) + ++acceptedSequence;
  await page.evaluate(({ intent, createdAt }) => window.__bahiaPushNostrEvent(window.__BAHIA_E2E_MAKE_INTENT_STATUS(intent,
    { id: `accepted-${intent.id}`, createdAt })), { intent, createdAt });
  await expect(overlay).toHaveCount(0);
}

function dnsSeeds() {
  return [
    cpStateFixture({ schema: BAHIA_STATE_SCHEMAS.DNS_ZONE_STATE, d: 'zone:example.test',
      content: { name: 'example.test', backend_ref: 'primary', visibility: 'internal', ttl: 60, authoritative: true, updated_at: revision } }),
    cpStateFixture({ schema: BAHIA_STATE_SCHEMAS.DNS_ENDPOINT_STATE, d: 'endpoint:service:api:prod',
      content: { coordinate: 'endpoint:service:api:prod', family: 'service', name: 'api', environment: 'prod',
        zone: 'example.test', fqdn: 'api.example.test', address: '192.0.2.10', source: 'operator', updated_at: revision } }),
    cpStateFixture({ schema: BAHIA_STATE_SCHEMAS.DNS_BACKEND_STATE, d: 'dnsbackend:secondary',
      content: { ref: 'secondary', type: 'coredns', health: 'healthy', updated_at: revision } }),
    cpStateFixture({ schema: BAHIA_STATE_SCHEMAS.DNS_POLICY_STATE, d: `dnspolicy:${id(2)}`,
      content: { id: id(2), name: 'prod-ttl', enabled: true, rules: [{ match: { environment: 'prod' },
        action: { ttl_override: 60 } }], updated_at: revision } })
  ];
}

async function openDNS(page) {
  await installE2EMocks(page, { systemInfo, nostrEvents: dnsSeeds() });
  await page.goto('/dns');
  await page.evaluate(() => { window.__BAHIA_E2E_INTENT_AUTO_STATUS = false; });
  return page.getByTestId('dns-registry-mutations');
}

test('DNS zone edit and delete reconcile only after scoped acceptance', async ({ page }) => {
  const registry = await openDNS(page);
  const form = registry.locator('form').nth(0);
  await form.getByLabel('Existing zone').selectOption('example.test');
  await form.getByLabel('TTL').fill('120');
  await form.getByRole('button', { name: 'Update zone' }).click();
  await acceptedAfterPending(page, 'dns', 'zone-update', 'zone:example.test',
    { name: 'example.test', ttl: 120, expected_updated_at: revision });
  page.once('dialog', dialog => dialog.accept());
  await form.getByRole('button', { name: 'Delete zone' }).click();
  await acceptedAfterPending(page, 'dns', 'zone-delete', 'zone:example.test',
    { name: 'example.test', expected_updated_at: revision });
});

test('DNS endpoint create, edit and delete reconcile only after scoped acceptance', async ({ page }) => {
  const registry = await openDNS(page);
  const form = registry.locator('form').nth(1);
  await form.getByLabel('Name').fill('new');
  await form.getByLabel('Environment').fill('prod');
  await form.getByLabel('Zone').fill('example.test');
  await form.getByLabel('FQDN').fill('new.example.test');
  await form.getByLabel('Address').fill('192.0.2.20');
  await form.getByRole('button', { name: 'Create endpoint' }).click();
  await acceptedAfterPending(page, 'dns', 'endpoint-create', 'endpoint:service:new:prod',
    { name: 'new', environment: 'prod', address: '192.0.2.20' });
  await form.getByLabel('Existing endpoint').selectOption('endpoint:service:api:prod');
  await form.getByLabel('Address').fill('192.0.2.11');
  await form.getByRole('button', { name: 'Update endpoint' }).click();
  await acceptedAfterPending(page, 'dns', 'endpoint-update', 'endpoint:service:api:prod',
    { address: '192.0.2.11', expected_updated_at: revision });
  page.once('dialog', dialog => dialog.accept());
  await form.getByRole('button', { name: 'Delete endpoint' }).click();
  await acceptedAfterPending(page, 'dns', 'endpoint-delete', 'endpoint:service:api:prod',
    { coordinate: 'endpoint:service:api:prod', expected_updated_at: revision });
});

test('DNS backend create, edit and delete reconcile only after scoped acceptance', async ({ page }) => {
  const registry = await openDNS(page);
  const form = registry.locator('form').nth(2);
  await form.getByLabel('Reference').fill('tertiary');
  await form.getByRole('button', { name: 'Create backend' }).click();
  await acceptedAfterPending(page, 'dns', 'backend-create', 'dnsbackend:tertiary', { ref: 'tertiary', type: 'coredns' });
  await form.getByLabel('Existing backend').selectOption('secondary');
  await form.getByLabel('Health').selectOption('unhealthy');
  await form.getByRole('button', { name: 'Update backend' }).click();
  await acceptedAfterPending(page, 'dns', 'backend-update', 'dnsbackend:secondary',
    { ref: 'secondary', health: 'unhealthy', expected_updated_at: revision });
  page.once('dialog', dialog => dialog.accept());
  await form.getByRole('button', { name: 'Delete backend' }).click();
  await acceptedAfterPending(page, 'dns', 'backend-delete', 'dnsbackend:secondary',
    { ref: 'secondary', expected_updated_at: revision });
});

test('DNS policy edit and delete reconcile only after scoped acceptance', async ({ page }) => {
  const registry = await openDNS(page);
  const form = registry.locator('form').nth(3);
  await form.getByLabel('Existing policy').selectOption(id(2));
  await form.getByLabel('Rules (JSON array)').fill('[{"match":{"environment":"prod"},"action":{"ttl_override":120}}]');
  await form.getByRole('button', { name: 'Update policy' }).click();
  await acceptedAfterPending(page, 'dns', 'policy-update', `dnspolicy:${id(2)}`,
    { id: id(2), rules: [{ match: { environment: 'prod' }, action: { ttl_override: 120 } }], expected_updated_at: revision });
  page.once('dialog', dialog => dialog.accept());
  await form.getByRole('button', { name: 'Delete policy' }).click();
  await acceptedAfterPending(page, 'dns', 'policy-delete', `dnspolicy:${id(2)}`,
    { id: id(2), expected_updated_at: revision });
});

test('ML identity updates and deletes reconcile only after scoped acceptance', async ({ page }) => {
  await installE2EMocks(page, { systemInfo, nostrEvents: [
    cpStateFixture({ schema: BAHIA_STATE_SCHEMAS.ML_MODEL_REGISTRY, d: 'model:sample',
      content: { id: id(3), slug: 'sample', name: 'Sample', updated_at: revision } }),
    cpStateFixture({ schema: BAHIA_STATE_SCHEMAS.ML_MODEL_VERSION_REGISTRY, d: `model-version:${id(4)}`,
      content: { id: id(4), model_id: id(3), version: 'v1', source: { uri: 's3://models/sample-v1' }, updated_at: revision } }),
    cpStateFixture({ schema: BAHIA_STATE_SCHEMAS.ML_INFERENCE_ENDPOINT_REGISTRY, d: `endpoint:${id(5)}`,
      content: { id: id(5), name: 'inference', environment_id: id(6), updated_at: revision } }),
    cpStateFixture({ schema: BAHIA_STATE_SCHEMAS.ENVIRONMENT_REGISTRY, d: `environment:${id(6)}`,
      content: { id: id(6), name: 'production' } })
  ] });
  await page.goto('/ml');
  await page.evaluate(() => { window.__BAHIA_E2E_INTENT_AUTO_STATUS = false; });
  const registry = page.getByTestId('ml-registry-mutations');
  const model = registry.locator('form').nth(0);
  await model.getByLabel('Existing model').selectOption(id(3));
  await model.getByLabel('Slug').fill('sample-renamed');
  await model.getByRole('button', { name: 'Update model' }).click();
  await acceptedAfterPending(page, 'ml', 'model-update', 'model:sample-renamed', { id: id(3), slug: 'sample-renamed', expected_updated_at: revision });
  page.once('dialog', dialog => dialog.accept());
  await model.getByRole('button', { name: 'Delete model' }).click();
  await acceptedAfterPending(page, 'ml', 'model-delete', 'model:sample', { id: id(3), expected_updated_at: revision });
  const version = registry.locator('form').nth(1);
  await version.getByLabel('Existing version').selectOption(id(4));
  await version.getByLabel('Version', { exact: true }).fill('v2');
  await version.getByRole('button', { name: 'Update version' }).click();
  await acceptedAfterPending(page, 'ml', 'version-update', `model-version:${id(4)}`,
    { id: id(4), version: 'v2', expected_updated_at: revision });
  page.once('dialog', dialog => dialog.accept());
  await version.getByRole('button', { name: 'Delete version' }).click();
  await acceptedAfterPending(page, 'ml', 'version-delete', `model-version:${id(4)}`,
    { id: id(4), expected_updated_at: revision });
  const endpoint = registry.locator('form').nth(2);
  await endpoint.getByLabel('Existing endpoint').selectOption(id(5));
  await endpoint.getByLabel('Name').fill('inference-renamed');
  await endpoint.getByRole('button', { name: 'Update endpoint' }).click();
  await acceptedAfterPending(page, 'ml', 'endpoint-update', `endpoint:${id(5)}`,
    { id: id(5), name: 'inference-renamed', expected_updated_at: revision });
  page.once('dialog', dialog => dialog.accept());
  await endpoint.getByRole('button', { name: 'Delete endpoint' }).click();
  await acceptedAfterPending(page, 'ml', 'endpoint-delete', `endpoint:${id(5)}`,
    { id: id(5), expected_updated_at: revision });
});
