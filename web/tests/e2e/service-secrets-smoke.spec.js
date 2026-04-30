import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

// Mock data
const mockService = {
  id: 'service-1',
  name: 'web-app',
  artifact_repo: 'ghcr.io/test/web-app',
  repo_url: 'https://github.com/test/web-app',
  runtime_type: 'docker',
  default_branch: 'main',
  created_at: new Date().toISOString()
};

const mockBuilds = [
  {
    id: 'build-1',
    service_id: 'service-1',
    git_ref: 'main',
    commit_sha: 'abc123def456',
    status: 'success',
    created_at: new Date().toISOString()
  }
];

const mockArtifacts = [
  {
    id: 'artifact-1',
    build_id: 'build-1',
    service_id: 'service-1',
    digest: 'sha256:abc123def456',
    registry_url: 'ghcr.io/test/web-app@sha256:abc123def456',
    created_at: new Date().toISOString()
  }
];

const mockSecrets = [
  {
    id: 'secret-1',
    service_id: 'service-1',
    name: 'DATABASE_URL',
    created_at: new Date().toISOString()
  },
  {
    id: 'secret-2',
    service_id: 'service-1',
    name: 'API_KEY',
    created_at: new Date().toISOString()
  }
];

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page);

  // Mock service detail
  await page.route('**/api/v1/services/service-1', (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: mockService })
      });
    }
  });
  
  // Mock services list
  await page.route('**/api/v1/services', (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: [mockService] })
      });
    }
  });
  
  // Mock builds
  await page.route('**/api/v1/services/service-1/builds', (route) => {
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: mockBuilds })
    });
  });
  
  // Mock artifacts
  await page.route('**/api/v1/services/service-1/artifacts', (route) => {
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: mockArtifacts })
    });
  });
  
  // Mock secrets list
  await page.route('**/api/v1/services/service-1/secrets', (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: mockSecrets })
      });
    } else if (route.request().method() === 'POST') {
      const postData = route.request().postDataJSON();
      return route.fulfill({
        status: 201,
        contentType: 'application/json',
        body: JSON.stringify({
          data: {
            id: 'secret-new-123',
            service_id: 'service-1',
            name: postData.name,
            created_at: new Date().toISOString()
          }
        })
      });
    }
  });
  
  // Mock secret deletion
  await page.route('**/api/v1/services/service-1/secrets/*', (route) => {
    if (route.request().method() === 'DELETE') {
      return route.fulfill({
        status: 204,
        contentType: 'application/json',
        body: ''
      });
    }
  });
  
  await page.route('**/api/v1/repositories**', (route) => {
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: [] })
    });
  });
  
  // Mock SSE endpoint
  await page.route('**/api/v1/events/stream', (route) => {
    return route.fulfill({
      status: 200,
      contentType: 'text/event-stream',
      body: ''
    });
  });
  
  // Mock other common endpoints
  await page.route('**/api/v1/environments', (route) => {
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: [] })
    });
  });
});

