import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

// Mock API responses with Bahia envelope shape
test.beforeEach(async ({ page }) => {
  await installE2EMocks(page);

  // Mock services list - initially empty
  await page.route('**/api/v1/services', (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: [] })
      });
    } else if (route.request().method() === 'POST') {
      // Capture the POST request and return success
      const postData = route.request().postDataJSON();
      return route.fulfill({
        status: 201,
        contentType: 'application/json',
        body: JSON.stringify({
          data: {
            id: 'test-service-id',
            ...postData,
            created_at: new Date().toISOString()
          }
        })
      });
    }

    return route.fulfill({
      status: 404,
      contentType: 'application/json',
      body: JSON.stringify({ error: 'Not found' })
    });
  });

});

test.describe('Services CRUD Smoke Test', () => {
  test('should create a service via modal form', async ({ page }) => {
    // Track API calls
    const apiCalls = {
      post: null
    };

    await page.route('**/api/v1/services', (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ data: [] })
        });
      } else if (route.request().method() === 'POST') {
        apiCalls.post = route.request().postDataJSON();
        return route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify({
            data: {
              id: 'test-service-123',
              ...route.request().postDataJSON(),
              created_at: new Date().toISOString()
            }
          })
        });
      }
    });

    // Navigate to services page
    await page.goto('/services');
    await page.waitForLoadState('networkidle');

    // Open Create Service modal
    await page.click('text=Create Service');

    // Wait for modal to appear
    await expect(page.getByRole('dialog', { name: 'Create Service' })).toBeVisible();

    // Fill out the form
    await page.fill('#service-name', 'test-service');
    await page.fill('#artifact-repo-path', 'ghcr.io/test/test-service');
    await page.selectOption('#runtime-type', 'docker');
    await page.fill('#default-branch', 'main');

    // Repository selection flow is available in modal
    await expect(page.getByRole('button', { name: 'Choose Repository' })).toBeVisible();

    // Submit the form
    await page.click('button[type="submit"]:has-text("Create")');

    // Wait for modal to close (request + refresh done)
    await expect(page.getByRole('dialog', { name: 'Create Service' })).not.toBeVisible();

    // Verify POST was called with correct data
    expect(apiCalls.post).not.toBeNull();
    expect(apiCalls.post).toMatchObject({
      name: 'test-service',
      repo_url: '',
      artifact_repo: 'ghcr.io/test/test-service',
      runtime_type: 'docker',
      default_branch: 'main'
    });
  });

  test('should show validation error for empty name', async ({ page }) => {
    await page.goto('/services');
    await page.waitForLoadState('networkidle');

    // Open modal
    await page.click('text=Create Service');

    // Try to submit without filling required fields
    await page.fill('#artifact-repo-path', 'ghcr.io/test/test-service');
    await page.click('button[type="submit"]:has-text("Create")');

    // Should show validation error
    await expect(page.getByRole('dialog', { name: 'Create Service' })).toBeVisible();
  });

  test('should show validation error for empty artifact repo', async ({ page }) => {
    await page.goto('/services');
    await page.waitForLoadState('networkidle');

    // Open modal
    await page.click('text=Create Service');

    // Fill only name
    await page.fill('#service-name', 'test-service');
    await page.click('button[type="submit"]:has-text("Create")');

    // Should show validation error
    await expect(page.getByRole('dialog', { name: 'Create Service' })).toBeVisible();
  });

  test('should close modal on cancel', async ({ page }) => {
    await page.goto('/services');
    await page.waitForLoadState('networkidle');

    // Open modal
    await page.click('text=Create Service');
    await expect(page.getByRole('dialog', { name: 'Create Service' })).toBeVisible();

    // Click cancel
    await page.click('button:has-text("Cancel")');

    // Modal should be closed
    await expect(page.locator('text=Artifact Repository')).not.toBeVisible();
  });

  test('should support search, runtime filtering, and pagination controls', async ({ page }) => {
    const services = Array.from({ length: 30 }, (_, index) => ({
      id: `service-${index + 1}`,
      name: `service-${index + 1}`,
      artifact_repo: `ghcr.io/acme/service-${index + 1}`,
      runtime_type: index < 15 ? 'docker' : 'kubernetes',
      default_branch: 'main'
    }));

    await page.unroute('**/api/v1/services');
    await page.route('**/api/v1/services', (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ data: services })
        });
      }

      return route.fulfill({
        status: 201,
        contentType: 'application/json',
        body: JSON.stringify({ data: { id: 'created-service' } })
      });
    });

    await page.goto('/services');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('#service-search')).toBeVisible();
    await expect(page.locator('#runtime-filter')).toBeVisible();
    await expect(page.locator('#page-size')).toBeVisible();

    await expect(page.locator('tbody tr')).toHaveCount(25);
    await expect(page.locator('text=Page 1 of 2')).toBeVisible();

    await page.selectOption('#page-size', '10');
    await expect(page.locator('text=Page 1 of 3')).toBeVisible();
    await expect(page.locator('tbody tr')).toHaveCount(10);

    await page.getByRole('button', { name: 'Next' }).click();
    await expect(page.locator('text=Page 2 of 3')).toBeVisible();

    await page.fill('#service-search', 'service-2');
    await expect(page.locator('tbody tr')).toHaveCount(10);
    await expect(page.locator('text=Page 1 of 2')).toBeVisible();

    await page.selectOption('#runtime-filter', 'kubernetes');
    await expect(page.locator('tbody tr')).toHaveCount(10);
    await expect(page.locator('text=Page 1 of 1')).not.toBeVisible();

    await page.selectOption('#page-size', '50');
    await expect(page.locator('tbody tr')).toHaveCount(10);
    await expect(page.locator('text=Page 1 of 1')).not.toBeVisible();
  });
});
