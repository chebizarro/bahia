import { test, expect } from '@playwright/test';
import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';
import { installE2EMocks, E2E_SERVICE_PUBKEY, TEST_PUBKEY, signE2EEvent } from './helpers.js';
import { confidentialCpStateFixture } from './cp-state-fixtures.js';
import { base64Encode, CONFIDENTIAL_SCHEMA, CONFIDENTIAL_ALGORITHM } from '../../src/lib/nostr/confidential.js';

const now = Math.floor(Date.now() / 1000);
const service = '11111111-1111-4111-8111-111111111111';
const environment = '22222222-2222-4222-8222-222222222222';
const unit = '33333333-3333-4333-8333-333333333333';
const owner = TEST_PUBKEY;
const hash = 'a'.repeat(64);
const topic = (kind, name, d, content, tags = [], created_at = now, pubkey = E2E_SERVICE_PUBKEY) => ({
  kind, pubkey, created_at, tags: [['d', d], ['t', name], ...tags], content: JSON.stringify(content)
});
const wrap = { schema: 'bahia.ock-wrap.v1', org_id: 'fleet', key_ref: 'ock:fleet', version: 1,
  key: base64Encode(new Uint8Array(32).fill(7)), recipient_pubkey: TEST_PUBKEY };
const keyWrap = confidentialCpStateFixture({ d: 'org-key:fleet:v1:f75', topic: 'org-key-envelope', legacyKind: 32010,
  content: `mock-nip44:${Buffer.from(JSON.stringify(wrap)).toString('base64')}` });
function encrypted(topicName, legacyKind, d, payload, nonceByte) {
  const ad = { d, key_org: 'fleet', key_ref: 'ock:fleet', key_version: 'v1', legacy_kind: String(legacyKind), schema: CONFIDENTIAL_SCHEMA, t: topicName };
  const nonce = new Uint8Array(24); nonce[0] = nonceByte;
  const ciphertext = xchacha20poly1305(new Uint8Array(32).fill(7), nonce, new TextEncoder().encode(JSON.stringify(ad)))
    .encrypt(new TextEncoder().encode(JSON.stringify(payload)));
  return confidentialCpStateFixture({ d, topic: topicName, legacyKind,
    content: JSON.stringify({ schema: CONFIDENTIAL_SCHEMA, algorithm: CONFIDENTIAL_ALGORITHM,
      key_org: 'fleet', key_ref: 'ock:fleet', key_version: 'v1', nonce: base64Encode(nonce),
      ciphertext: base64Encode(ciphertext), associated_data: ad }) });
}

const healthTags = [['service', service], ['environment', environment], ['deployment_unit', unit], ['target', 'gateway']];
const canaryTags = [['service', service], ['environment', environment], ['deployment_unit', unit], ['hostname', 'edge.example.test']];
const health = { service_id: service, environment_id: environment, deployment_unit_id: unit,
  runtime_target_name: 'gateway', host: 'host-1', supervisor_type: 'docker', status: 'healthy',
  last_observed_at: '2026-10-03T00:00:00Z', restart_count: 0, consecutive_restart_count: 0 };
const canary = { service_id: service, environment_id: environment, deployment_unit_id: unit,
  hostname: 'edge.example.test', perspective: 'public_edge', classification: 'connect_failed', open: true,
  consecutive_failures: 3, consecutive_successes: 0, last_observed_at: '2026-10-03T00:00:00Z' };
const desiredSchema = 'cascadia.config.route.v1';
const desired = signE2EEvent(topic(30078, 'config-fabric', `service:${service}:route`, { service_id: service, scope: 'prod', version: 1, schema: desiredSchema, policy: { enabled: true } },
  [['service', service], ['scope', 'prod'], ['version', '1'], ['schema', desiredSchema]], now, TEST_PUBKEY));

const readPaths = [
  /\/api\/v1\/instance-health(?:\?|$)/,
  /\/api\/v1\/route-canaries(?:\?|$)/,
  /\/api\/v1\/config-fabric\/drift(?:\?|$)/,
  /\/api\/v1\/blossom\/(?:list|servers|health|stats)(?:\?|$)/,
  /\/api\/v1\/soulfactory\/runtimes(?:\?|$)/
];
async function withoutLegacyReads(page, events) {
  const reads = [];
  page.on('request', request => { if (readPaths.some(pattern => pattern.test(new URL(request.url()).pathname))) reads.push(request.url()); });
  await installE2EMocks(page, { nostrEvents: events });
  return reads;
}

