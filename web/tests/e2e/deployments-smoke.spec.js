import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

// Mock data
const mockServices = [
  {
    id: 'service-1',
    name: 'web-app',
    artifact_repo: 'ghcr.io/test/web-app',
    runtime_type: 'docker',
    default_branch: 'main'
  },
  {
    id: 'service-2',
    name: 'api-service',
    artifact_repo: 'ghcr.io/test/api',
    runtime_type: 'docker',
    default_branch: 'main'
  }
];

const mockEnvironments = [
  {
    id: 'env-1',
    name: 'production',
    loom_worker_selector: 'role=prod',
    protected: true
  },
  {
    id: 'env-2',
    name: 'staging',
    loom_worker_selector: 'role=staging',
    protected: false
  }
];

const mockPendingIntents = [
  {
    id: 'intent-1',
    service_id: 'service-1',
    environment_id: 'env-1',
    artifact_id: 'sha256:abc123def456',
    approval_status: 'pending',
    requested_by: 'alice@example.com',
    created_at: new Date().toISOString()
  },
  {
    id: 'intent-2',
    service_id: 'service-2',
    environment_id: 'env-2',
    artifact_id: 'sha256:789ghi012jkl',
    approval_status: 'pending',
    requested_by: 'bob@example.com',
    created_at: new Date(Date.now() - 3600000).toISOString()
  }
];

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page);

  // Mock services
  await page.route('**/api/v1/services', (route) => {
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: mockServices })
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
  
  // Mock intents for each service/environment combination
  await page.route('**/api/v1/services/*/environments/*/intents', (route) => {
    const url = route.request().url();
    
    // Extract service and environment IDs from URL
    const match = url.match(/services\/([^\/]+)\/environments\/([^\/]+)\/intents/);
    if (!match) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: [] })
      });
    }
    
    const [, serviceId, envId] = match;
    
    // Return matching intents
    const intents = mockPendingIntents.filter(
      intent => intent.service_id === serviceId && intent.environment_id === envId
    );
    
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: intents })
    });
  });
  
  // Mock approval endpoint
  await page.route('**/api/v1/deployments/intents/*/approve', (route) => {
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: { status: 'approved' } })
    });
  });
  
  // Mock rejection endpoint
  await page.route('**/api/v1/deployments/intents/*/reject', (route) => {
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: { status: 'rejected' } })
    });
  });
  
});

test.describe('Deployments Smoke Test', () => {
  test('should load pending approvals page', async ({ page }) => {
    await page.goto('/deployments/pending');
    await page.waitForLoadState('networkidle');
    
    // Check page title
    await expect(page.locator('h1:has-text("Pending Approvals")')).toBeVisible();
    
    // Check that pending count is shown
    await expect(page.locator('.count:has-text("pending")')).toBeVisible();
  });
  
  test('should display pending intents in table', async ({ page }) => {
    await page.goto('/deployments/pending');
    await page.waitForLoadState('networkidle');
    
    // Wait for data to load
    await page.waitForTimeout(500);
    
    // Check that pending row is shown
    await expect(page.locator('text=web-app')).toBeVisible();
    await expect(page.locator('text=production')).toBeVisible();
  });
  
  test('should approve a deployment intent', async ({ page }) => {
    const approvedIntents = [];
    
    // Track approval calls
    await page.route('**/api/v1/deployments/intents/*/approve', (route) => {
      const intentId = route.request().url().match(/intents\/([^\/]+)\/approve/)[1];
      approvedIntents.push(intentId);
      
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: { status: 'approved' } })
      });
    });
    
    await page.goto('/deployments/pending');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);
    
    // Click the first approve button
    await page.locator('button:has-text("Approve")').first().click();
    
    // Wait for confirmation dialog
    await expect(page.getByRole('dialog', { name: 'Approve Deployment' })).toBeVisible();
    
    // Confirm approval
    await page.getByRole('dialog', { name: 'Approve Deployment' }).getByRole('button', { name: 'Approve' }).click();
    
    // Wait for API call
    await page.waitForTimeout(500);
    
    // Verify approval was called
    expect(approvedIntents.length).toBe(1);
    expect(approvedIntents[0]).toBe('intent-1');
  });
  
  test('should reject a deployment intent', async ({ page }) => {
    const rejectedIntents = [];
    
    // Track rejection calls
    await page.route('**/api/v1/deployments/intents/*/reject', (route) => {
      const intentId = route.request().url().match(/intents\/([^\/]+)\/reject/)[1];
      rejectedIntents.push(intentId);
      
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: { status: 'rejected' } })
      });
    });
    
    await page.goto('/deployments/pending');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);
    
    // Click the first reject button
    await page.locator('button:has-text("Reject")').first().click();
    
    // Wait for confirmation dialog
    await expect(page.getByRole('dialog', { name: 'Reject Deployment' })).toBeVisible();
    
    // Confirm rejection
    await page.getByRole('dialog', { name: 'Reject Deployment' }).getByRole('button', { name: 'Reject' }).click();
    
    // Wait for API call
    await page.waitForTimeout(500);
    
    // Verify rejection was called
    expect(rejectedIntents.length).toBe(1);
    expect(rejectedIntents[0]).toBe('intent-1');
  });
  
  test('should show empty state when no pending approvals', async ({ page }) => {
    // Override intents to return empty
    await page.route('**/api/v1/services/*/environments/*/intents', (route) => {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: [] })
      });
    });
    
    await page.goto('/deployments/pending');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);
    
    // Should show empty state
    await expect(page.locator('text=No pending approvals')).toBeVisible();
  });
});

