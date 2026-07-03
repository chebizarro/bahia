import { test, expect } from '@playwright/test';
import { E2E_SERVICE_PUBKEY, installE2EMocks } from './helpers.js';

const SERVICE_ID = 'service-1';
const ENCRYPTED_RELAY = 'wss://relay.example.com';

const mockService = {
  id: SERVICE_ID,
  name: 'web-app',
  artifact_repo: 'ghcr.io/test/web-app',
  repo_url: 'https://github.com/test/web-app',
  runtime_type: 'docker',
  default_branch: 'main',
  created_at: new Date().toISOString()
};

const mockBuilds = [{ id: 'build-1', service_id: SERVICE_ID, git_ref: 'main', git_sha: 'abc123def456', status: 'success', created_at: new Date().toISOString() }];
const mockArtifacts = [{ id: 'artifact-1', build_id: 'build-1', service_id: SERVICE_ID, name: 'web-app', version: '1.0.0', digest: 'sha256:abc123def456', created_at: new Date().toISOString() }];
const seededSecrets = [
  { id: 'secret-1', service_id: SERVICE_ID, name: 'DATABASE_URL', value: 'postgres://hidden.example/db', version: 1, created_at: new Date().toISOString() },
  { id: 'secret-2', service_id: SERVICE_ID, name: 'API_KEY', value: 'api-key-hidden-value', version: 1, created_at: new Date().toISOString() }
];

async function seedEncryptedSecrets(page) {
  await page.addInitScript(({ serviceId, secrets }) => {
    localStorage.setItem('__bahia_e2e_service_secrets', JSON.stringify({ [serviceId]: secrets }));
  }, { serviceId: SERVICE_ID, secrets: seededSecrets });
}

test.beforeEach(async ({ page }) => {
  await seedEncryptedSecrets(page);
  await installE2EMocks(page, {
    systemInfo: {
      nostr: {
        browser_relays: [ENCRYPTED_RELAY],
        service_relays: [ENCRYPTED_RELAY],
        service_pubkey: E2E_SERVICE_PUBKEY
      },
      features: {
        relay_sidecar: true,
        relay_read_models: true,
        encrypted_controlplane: true,
        legacy_sse: false
      }
    }
  });

  await page.route('**/api/v1/services', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [mockService] }) }));
  await page.route('**/api/v1/services/service-1/builds', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: mockBuilds }) }));
  await page.route('**/api/v1/services/service-1/artifacts', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: mockArtifacts }) }));
  await page.route('**/api/v1/environments', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [] }) }));
  await page.route('**/api/v1/repositories**', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [] }) }));
});

test.describe('Service Secrets Smoke Test', () => {
  test('loads encrypted relay-backed secrets without rendering values', async ({ page }) => {
    await page.goto('/services/service-1');

    await expect(page.getByRole('heading', { name: 'web-app' })).toBeVisible();
    await expect(page.getByRole('heading', { name: /Secrets \(2\)/ })).toBeVisible();
    await expect(page.locator('.secret-row:has-text("DATABASE_URL")')).toBeVisible();
    await expect(page.locator('.secret-row:has-text("API_KEY")')).toBeVisible();

    const pageContent = await page.content();
    expect(pageContent).not.toContain('postgres://hidden.example/db');
    expect(pageContent).not.toContain('api-key-hidden-value');
  });

  test('creates a secret through ContextVM request coverage', async ({ page }) => {
    await page.goto('/services/service-1');

    await page.getByRole('button', { name: 'Add Secret' }).click();
    await expect(page.getByRole('dialog', { name: 'Add Secret' })).toBeVisible();
    await page.locator('#secret-name').fill('NEW_SECRET');
    await page.locator('#secret-value').fill('super-secret-value-123');
    await page.getByRole('dialog', { name: 'Add Secret' }).getByRole('button', { name: 'Create Secret' }).click();

    await expect(page.getByRole('dialog', { name: 'Add Secret' })).not.toBeVisible();
    await expect(page.locator('.secret-row:has-text("NEW_SECRET")')).toBeVisible();
    expect(await page.content()).not.toContain('super-secret-value-123');
  });

  test('reveals, copies, updates, and deletes via encrypted secret state', async ({ page }) => {
    await page.addInitScript(() => {
      Object.defineProperty(window.navigator, 'clipboard', {
        configurable: true,
        value: { writeText: async (text) => { window.__copied_secret_value = text; } }
      });
      window.__copied_secret_value = null;
    });

    await page.goto('/services/service-1');

    await page.locator('.secret-row:has-text("DATABASE_URL") button:has-text("Reveal")').click();
    await expect(page.getByRole('dialog', { name: 'Reveal Secret Value' })).toBeVisible();
    await expect(page.locator('text=postgres://hidden.example/db')).not.toBeVisible();
    await page.getByRole('dialog', { name: 'Reveal Secret Value' }).getByRole('button', { name: 'Reveal Value' }).click();
    await expect(page.locator('text=postgres://hidden.example/db')).toBeVisible();
    await page.getByRole('button', { name: /Copy to Clipboard/ }).click();
    expect(await page.evaluate(() => window.__copied_secret_value)).toBe('postgres://hidden.example/db');
    await page.getByRole('dialog', { name: 'Reveal Secret Value' }).getByRole('button', { name: 'Close' }).click();

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
});
