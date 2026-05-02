import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

// Mock data
const mockEnvironments = [
  {
    id: 'env-1',
    name: 'production',
    loom_worker_selector: 'role=prod',
    runtime_config: { cpu_limit: '2', memory_limit: '4Gi' },
    protected: true,
    created_at: new Date().toISOString()
  },
  {
    id: 'env-2',
    name: 'staging',
    loom_worker_selector: 'role=staging',
    runtime_config: { cpu_limit: '1', memory_limit: '2Gi' },
    protected: false,
    created_at: new Date().toISOString()
  }
];

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page);

  // Mock environments list
  await page.route('**/api/v1/environments', (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: mockEnvironments })
      });
    } else if (route.request().method() === 'POST') {
      const postData = route.request().postDataJSON();
      return route.fulfill({
        status: 201,
        contentType: 'application/json',
        body: JSON.stringify({
          data: {
            id: 'env-new-123',
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
  
  // Mock individual environment detail
  await page.route('**/api/v1/environments/*', (route) => {
    const method = route.request().method();
    const url = route.request().url();
    const envId = url.match(/environments\/([^\/\?]+)/)?.[1];
    
    if (method === 'GET') {
      const env = mockEnvironments.find(e => e.id === envId);
      if (env) {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ data: env })
        });
      }
      return route.fulfill({
        status: 404,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'Environment not found' })
      });
    } else if (method === 'PUT' || method === 'PATCH') {
      const putData = route.request().postDataJSON();
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          data: {
            id: envId,
            ...putData,
            updated_at: new Date().toISOString()
          }
        })
      });
    } else if (method === 'DELETE') {
      return route.fulfill({
        status: 204,
        contentType: 'application/json',
        body: ''
      });
    }
    
    return route.fulfill({
      status: 404,
      contentType: 'application/json',
      body: JSON.stringify({ error: 'Not found' })
    });
  });
  
  await page.route('**/api/v1/state', (route) => {
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: [] })
    });
  });
  
  
  // Mock other common endpoints
  await page.route('**/api/v1/services', (route) => {
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: [] })
    });
  });
});

