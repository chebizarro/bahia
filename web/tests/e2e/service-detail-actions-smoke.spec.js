import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

const mockBuilds = [
  {
    id: 'build-1',
    git_sha: 'abcdef1234567890',
    git_ref: 'refs/heads/main',
    status: 'succeeded',
    ci_system: 'hive-ci'
  }
];

const mockArtifacts = [
  {
    id: 'artifact-1',
    service_id: 'svc-1',
    build_id: 'build-1',
    image_repo: 'ghcr.io/acme/api-service',
    image_tag: 'v1.2.3',
    image_digest: 'sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890',
    size_bytes: 12582912,
    created_at: '2026-05-01T10:00:00.000Z',
    metadata: { build_id: 'build-1' }
  }
];

const mockEnvironments = [
  { id: 'env-prod', name: 'production', protected: true },
  { id: 'env-stage', name: 'staging', protected: false }
];

test.describe('Service Detail Edit/Delete Actions', () => {
  test.beforeEach(async ({ page }) => {
    await installE2EMocks(page);

    await page.route('**/api/v1/services/svc-1*', async (route) => {
      const method = route.request().method();

      if (method === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            data: {
              id: 'svc-1',
              name: 'api-service',
              repo_url: 'https://github.com/acme/api-service',
              artifact_repo: 'ghcr.io/acme/api-service',
              runtime_type: 'docker',
              default_branch: 'main',
              created_at: new Date().toISOString()
            }
          })
        });
      }

      if (method === 'PUT') {
        const body = route.request().postDataJSON();
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ data: { id: 'svc-1', ...body } })
        });
      }

      if (method === 'DELETE') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ data: null })
        });
      }

      return route.fulfill({ status: 404, body: JSON.stringify({ error: 'Not found' }) });
    });

    await page.route('**/api/v1/services/svc-1/builds', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: mockBuilds }) })
    );
    await page.route('**/api/v1/services/svc-1/artifacts', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: mockArtifacts }) })
    );
    await page.route('**/api/v1/services/svc-1/secrets', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [] }) })
    );

    await page.route('**/api/v1/repositories*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [] }) })
    );

    await page.route('**/api/v1/services', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [] }) })
    );
    await page.route('**/api/v1/environments', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: mockEnvironments }) })
    );
    await page.route('**/api/v1/environments/env-prod', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: mockEnvironments[0] }) })
    );
    await page.route('**/api/v1/state', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [] }) })
    );
    await page.route('**/api/v1/workers', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [] }) })
    );

    await page.route('**/api/v1/system/info', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          data: {
            registries: [],
            nostr: { browser_relays: [], service_pubkey: 'b'.repeat(64) },
            features: {
              relay_sidecar: true,
              relay_read_models: true,
              legacy_sse: false,
              direct_nostr_http_auth: true
            }
          }
        })
      })
    );

    await page.route('**/api/v1/policies/evaluate', async (route) => {
      expect(route.request().headers().authorization).toMatch(/^Nostr\s+/);
      const body = route.request().postDataJSON();
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          data: {
            allowed: true,
            warnings: 0,
            blockers: 0,
            results: [
              {
                policy_id: 'policy-signatures',
                policy_name: 'Signature required',
                passed: true,
                enforcement: 'block',
                request: body
              }
            ]
          }
        })
      });
    });

    await page.route('**/api/v1/deployments/intents', async (route) => {
      if (route.request().method() !== 'POST') {
        return route.fallback();
      }
      expect(route.request().headers().authorization).toMatch(/^Nostr\s+/);
      const body = route.request().postDataJSON();
      return route.fulfill({
        status: 201,
        contentType: 'application/json',
        body: JSON.stringify({
          data: {
            id: 'intent-1',
            ...body,
            approval_status: 'pending',
            status: 'pending',
            created_at: '2026-05-02T12:00:00.000Z'
          }
        })
      });
    });

  });

  test('opens edit modal with pre-populated values and saves', async ({ page }) => {

    await page.goto('/services/svc-1');
    await expect(page.getByRole('heading', { name: 'api-service' })).toBeVisible();

    await page.getByRole('button', { name: 'Edit' }).click();
    await expect(page.getByRole('dialog', { name: 'Edit Service' })).toBeVisible();

    await expect(page.locator('#edit-name')).toHaveValue('api-service');
    await expect(page.locator('#edit-artifact-repo')).toHaveValue('ghcr.io/acme/api-service');
    await expect(page.locator('#edit-runtime-type')).toHaveValue('docker');
    await expect(page.locator('#edit-default-branch')).toHaveValue('main');

    await page.locator('#edit-name').fill('api-service-renamed');
    const putRequestPromise = page.waitForRequest(
      (request) => request.method() === 'PUT' && request.url().includes('/api/v1/services/svc-1')
    );
    await page.getByRole('button', { name: 'Save' }).click();

    const putPayload = (await putRequestPromise).postDataJSON();
    expect(putPayload).toMatchObject({
      name: 'api-service-renamed',
      artifact_repo: 'ghcr.io/acme/api-service',
      runtime_type: 'docker',
      default_branch: 'main'
    });
    await expect(page.getByRole('dialog', { name: 'Edit Service' })).not.toBeVisible();
  });

  test('opens deploy intent modal, previews policy and creates intent', async ({ page }) => {
    await page.goto('/services/svc-1');
    await expect(page.getByRole('heading', { name: 'api-service' })).toBeVisible();

    await page.getByRole('button', { name: 'Deploy' }).click();
    await expect(page.getByRole('dialog', { name: 'Create Deployment Intent' })).toBeVisible();
    await expect(page.getByLabel('Environment *')).toBeVisible();
    await expect(page.getByLabel('Artifact from recent builds *')).toBeVisible();

    await page.locator('#deploy-environment').selectOption('env-prod');
    const policyRequestPromise = page.waitForRequest(
      (request) => request.method() === 'POST' && request.url().includes('/api/v1/policies/evaluate')
    );
    await page.locator('#deploy-artifact').selectOption('artifact-1');

    const policyPayload = (await policyRequestPromise).postDataJSON();
    expect(policyPayload).toEqual({ artifact_id: 'artifact-1', environment_id: 'env-prod' });

    const dialog = page.getByRole('dialog', { name: 'Create Deployment Intent' });
    await expect(dialog.getByText('Selection Summary')).toBeVisible();
    await expect(dialog.locator('.deploy-summary').getByText('production')).toBeVisible();
    await expect(dialog.locator('.deploy-summary').getByText(/v1\.2\.3/)).toBeVisible();
    await expect(dialog.getByText('abcdef1 · refs/heads/main')).toBeVisible();
    await expect(dialog.getByText('Allowed')).toBeVisible();
    await expect(page.getByText('Signature required')).toBeVisible();
    await expect(page.getByText(/Cost estimate unavailable before run creation/)).toBeVisible();

    const createRequestPromise = page.waitForRequest(
      (request) => request.method() === 'POST' && request.url().endsWith('/api/v1/deployments/intents')
    );
    await page.getByRole('button', { name: 'Create Intent' }).click();

    const createPayload = (await createRequestPromise).postDataJSON();
    expect(createPayload).toEqual({
      service_id: 'svc-1',
      environment_id: 'env-prod',
      artifact_id: 'artifact-1'
    });
    await expect(page).toHaveURL(/\/deployments$/);
  });

  test('shows delete confirmation warning and supports force delete', async ({ page }) => {

    await page.goto('/services/svc-1');
    await expect(page.getByRole('heading', { name: 'api-service' })).toBeVisible();

    await page.getByRole('button', { name: 'Delete' }).click();

    await expect(page.getByRole('dialog', { name: 'Delete Service' })).toBeVisible();
    await expect(page.getByText('This action cannot be undone.')).toBeVisible();
    await expect(
      page.getByText('Deleting this service will cascade to related resources (such as secrets, artifacts, and deployment records).')
    ).toBeVisible();

    await page.locator('#delete-force').check();
    const deleteRequestPromise = page.waitForRequest(
      (request) => request.method() === 'DELETE' && request.url().includes('/api/v1/services/svc-1?force=true')
    );
    await page.getByRole('dialog', { name: 'Delete Service' }).getByRole('button', { name: 'Delete' }).click();

    const deleteRequest = await deleteRequestPromise;
    expect(deleteRequest.url()).toContain('/api/v1/services/svc-1?force=true');
    await expect(page).toHaveURL(/\/services$/);
  });
});
