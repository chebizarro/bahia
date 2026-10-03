import { test, expect } from '@playwright/test';
import { BAHIA_STATE_SCHEMAS, cpStateFixture } from './cp-state-fixtures.js';
import { E2E_SERVICE_PUBKEY, installE2EMocks } from './helpers.js';

const SERVICE_ID = 'service-1';
const BUILD_ID = 'build-1';
const ARTIFACT_ID = 'artifact-1';
const ENCRYPTED_RELAY = 'wss://relay.example.com';

const mockService = {
  schema: 'bahia.registry.service.v1',
  id: SERVICE_ID,
  org_id: '0199c749-9300-7444-8444-444444444444',
  name: 'web-app',
  artifact_repo: 'ghcr.io/test/web-app',
  repo_url: 'https://github.com/test/web-app',
  runtime_type: 'docker',
  default_branch: 'main',
  created_at: '2026-05-13T12:00:00.000Z',
  deleted: false
};

const mockBuilds = [{
  schema: 'bahia.registry.build.v1',
  id: BUILD_ID,
  service_id: SERVICE_ID,
  git_ref: 'main',
  git_sha: 'abc123def456',
  status: 'success',
  ci_system: 'e2e',
  created_at: '2026-05-13T12:05:00.000Z',
  deleted: false
}];

const mockArtifacts = [{
  schema: 'bahia.registry.artifact.v1',
  id: ARTIFACT_ID,
  build_id: BUILD_ID,
  service_id: SERVICE_ID,
  name: 'web-app',
  image_repo: 'ghcr.io/test/web-app',
  image_tag: '1.0.0',
  version: '1.0.0',
  digest: 'sha256:abc123def456',
  image_digest: 'sha256:abc123def456',
  size_bytes: 1024,
  created_at: '2026-05-13T12:10:00.000Z',
  deleted: false
}];

const seededSecrets = [
  { id: 'secret-1', org_id: mockService.org_id, service_id: SERVICE_ID, name: 'DATABASE_URL', value: 'postgres://hidden.example/db', redacted_value: '********', version: 1, created_at: '2026-05-13T12:15:00.000Z', updated_at: '2026-05-13T12:15:00.000Z' },
  { id: 'secret-2', org_id: mockService.org_id, service_id: SERVICE_ID, name: 'API_KEY', value: 'api-key-hidden-value', redacted_value: '********', version: 1, created_at: '2026-05-13T12:16:00.000Z', updated_at: '2026-05-13T12:16:00.000Z' }
];

const relaySystemInfo = {
  nostr: {
    browser_relays: [ENCRYPTED_RELAY],
    service_relays: [ENCRYPTED_RELAY],
    contextvm_relays: [ENCRYPTED_RELAY],
    service_pubkey: E2E_SERVICE_PUBKEY
  },
  features: {
    relay_sidecar: true,
    relay_read_models: true,
    encrypted_controlplane: true,
    encrypted_nostr_requests: true,
    legacy_sse: false
  }
};

function serviceEvent() {
  return cpStateFixture({
    id: 'service-1-registry-event',
    schema: BAHIA_STATE_SCHEMAS.SERVICE_REGISTRY,
    d: SERVICE_ID,
    tags: [['service', SERVICE_ID]],
    content: mockService
  });
}

function buildEvent(build = mockBuilds[0]) {
  return cpStateFixture({
    id: `${build.id}-registry-event`,
    schema: BAHIA_STATE_SCHEMAS.BUILD_REGISTRY,
    d: build.id,
    tags: [['build', build.id], ['service', SERVICE_ID]],
    content: build
  });
}

function artifactEvent(artifact = mockArtifacts[0]) {
  return cpStateFixture({
    id: `${artifact.id}-registry-event`,
    schema: BAHIA_STATE_SCHEMAS.ARTIFACT_REGISTRY,
    d: artifact.id,
    tags: [['artifact', artifact.id], ['service', SERVICE_ID], ['build', BUILD_ID]],
    content: artifact
  });
}

async function seedEncryptedSecrets(page) {
  await page.addInitScript(({ serviceId, secrets }) => {
    localStorage.setItem('__bahia_e2e_service_secrets', JSON.stringify({ [serviceId]: secrets }));
  }, { serviceId: SERVICE_ID, secrets: seededSecrets });
}

async function queueContextVMOperation(page, operation) {
  await page.evaluate((nextOperation) => {
    const queue = Array.isArray(window.__BAHIA_E2E_CONTEXTVM_OPERATIONS)
      ? window.__BAHIA_E2E_CONTEXTVM_OPERATIONS
      : [];
    queue.push(nextOperation);
    window.__BAHIA_E2E_CONTEXTVM_OPERATIONS = queue;
  }, operation);
}

function secretOperation(operation, payload = {}) {
  return { operation, payload: { service_id: SERVICE_ID, ...payload } };
}

test.beforeEach(async ({ page }) => {
  await seedEncryptedSecrets(page);
  await installE2EMocks(page, {
    systemInfo: relaySystemInfo,
    contextVMOperations: [secretOperation('services.secrets.list')],
    nostrEvents: [serviceEvent(), buildEvent(), artifactEvent()]
  });
});

