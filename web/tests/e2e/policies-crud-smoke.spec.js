import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

const mockEnvironments = [
  {
    id: 'env-1',
    name: 'production',
    loom_worker_selector: 'role=prod'
  },
  {
    id: 'env-2',
    name: 'staging',
    loom_worker_selector: 'role=staging'
  }
];

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page);

  // Mock policies list - initially empty
  await page.route('**/api/v1/policies', (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: [] })
      });
    } else if (route.request().method() === 'POST') {
      // Return created policy
      const postData = route.request().postDataJSON();
      return route.fulfill({
        status: 201,
        contentType: 'application/json',
        body: JSON.stringify({
          data: {
            id: 'test-policy-id',
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
  
  // Mock environments
  await page.route('**/api/v1/environments', (route) => {
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: mockEnvironments })
    });
  });
  
});

test.describe('Policies CRUD Smoke Test', () => {
  test('should create a policy with JSON rules', async ({ page }) => {
    const apiCalls = {
      post: null
    };
    
    await page.route('**/api/v1/policies', (route) => {
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
              id: 'policy-123',
              ...route.request().postDataJSON(),
              created_at: new Date().toISOString()
            }
          })
        });
      }
    });
    
    await page.goto('/policies');
    await page.waitForLoadState('networkidle');
    
    // Open Create Policy modal
    await page.click('text=Create Policy');
    
    // Wait for modal
    await expect(page.getByRole('dialog', { name: 'Create Policy' })).toBeVisible();
    
    // Fill form
    await page.fill('#policy-name', 'require-sbom-policy');
    await page.selectOption('#enforcement', 'block');
    
    // Fill JSON rules
    const rules = [
      { type: 'require_sbom' },
      { type: 'require_signature' }
    ];
    await page.fill('#rules', JSON.stringify(rules));
    
    // Submit
    await page.click('button[type="submit"]:has-text("Create")');
    
    // Wait for request
    await page.waitForTimeout(500);
    
    // Verify POST payload
    expect(apiCalls.post).not.toBeNull();
    expect(apiCalls.post).toMatchObject({
      name: 'require-sbom-policy',
      enforcement: 'block',
      enabled: true,
      rules: [
        { type: 'require_sbom' },
        { type: 'require_signature' }
      ]
    });
    
    // environment_id should not be included for global policies
    expect(apiCalls.post.environment_id).toBeUndefined();
  });
  
  test('should create a scoped policy for specific environment', async ({ page }) => {
    const apiCalls = {
      post: null
    };
    
    await page.route('**/api/v1/policies', (route) => {
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
              id: 'policy-456',
              ...route.request().postDataJSON(),
              created_at: new Date().toISOString()
            }
          })
        });
      }
    });
    
    await page.goto('/policies');
    await page.waitForLoadState('networkidle');
    
    // Open modal
    await page.click('text=Create Policy');
    await expect(page.getByRole('dialog', { name: 'Create Policy' })).toBeVisible();
    
    // Fill form with environment scope
    await page.fill('#policy-name', 'prod-policy');
    await page.selectOption('#environment-id', 'env-1'); // Production environment
    await page.selectOption('#enforcement', 'warn');
    await page.fill('#rules', '[{"type": "require_approval"}]');
    
    // Submit
    await page.click('button[type="submit"]:has-text("Create")');
    await page.waitForTimeout(500);
    
    // Verify payload includes environment_id
    expect(apiCalls.post).not.toBeNull();
    expect(apiCalls.post).toMatchObject({
      name: 'prod-policy',
      environment_id: 'env-1',
      enforcement: 'warn',
      enabled: true,
      rules: [{ type: 'require_approval' }]
    });
  });
  
  test('should show validation error for invalid JSON rules', async ({ page }) => {
    await page.goto('/policies');
    await page.waitForLoadState('networkidle');
    
    // Open modal
    await page.click('text=Create Policy');
    
    // Fill form with invalid JSON
    await page.fill('#policy-name', 'test-policy');
    await page.fill('#rules', 'not valid json');
    
    // Submit
    await page.click('button[type="submit"]:has-text("Create")');
    
    // Should show validation error
    await expect(page.locator('text=Rules must be valid JSON')).toBeVisible();
  });
  
  test('should show validation error for non-array JSON rules', async ({ page }) => {
    await page.goto('/policies');
    await page.waitForLoadState('networkidle');
    
    // Open modal
    await page.click('text=Create Policy');
    
    // Fill form with JSON object instead of array
    await page.fill('#policy-name', 'test-policy');
    await page.fill('#rules', '{"type": "require_sbom"}');
    
    // Submit
    await page.click('button[type="submit"]:has-text("Create")');
    
    // Should show validation error
    await expect(page.locator('text=Rules must be a JSON array')).toBeVisible();
  });
  
  test('should show validation error for empty name', async ({ page }) => {
    await page.goto('/policies');
    await page.waitForLoadState('networkidle');
    
    // Open modal
    await page.click('text=Create Policy');
    
    // Try to submit without name
    await page.fill('#rules', '[]');
    await page.click('button[type="submit"]:has-text("Create")');
    
    // Should show validation error
    await expect(page.getByRole('dialog', { name: 'Create Policy' })).toBeVisible();
  });
  
  test('should close modal on cancel', async ({ page }) => {
    await page.goto('/policies');
    await page.waitForLoadState('networkidle');
    
    // Open modal
    await page.click('text=Create Policy');
    await expect(page.locator('#policy-name')).toBeVisible();
    
    // Click cancel
    await page.click('button:has-text("Cancel")');
    
    // Modal should be closed
    await expect(page.locator('#policy-name')).not.toBeVisible();
  });
  
  test('should toggle enabled checkbox', async ({ page }) => {
    const apiCalls = {
      post: null
    };
    
    await page.route('**/api/v1/policies', (route) => {
      if (route.request().method() === 'POST') {
        apiCalls.post = route.request().postDataJSON();
        return route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify({
            data: {
              id: 'policy-789',
              ...route.request().postDataJSON()
            }
          })
        });
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: [] })
      });
    });
    
    await page.goto('/policies');
    await page.waitForLoadState('networkidle');
    
    // Open modal
    await page.click('text=Create Policy');
    
    // Fill form
    await page.fill('#policy-name', 'disabled-policy');
    await page.fill('#rules', '[]');
    
    // Uncheck enabled
    await page.uncheck('#enabled');
    
    // Submit
    await page.click('button[type="submit"]:has-text("Create")');
    await page.waitForTimeout(500);
    
    // Verify enabled is false
    expect(apiCalls.post).not.toBeNull();
    expect(apiCalls.post.enabled).toBe(false);
  });
});
