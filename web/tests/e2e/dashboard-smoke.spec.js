import { test, expect } from '@playwright/test';

// Mock data
const mockServices = [
  {
    id: 'service-1',
    name: 'web-app',
    artifact_repo: 'ghcr.io/test/web-app',
    runtime_type: 'docker'
  },
  {
    id: 'service-2',
    name: 'api-service',
    artifact_repo: 'ghcr.io/test/api',
    runtime_type: 'docker'
  },
  {
    id: 'service-3',
    name: 'worker-service',
    artifact_repo: 'ghcr.io/test/worker',
    runtime_type: 'docker'
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

const mockStates = [
  {
    id: 'state-1',
    service_id: 'service-1',
    environment_id: 'env-1',
    artifact_id: 'sha256:abc123',
    status: 'deployed',
    drift_detected: false
  },
  {
    id: 'state-2',
    service_id: 'service-1',
    environment_id: 'env-2',
    artifact_id: 'sha256:def456',
    status: 'deployed',
    drift_detected: true
  },
  {
    id: 'state-3',
    service_id: 'service-2',
    environment_id: 'env-1',
    artifact_id: 'sha256:ghi789',
    status: 'deployed',
    drift_detected: false
  }
];

const mockWorkers = [
  {
    pubkey: 'npub1worker1abc',
    status: 'online',
    last_seen: new Date().toISOString()
  },
  {
    pubkey: 'npub1worker2def',
    status: 'online',
    last_seen: new Date().toISOString()
  },
  {
    pubkey: 'npub1worker3ghi',
    status: 'offline',
    last_seen: new Date(Date.now() - 3600000).toISOString()
  }
];

const mockPendingIntents = [
  {
    id: 'intent-1',
    service_id: 'service-1',
    environment_id: 'env-1',
    artifact_id: 'sha256:new123',
    approval_status: 'pending',
    requested_by: 'alice@example.com',
    created_at: new Date().toISOString()
  },
  {
    id: 'intent-2',
    service_id: 'service-2',
    environment_id: 'env-2',
    artifact_id: 'sha256:new456',
    approval_status: 'pending',
    requested_by: 'bob@example.com',
    created_at: new Date(Date.now() - 1800000).toISOString()
  }
];

const mockEvents = [
  {
    id: 'event-1',
    type: 'deployment.started',
    timestamp: new Date().toISOString(),
    data: {
      service_id: 'service-1',
      service_name: 'web-app',
      environment_id: 'env-1',
      environment_name: 'production'
    }
  },
  {
    id: 'event-2',
    type: 'deployment.completed',
    timestamp: new Date(Date.now() - 300000).toISOString(),
    data: {
      service_id: 'service-2',
      service_name: 'api-service',
      environment_id: 'env-2',
      environment_name: 'staging',
      status: 'success'
    }
  },
  {
    id: 'event-3',
    type: 'drift.detected',
    timestamp: new Date(Date.now() - 600000).toISOString(),
    data: {
      service_id: 'service-1',
      service_name: 'web-app',
      environment_id: 'env-2',
      environment_name: 'staging'
    }
  }
];

test.beforeEach(async ({ page }) => {
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
  
  // Mock deployment states
  await page.route('**/api/v1/states', (route) => {
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: mockStates })
    });
  });
  
  // Mock workers
  await page.route('**/api/v1/workers', (route) => {
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: mockWorkers })
    });
  });
  
  // Mock deployment intents for each service/environment combination
  await page.route('**/api/v1/services/*/environments/*/intents', (route) => {
    const url = route.request().url();
    const match = url.match(/services\/([^\/]+)\/environments\/([^\/]+)\/intents/);
    
    if (!match) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: [] })
      });
    }
    
    const [, serviceId, envId] = match;
    const intents = mockPendingIntents.filter(
      intent => intent.service_id === serviceId && intent.environment_id === envId
    );
    
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: intents })
    });
  });
  
  // Mock events endpoint
  await page.route('**/api/v1/events', (route) => {
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: mockEvents })
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
});