test.describe('Service Secrets Smoke Test', () => {
  test('loads encrypted relay-backed secrets without rendering values', async ({ page }) => {
    await page.goto(`/services/${SERVICE_ID}`);

    await expect(page.getByRole('heading', { name: 'web-app' })).toBeVisible();
    await expect(page.getByRole('heading', { name: /Secrets \(2\)/ })).toBeVisible();
    await expect(page.locator('.secret-row:has-text("DATABASE_URL")')).toBeVisible();
    await expect(page.locator('.secret-row:has-text("API_KEY")')).toBeVisible();

    const pageContent = await page.content();
    expect(pageContent).not.toContain('postgres://hidden.example/db');
    expect(pageContent).not.toContain('api-key-hidden-value');
  });

  test('creates a secret through a 1059 intent and receives 30315 accepted', async ({ page }) => {
    await page.goto(`/services/${SERVICE_ID}`);
    await expect(page.getByRole('heading', { name: 'web-app' })).toBeVisible();

    await page.getByRole('button', { name: 'Add Secret' }).click();
    await expect(page.getByRole('dialog', { name: 'Add Secret' })).toBeVisible();
    await page.locator('#secret-name').fill('NEW_SECRET');
    await page.locator('#secret-value').fill('super-secret-value-123');
    await page.getByRole('dialog', { name: 'Add Secret' }).getByRole('button', { name: 'Create Secret' }).click();

    await expect(page.getByRole('dialog', { name: 'Add Secret' })).not.toBeVisible();
    await expect(page.locator('.secret-row:has-text("NEW_SECRET")')).toBeVisible();
    expect(await page.content()).not.toContain('super-secret-value-123');
    await expect.poll(() => page.evaluate(() => window.__BAHIA_E2E_INTENT_WRAPS.length)).toBe(1);
    const trace = await page.evaluate(() => ({ wraps: window.__BAHIA_E2E_INTENT_WRAPS, statuses: window.__BAHIA_E2E_INTENT_STATUS_EVENTS }));
    expect(trace.wraps).toHaveLength(1);
    expect(trace.wraps[0].outer.kind).toBe(1059);
    expect(trace.wraps[0].outer.tags).toContainEqual(['p', E2E_SERVICE_PUBKEY]);
    expect(trace.wraps[0].inner.kind).toBe(30900);
    expect(trace.wraps[0].inner.content).not.toContain('super-secret-value-123');
    expect(trace.statuses[0].tags).toContainEqual(['status', 'accepted']);
  });

  test('reveals, copies, updates, and deletes via encrypted secret state', async ({ page }) => {
    await page.addInitScript(() => {
      Object.defineProperty(window.navigator, 'clipboard', {
        configurable: true,
        value: { writeText: async (text) => { window.__copied_secret_value = text; } }
      });
      window.__copied_secret_value = null;
    });

    await page.goto(`/services/${SERVICE_ID}`);
    await expect(page.getByRole('heading', { name: 'web-app' })).toBeVisible();

    await queueContextVMOperation(page, secretOperation('services.secrets.reveal', { secret_id: 'secret-1' }));
    await page.locator('.secret-row:has-text("DATABASE_URL") button:has-text("Reveal")').click();
    await expect(page.getByRole('dialog', { name: 'Reveal Secret Value' })).toBeVisible();
    await expect(page.locator('text=postgres://hidden.example/db')).not.toBeVisible();
    await page.getByRole('dialog', { name: 'Reveal Secret Value' }).getByRole('button', { name: 'Reveal Value' }).click();
    await expect(page.locator('text=postgres://hidden.example/db')).toBeVisible();
    await page.getByRole('button', { name: /Copy to Clipboard/ }).click();
    expect(await page.evaluate(() => window.__copied_secret_value)).toBe('postgres://hidden.example/db');
    await page.getByRole('dialog', { name: 'Reveal Secret Value' }).getByText('Close', { exact: true }).click();

    await page.locator('.secret-row:has-text("DATABASE_URL") button:has-text("Update")').click();
    await page.locator('#secret-update-value').fill('updated-secret-value-xyz');
    await page.getByRole('dialog', { name: 'Update Secret' }).getByRole('button', { name: 'Update Secret' }).click();
    await expect(page.getByRole('dialog', { name: 'Update Secret' })).not.toBeVisible();
    expect(await page.content()).not.toContain('updated-secret-value-xyz');

    await page.locator('.secret-row:has-text("API_KEY") button:has-text("Delete")').click();
    await expect(page.getByRole('dialog', { name: 'Delete Secret' })).toBeVisible();
    await page.getByRole('dialog', { name: 'Delete Secret' }).getByRole('button', { name: 'Delete' }).click();
    await expect(page.locator('.secret-row:has-text("API_KEY")')).not.toBeVisible();
  });

  test('disables Create Secret with an explanatory NIP-44 tooltip when unavailable', async ({ page }) => {
    await page.addInitScript(() => { if (window.nostr) delete window.nostr.nip44; });
    await page.goto(`/services/${SERVICE_ID}`);
    const create = page.getByRole('button', { name: 'Add Secret' });
    await expect(create).toBeDisabled();
    await expect(create).toHaveAttribute('title', /NIP-44/);
  });
});