test('managed instance health and recovery history render from signed state and audit events', async ({ page }) => {
  const events = [topic(30900, 'runtime-instance-health', 'runtime:instance:f75', { health }, healthTags),
    topic(4903, 'runtime-instance-health', 'audit:f75', { type: 'health_observation', status: 'healthy', observed_at: health.last_observed_at, reason: 'ready' }, healthTags, now + 1)];
  const reads = await withoutLegacyReads(page, events);
  await page.goto('/instance-health');
  await expect(page.getByRole('button', { name: /gateway/ })).toBeVisible({ timeout: 20000 });
  await page.getByRole('button', { name: /gateway/ }).click();
  await expect(page.getByRole('heading', { name: 'Recent health events' })).toBeVisible();
  await expect(page.getByText('ready')).toBeVisible();
  expect(reads).toEqual([]);
});

test('route canary state and transition history render from signed events', async ({ page }) => {
  const events = [topic(30900, 'route-canary', 'route:f75', { route_canary: canary,
      observed_instance_status: 'healthy', service_healthy_route_broken: true }, canaryTags),
    topic(4903, 'route-canary', 'audit:route:f75', { transition: 'opened', classification: 'connect_failed', occurred_at: '2026-10-03T00:00:00Z', reason: 'HTTP 502' }, canaryTags, now + 1)];
  const reads = await withoutLegacyReads(page, events);
  await page.goto('/route-canaries');
  await expect(page.getByRole('button', { name: /edge.example.test/ })).toBeVisible({ timeout: 20000 });
  await page.getByRole('button', { name: /edge.example.test/ }).click();
  await expect(page.getByRole('heading', { name: 'Recent events' })).toBeVisible();
  await expect(page.getByText(/HTTP 502/)).toBeVisible();
  expect(reads).toEqual([]);
});

test('Config Fabric list and detail derive drift from signed desired and status records', async ({ page }) => {
  const events = [desired, topic(30900, 'config-status', `config-status:${service}:route:prod`, {
    service_id: service, scope: 'prod', policy_schema: desiredSchema, version: 1, status: 'applied',
    config_event_id: desired.id, effective_version: 1, last_applied_event_id: desired.id
  }, [['domain', 'config-status'], ['schema', 'cascadia.config.status.v3'], ['service', service], ['scope', 'prod'], ['version', '1'], ['status', 'applied'], ['e', desired.id]], now + 1)];
  const reads = await withoutLegacyReads(page, events);
  await page.goto('/config-fabric');
  await expect(page.getByRole('table', { name: 'Config Fabric drift' })).toBeVisible({ timeout: 20000 });
  await expect(page.getByText(service).first()).toBeVisible();
  await page.locator('a.coordinate').click();
  await expect(page.getByRole('heading', { name: `${service} / route` })).toBeVisible();
  await expect(page.getByText('Current effective config')).toBeVisible();
  await expect(page.getByText('Not applied')).toHaveCount(0);
  expect(reads).toEqual([]);
});

test('Blossom administration and listing decrypt from fleet OCK without REST reads', async ({ page }) => {
  const server = 'https://blossom.example.test';
  const events = [keyWrap,
    encrypted('blossom-admin', 32043, 'blossom:admin', { servers: [server], health: { [server]: 'ok' } }, 1),
    encrypted('blossom-blob', 32044, `blossom:blob:${owner}:${hash}`, { pubkey: owner, sha256: hash,
      url: `${server}/${hash}`, size: 42, type: 'image/png', uploaded: '2026-10-03T00:00:00Z' }, 2)];
  const reads = await withoutLegacyReads(page, events);
  await page.goto('/artifacts');
  await page.getByRole('button', { name: 'Blossom' }).click();
  await expect(page.getByLabel('blossom.example.test healthy')).toBeVisible();
  await expect(page.getByRole('table').getByText('image/png')).toBeVisible();
  expect(reads).toEqual([]);
});

test('Soul Factory policy arrives as signed state without runtimes REST read', async ({ page }) => {
  const events = [topic(30900, 'soul-factory-runtime-policy', 'soul-factory:runtime-policy', { agent_runtimes: ['openclaw', 'metiq'] })];
  const reads = await withoutLegacyReads(page, events);
  await page.goto('/souls/new');
  await expect(page.getByRole('heading', { name: /New Soul|Create Soul/ })).toBeVisible();
  await expect.poll(() => page.evaluate(async () => {
    const store = await import('/src/lib/stores/souls.svelte.js');
    return store.serverAgentRuntimes.join(',');
  })).toBe('openclaw,metiq');
  expect(reads).toEqual([]);
});