test.describe('Deployment History Page', () => {
  test('should show status/service/environment/date filters and pagination on /deployments', async ({ page }) => {
    const pagedIntents = Array.from({ length: 30 }, (_, index) => ({
      id: `intent-page-${index + 1}`,
      service_id: index < 26 ? 'service-1' : 'service-2',
      environment_id: index < 28 ? 'env-1' : 'env-2',
      artifact_id: `sha256:paged${index.toString().padStart(6, '0')}`,
      approval_status: 'approved',
      deployment_status: index === 0 ? 'running' : 'succeeded',
      requested_by: `user-${index + 1}@example.com`,
      created_at: `2026-04-${String((index % 28) + 1).padStart(2, '0')}T10:00:00.000Z`
    }));

    await page.unroute('**/api/v1/services/*/environments/*/intents');
    await page.route('**/api/v1/services/*/environments/*/intents', (route) => {
      const url = route.request().url();
      const match = url.match(/services\/([^\/]+)\/environments\/([^\/]+)\/intents/);
      if (!match) {
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [] }) });
      }

      const [, serviceId, envId] = match;
      const intents = pagedIntents.filter((intent) => intent.service_id === serviceId && intent.environment_id === envId);
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: intents }) });
    });

    await page.goto('/deployments');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('h1:has-text("Deployment History")')).toBeVisible();
    await expect(page.locator('#status-filter')).toBeVisible();
    await expect(page.locator('#service-filter')).toBeVisible();
    await expect(page.locator('#environment-filter')).toBeVisible();
    await expect(page.locator('#start-date-filter')).toBeVisible();
    await expect(page.locator('#end-date-filter')).toBeVisible();

    await page.selectOption('#status-filter', 'running');
    await expect(page.locator('text=1 of 30 deployments')).toBeVisible();

    await page.selectOption('#status-filter', 'all');
    await expect(page.locator('text=30 of 30 deployments')).toBeVisible();
    await expect(page.locator('text=Page 1 of 2')).toBeVisible();
    await expect(page.locator('tbody tr')).toHaveCount(25);

    await page.getByRole('button', { name: 'Next' }).click();
    await expect(page.locator('text=Page 2 of 2')).toBeVisible();
    await expect(page.locator('tbody tr')).toHaveCount(5);

    await page.selectOption('#service-filter', 'service-2');
    await expect(page.locator('text=4 of 30 deployments')).toBeVisible();

    await page.selectOption('#environment-filter', 'env-2');
    await expect(page.locator('text=2 of 30 deployments')).toBeVisible();

    await page.fill('#start-date-filter', '2026-04-27');
    await expect(page.locator('text=No deployments match current filters')).toBeVisible();
  });
});
