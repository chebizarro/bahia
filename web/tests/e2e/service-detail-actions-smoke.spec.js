import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

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
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [] }) })
    );
    await page.route('**/api/v1/services/svc-1/artifacts', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [] }) })
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
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [] }) })
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
        body: JSON.stringify({ data: { registries: [] } })
      })
    );
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
