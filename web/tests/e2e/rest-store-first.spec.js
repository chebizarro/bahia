import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { test, expect } from '@playwright/test';
import { cpStateFixture, confidentialCpStateFixture, workerStateFixture } from './cp-state-fixtures.js';
import { E2E_SERVICE_PUBKEY, TEST_PUBKEY, installE2EMocks } from './helpers.js';

const goFixture = JSON.parse(readFileSync(join(process.cwd(), 'tests/fixtures/paysec-confidential.json'), 'utf8'));
const now = Math.floor(Date.now() / 1000);
const artifactId = 'artifact-w2s3';

function state(schema, d, content, createdAt = now, deleted = false) {
  return cpStateFixture({ schema, d, content, createdAt, deleted });
}
function confidential(record) {
  return confidentialCpStateFixture({ d: record.d_tag, topic: record.topic,
    legacyKind: record.legacy_kind, content: record.encrypted_content, createdAt: now });
}
const initial = [
  state('bahia.registry.backup-repository.v1', 'repo-w2s3', { id: 'repo-w2s3', name: 'Backup North', backend: 's3' }),
  state('bahia.registry.ml-model.v1', 'model-w2s3', { id: 'model-w2s3', name: 'Model North', slug: 'model-north' }),
  workerStateFixture({ pubkey: 'worker-a', status: 'online' }),
  state('bahia.registry.artifact.v1', artifactId, { id: artifactId, name: 'Artifact North', digest: 'sha256:' + 'a'.repeat(64) }),
  { kind: 4903, pubkey: E2E_SERVICE_PUBKEY, created_at: now,
    tags: [['t', 'cp-audit'], ['fact', 'w2s3-fact'], ['event_type', 'backup.created'], ['service', 'svc-w2s3']],
    content: JSON.stringify({ event_type: 'backup.created', entity_id: 'svc-w2s3' }) },
  { kind: 30078, pubkey: E2E_SERVICE_PUBKEY, created_at: now,
    tags: [['d', 'sbom-w2s3'], ['t', 'sbom-reference'], ['artifact', artifactId], ['format', 'spdx'], ['subject', 'sha256:' + 'a'.repeat(64)]],
    content: JSON.stringify({ format: 'spdx', packages: [{ name: 'package-north', version: '1.0' }], storage: { uri: 'blossom://sbom-w2s3' } }) },
  ...goFixture.records.map(confidential)
];

test.describe('W2-S3 store-first views', () => {
  test.beforeEach(async ({ page }) => { await installE2EMocks(page, { nostrEvents: initial }); });

  test('activity, backup, ML, SBOM, payments and security render without read-path spinners', async ({ page }) => {
    await page.goto('/events');
    await expect(page.getByText('backup.created')).toBeVisible();
    await page.goto('/backup');
    await expect(page.getByText('Backup North')).toBeVisible();
    await expect(page.getByText('Loading backup posture...')).toHaveCount(0);
    await page.goto('/ml');
    await expect(page.getByText('Model North')).toBeVisible();
    await expect(page.getByText('Bootstrapping inference control plane…')).toHaveCount(0);
    await page.goto(`/artifacts/${artifactId}?tab=sbom`);
    await expect(page.getByText('package-north')).toBeVisible();
    await page.goto('/payments?worker=worker-a');
    await expect(page.getByText('Payment records not readable with this key')).toBeVisible({ timeout: 20000 });
    await page.goto('/security');
    await expect(page.getByText(/Security records not readable with this key/)).toBeVisible();
  });

  test('live backup replacement and tombstone update the view without a refresh cascade', async ({ page }) => {
    await page.goto('/backup');
    await expect(page.getByText('Backup North')).toBeVisible();
    await page.evaluate((event) => window.__bahiaPushNostrEvent(event),
      state('bahia.registry.backup-repository.v1', 'repo-w2s3', { id: 'repo-w2s3', name: 'Backup South', backend: 's3' }, now + 1));
    await expect(page.getByText('Backup South')).toBeVisible();
    await expect(page.getByText('Backup North')).toHaveCount(0);
    await page.evaluate((event) => window.__bahiaPushNostrEvent(event),
      state('bahia.registry.backup-repository.v1', 'repo-w2s3', { id: 'repo-w2s3' }, now + 2, true));
    await expect(page.getByText('Backup South')).toHaveCount(0);
    await expect(page.getByText('Loading backup posture...')).toHaveCount(0);
  });

  test('fleet OCK arrival decrypts cached payment and security records without ContextVM', async ({ page }) => {
    await page.goto('/payments?worker=worker-a');
    await expect(page.getByText('Payment records not readable with this key')).toBeVisible({ timeout: 20000 });
    const wrap = {
      schema: 'bahia.ock-wrap.v1', org_id: 'fleet', key_ref: 'ock:fleet',
      version: goFixture.ock.version, key: goFixture.ock.key_b64, recipient_pubkey: TEST_PUBKEY
    };
    await page.evaluate((event) => window.__bahiaPushNostrEvent(event), confidentialCpStateFixture({
      d: 'org-key:fleet:v3:e2e', topic: 'org-key-envelope', legacyKind: 32010,
      content: `mock-nip44:${Buffer.from(JSON.stringify(wrap)).toString('base64')}`
    }));
    await expect(page.getByText('42 sats', { exact: true })).toBeVisible();
    await page.goto('/security');
    await expect(page.getByText('Go fixture finding')).toBeVisible();
    await expect(page.getByText(/Security records not readable with this key/)).toHaveCount(0);
    await page.goto('/');
    await expect(page.locator('main a[href="/payments"] .card:has-text("Recent Spend") .card-value')).toHaveText('42 sats');
    expect(await page.evaluate(() => window.__BAHIA_E2E_CONTEXTVM_REQUESTS.filter((request) =>
      ['payments.history', 'security/findings-list', 'security/schedules-list'].includes(request.operation)))).toEqual([]);
  });
});