test.describe('Dashboard Smoke Test', () => {
  test('should load dashboard page', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    
    // Dashboard should render
    const body = await page.locator('body');
    await expect(body).toBeVisible();
  });
  
  test('should display stat cards with counts', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(800);
    
    // Services stat card
    await expect(page.locator('text=Services, text=Service')).toBeVisible();
    await expect(page.locator('text=3').first()).toBeVisible();
    
    // Environments stat card
    await expect(page.locator('text=Environments, text=Environment')).toBeVisible();
    await expect(page.locator('text=2')).toBeVisible();
    
    // Workers stat card
    await expect(page.locator('text=Workers, text=Worker')).toBeVisible();
  });
  
  test('should show online workers count in stat card', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(800);
    
    // Online workers count (2 out of 3 are online in mock data)
    await expect(page.locator('text=2, text=online').first()).toBeVisible();
  });
  
  test('should show drift count in states stat card', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(800);
    
    // States with drift (1 out of 3 has drift in mock data)
    await expect(page.locator('text=Drift, text=drift')).toBeVisible();
  });
  
  test('should display pending approvals card', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(1000);
    
    // Pending approvals card should exist
    await expect(page.locator('text=Pending Approvals, text=Pending')).toBeVisible();
    
    // Should show count of pending approvals (2 in mock data)
    await expect(page.locator('text=2').first()).toBeVisible();
  });
  
  test('should link to pending deployments page from pending approvals card', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(1000);
    
    // Find link to pending deployments
    const pendingLink = page.locator('a[href="/deployments/pending"], a[href*="pending"]');
    await expect(pendingLink.first()).toBeVisible();
    
    // Click the link
    await pendingLink.first().click();
    await page.waitForLoadState('networkidle');
    
    // Verify navigation
    expect(page.url()).toMatch(/pending/);
  });
  
  test('should display quick actions section', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    
    // Quick actions heading or section
    await expect(page.locator('text=Quick Actions, text=Actions').first()).toBeVisible();
  });
  
  test('should have quick action links', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);
    
    // Common quick actions
    const createServiceLink = page.locator('a[href*="service"], button:has-text("Create Service"), text=Create Service');
    const createEnvLink = page.locator('a[href*="environment"], button:has-text("Create Environment"), text=Create Environment');
    
    // At least one quick action should exist
    const quickActions = page.locator('.quick-actions a, .actions a, button');
    await expect(quickActions.first()).toBeVisible();
  });
  
  test('should display recent activity section', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(800);
    
    // Recent activity heading
    await expect(page.locator('text=Recent Activity, text=Activity').first()).toBeVisible();
  });
  
  test('should show recent events in activity feed', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(1000);
    
    // Events should be displayed
    await expect(page.locator('text=deployment, text=drift').first()).toBeVisible();
  });
  
  test('should render event type badges in recent activity', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(1000);
    
    // Event type badges should render
    const badges = page.locator('.badge, .event-type, .type-badge, span[class*="badge"]');
    await expect(badges.first()).toBeVisible();
  });
  
  test('should show service and environment names in recent events', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(1000);
    
    // Service names from events
    await expect(page.locator('text=web-app, text=api-service').first()).toBeVisible();
    
    // Environment names from events
    await expect(page.locator('text=production, text=staging').first()).toBeVisible();
  });
  
  test('should show event timestamps in recent activity', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(1000);
    
    // Timestamps should be formatted (relative or absolute)
    const pageContent = await page.content();
    expect(pageContent.match(/\d{1,2}:\d{2}|\d{4}-\d{2}-\d{2}|ago|seconds|minutes|hours|just now/i)).toBeTruthy();
  });
  
  test('should handle empty pending approvals gracefully', async ({ page }) => {
    // Override intents to return empty
    await page.route('**/api/v1/services/*/environments/*/intents', (route) => {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: [] })
      });
    });
    
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(1000);
    
    // Should show 0 pending or "No pending approvals"
    await expect(page.locator('text=0, text=No pending').first()).toBeVisible();
  });
  
  test('should handle empty recent activity gracefully', async ({ page }) => {
    // Override events to return empty
    await page.route('**/api/v1/events', (route) => {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: [] })
      });
    });
    
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(1000);
    
    // Should show empty state
    await expect(page.locator('text=No recent activity, text=No events').first()).toBeVisible();
  });
  
  test('should handle API errors gracefully', async ({ page }) => {
    // Override services to return error
    await page.route('**/api/v1/services', (route) => {
      return route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'Internal server error' })
      });
    });
    
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);
    
    // Page should still render, possibly with error state or 0 counts
    const body = await page.locator('body');
    await expect(body).toBeVisible();
  });
  
  test('should not show console errors on dashboard load', async ({ page }) => {
    const consoleErrors = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        consoleErrors.push(msg.text());
      }
    });
    
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(1000);
    
    // Filter out expected SSE-related errors
    const unexpectedErrors = consoleErrors.filter(err => 
      !err.includes('EventSource') && 
      !err.includes('SSE') &&
      !err.includes('event stream')
    );
    
    expect(unexpectedErrors.length).toBe(0);
  });
});