test.describe('Environments CRUD Smoke Test', () => {
  test('should load environments page', async ({ page }) => {
    await page.goto('/environments');
    await page.waitForLoadState('networkidle');
    
    // Check page renders
    await expect(page.locator('h1:has-text("Environments")')).toBeVisible();
    
    // Check environments are listed
    await expect(page.locator('text=production')).toBeVisible();
    await expect(page.locator('text=staging')).toBeVisible();
  });
  
  test('should open Create Environment modal', async ({ page }) => {
    await page.goto('/environments');
    await page.waitForLoadState('networkidle');

    // Click Create Environment button
    await page.click('text=Create Environment');

    // Wait for modal to appear
    await expect(page.getByRole('dialog', { name: 'Create Environment' })).toBeVisible();

    // Modal should have required fields
    await expect(page.getByLabel('Name *')).toBeVisible();
    await expect(page.getByLabel('Loom Worker Selector')).toBeVisible();
    await expect(page.getByLabel('Runtime Config (JSON)')).toBeVisible();
    await expect(page.getByLabel('Deploy Strategy *')).toBeVisible();
    await expect(page.getByLabel('Protected (requires approval for deployments)')).toBeVisible();

    // Deploy strategy options should match product language
    const strategySelect = page.getByLabel('Deploy Strategy *');
    await strategySelect.selectOption('rolling');
    await expect(strategySelect).toHaveValue('rolling');
    await strategySelect.selectOption('blue-green');
    await expect(strategySelect).toHaveValue('blue-green');
    await strategySelect.selectOption('canary');
    await expect(strategySelect).toHaveValue('canary');
  });
  
  test('should create environment with valid JSON runtime config', async ({ page }) => {
    const apiCalls = {
      post: null
    };
    
    await page.route('**/api/v1/environments', (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ data: mockEnvironments })
        });
      } else if (route.request().method() === 'POST') {
        apiCalls.post = route.request().postDataJSON();
        return route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify({
            data: {
              id: 'env-new-123',
              ...route.request().postDataJSON(),
              created_at: new Date().toISOString()
            }
          })
        });
      }
    });
    
    await page.goto('/environments');
    await page.waitForLoadState('networkidle');
    
    // Open Create Environment modal
    await page.click('text=Create Environment');
    await page.waitForTimeout(300);
    
    // Fill out the form
    await page.getByLabel('Name *').fill('development');
    await page.getByLabel('Loom Worker Selector').fill('role=dev');
    
    // Fill runtime config as JSON
    const runtimeConfigField = page.locator('#runtime-config, [name="runtime_config"], textarea[placeholder*="JSON"], textarea[placeholder*="runtime"]').first();
    await runtimeConfigField.fill('{"cpu_limit":"1","memory_limit":"1Gi"}');
    
    // Select strategy and set protected toggle
    await page.getByLabel('Deploy Strategy *').selectOption('blue-green');
    await page.getByLabel('Protected (requires approval for deployments)').check();

    // Submit the form
    await page.click('button[type="submit"]:has-text("Create")');
    
    // Wait for the request to complete
    await page.waitForTimeout(500);
    
    // Verify POST was called with correct data
    expect(apiCalls.post).not.toBeNull();
    expect(apiCalls.post).toMatchObject({
      name: 'development',
      loom_worker_selector: 'role=dev',
      deploy_strategy: 'blue_green',
      protected: true
    });

    // Verify runtime_config is parsed JSON
    expect(apiCalls.post.runtime_config).toEqual({ cpu_limit: '1', memory_limit: '1Gi' });
  });
  
  test('should navigate to environment detail page', async ({ page }) => {
    await page.goto('/environments');
    await page.waitForLoadState('networkidle');
    
    // Verify the row renders, then load the detail route directly. The table row
    // click behavior is covered by component tests; this smoke test focuses on route rendering.
    await expect(page.getByRole('row', { name: /production/ })).toBeVisible();
    await page.goto('/environments/env-1');
    await page.waitForLoadState('networkidle');
    
    // Verify URL contains environment ID
    expect(page.url()).toMatch(/\/environments\/env-1/);
  });
  
  test('should show edit environment option on detail page', async ({ page }) => {
    await page.goto('/environments/env-1');
    await page.waitForLoadState('networkidle');
    
    // Should show environment name
    await expect(page.locator('text=production')).toBeVisible();
    
    // Should have an edit button or action
    const editButton = page.locator('button:has-text("Edit"), a:has-text("Edit")');
    await expect(editButton.first()).toBeVisible();
  });
  
  test('should update environment', async ({ page }) => {
    const apiCalls = {
      put: null
    };
    
    await page.route('**/api/v1/environments/env-1', (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ data: mockEnvironments[0] })
        });
      } else if (route.request().method() === 'PUT' || route.request().method() === 'PATCH') {
        apiCalls.put = route.request().postDataJSON();
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            data: {
              ...mockEnvironments[0],
              ...route.request().postDataJSON(),
              updated_at: new Date().toISOString()
            }
          })
        });
      }
    });
    
    await page.goto('/environments/env-1');
    await page.waitForLoadState('networkidle');
    
    // Click edit button
    await page.click('button:has-text("Edit")');
    await page.waitForTimeout(300);
    
    // Modify a field
    await page.getByLabel('Loom Worker Selector').fill('role=prod-updated');
    
    // Submit
    await page.click('button[type="submit"]:has-text("Save"), button[type="submit"]:has-text("Update")');
    
    // Wait for the request
    await page.waitForTimeout(500);
    
    // Verify PUT/PATCH was called
    expect(apiCalls.put).not.toBeNull();
  });
  
  test('should show delete environment confirmation', async ({ page }) => {
    await page.goto('/environments/env-2');
    await page.waitForLoadState('networkidle');
    
    // Click delete button
    await page.click('button:has-text("Delete")');
    
    // Should show confirmation dialog
    await expect(page.getByRole('dialog', { name: 'Delete Environment' })).toBeVisible();
  });
  
  test('should delete environment after confirmation', async ({ page }) => {
    const apiCalls = {
      delete: false
    };
    
    await page.route('**/api/v1/environments/env-2', (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ data: mockEnvironments[1] })
        });
      } else if (route.request().method() === 'DELETE') {
        apiCalls.delete = true;
        return route.fulfill({
          status: 204,
          contentType: 'application/json',
          body: ''
        });
      }
    });
    
    await page.goto('/environments/env-2');
    await page.waitForLoadState('networkidle');
    
    // Click delete
    await page.click('button:has-text("Delete")');
    await page.waitForTimeout(300);
    
    // Confirm deletion
    await page.getByRole('dialog', { name: 'Delete Environment' }).getByRole('button', { name: 'Delete' }).click();
    
    // Wait for the request
    await page.waitForTimeout(500);
    
    // Verify DELETE was called
    expect(apiCalls.delete).toBe(true);
  });
  
  test('should cancel environment deletion', async ({ page }) => {
    await page.goto('/environments/env-2');
    await page.waitForLoadState('networkidle');
    
    // Click delete
    await page.click('button:has-text("Delete")');
    await page.waitForTimeout(300);
    
    // Click cancel
    await page.click('button:has-text("Cancel"):visible');
    
    // Confirmation dialog should be closed
    await expect(page.locator('text=Are you sure').first()).not.toBeVisible();
  });
});