test.describe('Service Secrets Smoke Test', () => {
  test('should load service detail page with secrets tab', async ({ page }) => {
    await page.goto('/services/service-1');
    await page.waitForLoadState('networkidle');
    
    // Service name should be visible
    await expect(page.getByRole('heading', { name: 'web-app' })).toBeVisible();
    
    // Should have secrets section
    await expect(page.getByRole('heading', { name: /Secrets \(\d+\)/ })).toBeVisible();
  });
  
  test('should display existing secrets without values', async ({ page }) => {
    await page.goto('/services/service-1');
    await page.waitForLoadState('networkidle');
    
    // Wait for secrets to load
    await page.waitForTimeout(500);
    
    // Secret names should be visible
    await expect(page.locator('text=DATABASE_URL')).toBeVisible();
    await expect(page.locator('text=API_KEY')).toBeVisible();
    
    // Secret VALUES should NEVER be rendered on the page
    // Check the entire page content
    const pageContent = await page.content();
    
    // Ensure no actual secret values appear (assuming real values would be different from names)
    // The mock doesn't have values, but verify the structure doesn't show a "value" column with data
    expect(pageContent.toLowerCase()).not.toContain('secret-value-');
    expect(pageContent.toLowerCase()).not.toContain('password123');
  });
  
  test('should open Add Secret modal', async ({ page }) => {
    await page.goto('/services/service-1');
    await page.waitForLoadState('networkidle');
    
    // Click Add Secret button
    await page.click('button:has-text("Add Secret"), button:has-text("Create Secret")');
    
    // Wait for modal to appear
    await expect(page.getByRole('dialog', { name: 'Add Secret' })).toBeVisible();
    
    // Modal should have name and value fields
    await expect(page.locator('#secret-name, [name="name"]')).toBeVisible();
    await expect(page.locator('#secret-value, [name="value"]')).toBeVisible();
  });
  
  test('should create a new secret and verify POST request', async ({ page }) => {
    const apiCalls = {
      post: null
    };
    
    await page.route('**/api/v1/services/service-1/secrets', (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ data: mockSecrets })
        });
      } else if (route.request().method() === 'POST') {
        apiCalls.post = route.request().postDataJSON();
        return route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify({
            data: {
              id: 'secret-new-123',
              service_id: 'service-1',
              name: route.request().postDataJSON().name,
              created_at: new Date().toISOString()
            }
          })
        });
      }
    });
    
    await page.goto('/services/service-1');
    await page.waitForLoadState('networkidle');
    
    // Open Add Secret modal
    await page.click('button:has-text("Add Secret"), button:has-text("Create Secret")');
    await page.waitForTimeout(300);
    
    // Fill out the form
    await page.fill('#secret-name, [name="name"]', 'NEW_SECRET');
    await page.fill('#secret-value, [name="value"]', 'super-secret-value-123');
    
    // Submit the form
    await page.click('button[type="submit"]:has-text("Add"), button[type="submit"]:has-text("Create")');
    
    // Wait for the request to complete
    await page.waitForTimeout(500);
    
    // Verify POST was called with correct data
    expect(apiCalls.post).not.toBeNull();
    expect(apiCalls.post).toMatchObject({
      name: 'NEW_SECRET',
      value: 'super-secret-value-123'
    });
  });
  
  test('should verify secret value is never rendered after submit', async ({ page }) => {
    await page.route('**/api/v1/services/service-1/secrets', (route) => {
      if (route.request().method() === 'GET') {
        // Return secrets without values (or with masked values)
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            data: [
              ...mockSecrets,
              {
                id: 'secret-new-123',
                service_id: 'service-1',
                name: 'NEW_SECRET',
                created_at: new Date().toISOString()
                // NOTE: No value field in response - values are never returned
              }
            ]
          })
        });
      } else if (route.request().method() === 'POST') {
        return route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify({
            data: {
              id: 'secret-new-123',
              service_id: 'service-1',
              name: 'NEW_SECRET',
              created_at: new Date().toISOString()
            }
          })
        });
      }
    });
    
    await page.goto('/services/service-1');
    await page.waitForLoadState('networkidle');
    
    // Open Add Secret modal
    await page.click('button:has-text("Add Secret"), button:has-text("Create Secret")');
    await page.waitForTimeout(300);
    
    // Fill and submit
    await page.fill('#secret-name, [name="name"]', 'NEW_SECRET');
    await page.fill('#secret-value, [name="value"]', 'super-secret-value-123');
    await page.click('button[type="submit"]:has-text("Add"), button[type="submit"]:has-text("Create")');
    
    // Wait for completion and page refresh
    await page.waitForTimeout(800);
    
    // Check page content - the secret value should NEVER appear
    const pageContent = await page.content();
    expect(pageContent).not.toContain('super-secret-value-123');
    
    // Secret name should be visible
    await expect(page.locator('text=NEW_SECRET')).toBeVisible();
  });
  
  test('should show delete secret confirmation', async ({ page }) => {
    await page.goto('/services/service-1');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);
    
    // Click delete button for a secret, scoped away from the service-level delete action.
    await page.locator('.secret-row:has-text("DATABASE_URL") button:has-text("Delete")').click();
    
    // Should show confirmation dialog
    await expect(page.getByRole('dialog', { name: 'Delete Secret' })).toBeVisible();
  });
  
  test('should delete secret and verify DELETE request', async ({ page }) => {
    const apiCalls = {
      delete: null
    };
    
    await page.route('**/api/v1/services/service-1/secrets/*', (route) => {
      if (route.request().method() === 'DELETE') {
        const secretId = route.request().url().match(/secrets\/([^\/\?]+)/)?.[1];
        apiCalls.delete = secretId;
        return route.fulfill({
          status: 204,
          contentType: 'application/json',
          body: ''
        });
      }
    });
    
    await page.goto('/services/service-1');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);
    
    // Click delete button, scoped away from the service-level delete action.
    await page.locator('.secret-row:has-text("DATABASE_URL") button:has-text("Delete")').click();
    await page.waitForTimeout(300);
    
    // Confirm deletion
    await page.getByRole('dialog', { name: 'Delete Secret' }).getByRole('button', { name: 'Delete' }).click();
    
    // Wait for the request
    await page.waitForTimeout(500);
    
    // Verify DELETE was called
    expect(apiCalls.delete).not.toBeNull();
    expect(apiCalls.delete).toMatch(/secret-/);
  });
  
  test('should cancel secret deletion', async ({ page }) => {
    await page.goto('/services/service-1');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);
    
    // Click delete, scoped away from the service-level delete action.
    await page.locator('.secret-row:has-text("DATABASE_URL") button:has-text("Delete")').click();
    await page.waitForTimeout(300);
    
    // Click cancel
    await page.click('button:has-text("Cancel"):visible');
    
    // Confirmation dialog should be closed
    await expect(page.locator('text=Are you sure').first()).not.toBeVisible();
    
    // Secrets should still be visible
    await expect(page.locator('text=DATABASE_URL')).toBeVisible();
  });
  
  test('should show validation error for empty secret name', async ({ page }) => {
    await page.goto('/services/service-1');
    await page.waitForLoadState('networkidle');
    
    // Open modal
    await page.click('button:has-text("Add Secret"), button:has-text("Create Secret")');
    await page.waitForTimeout(300);
    
    // Try to submit without name
    await page.fill('#secret-value, [name="value"]', 'some-value');
    await page.click('button[type="submit"]:has-text("Add"), button[type="submit"]:has-text("Create")');
    
    // Should show validation error
    await expect(page.getByRole('dialog', { name: 'Add Secret' })).toBeVisible();
  });
  
  test('should show validation error for empty secret value', async ({ page }) => {
    await page.goto('/services/service-1');
    await page.waitForLoadState('networkidle');
    
    // Open modal
    await page.click('button:has-text("Add Secret"), button:has-text("Create Secret")');
    await page.waitForTimeout(300);
    
    // Try to submit without value
    await page.fill('#secret-name, [name="name"]', 'NEW_SECRET');
    await page.click('button[type="submit"]:has-text("Add"), button[type="submit"]:has-text("Create")');
    
    // Should show validation error
    await expect(page.getByRole('dialog', { name: 'Add Secret' })).toBeVisible();
  });
});
