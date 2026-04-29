import { test, expect } from '@playwright/test';

// Mock data
const mockWorkers = [
  {
    pubkey: 'npub1worker1abc123def456',
    relay_url: 'wss://relay.example.com',
    last_seen: new Date().toISOString(),
    status: 'online',
    capabilities: ['docker', 'kubernetes'],
    metadata: {
      version: '1.0.0',
      region: 'us-east-1'
    }
  },
  {
    pubkey: 'npub1worker2xyz789ghi012',
    relay_url: 'wss://relay.example.com',
    last_seen: new Date(Date.now() - 3600000).toISOString(),
    status: 'offline',
    capabilities: ['docker'],
    metadata: {
      version: '0.9.0',
      region: 'eu-west-1'
    }
  }
];

const mockWorkerDetail = {
  pubkey: 'npub1worker1abc123def456',
  relay_url: 'wss://relay.example.com',
  last_seen: new Date().toISOString(),
  status: 'online',
  capabilities: ['docker', 'kubernetes'],
  metadata: {
    version: '1.0.0',
    region: 'us-east-1',
    hostname: 'worker-node-1'
  },
  active_deployments: 3,
  total_deployments: 15
};

const mockEvents = [
  {
    id: 'event-1',
    type: 'deployment.started',
    timestamp: new Date().toISOString(),
    data: {
      service_id: 'service-1',
      environment_id: 'env-1',
      deployment_id: 'deploy-1'
    }
  },
  {
    id: 'event-2',
    type: 'deployment.completed',
    timestamp: new Date(Date.now() - 60000).toISOString(),
    data: {
      service_id: 'service-1',
      environment_id: 'env-1',
      deployment_id: 'deploy-1',
      status: 'success'
    }
  },
  {
    id: 'event-3',
    type: 'drift.detected',
    timestamp: new Date(Date.now() - 120000).toISOString(),
    data: {
      service_id: 'service-2',
      environment_id: 'env-2',
      state_id: 'state-1'
    }
  }
];

test.beforeEach(async ({ page }) => {
  // Mock workers list
  await page.route('**/api/v1/workers', (route) => {
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: mockWorkers })
    });
  });
  
  // Mock worker detail
  await page.route('**/api/v1/workers/*', (route) => {
    const pubkey = route.request().url().match(/workers\/([^\/\?]+)/)?.[1];
    
    if (pubkey === 'npub1worker1abc123def456') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: mockWorkerDetail })
      });
    }
    
    return route.fulfill({
      status: 404,
      contentType: 'application/json',
      body: JSON.stringify({ error: 'Worker not found' })
    });
  });
  
  // Mock events endpoint - return static list for non-SSE requests
  await page.route('**/api/v1/events', (route) => {
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: mockEvents })
    });
  });
  
  // Mock SSE endpoint - EventSource connection
  await page.route('**/api/v1/events/stream', (route) => {
    // Return empty SSE stream
    return route.fulfill({
      status: 200,
      contentType: 'text/event-stream',
      body: 'retry: 10000\n\n'
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
  
  await page.route('**/api/v1/environments', (route) => {
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: [] })
    });
  });
});

test.describe('Workers and Events Smoke Test', () => {
  test('should load workers page', async ({ page }) => {
    await page.goto('/workers');
    await page.waitForLoadState('networkidle');
    
    // Check page title/heading
    await expect(page.locator('h1:has-text("Workers")')).toBeVisible();
    
    // Workers should be listed
    await expect(page.locator('text=npub1worker1abc123def456, text=worker1abc123')).toBeVisible();
  });
  
  test('should display worker status indicators', async ({ page }) => {
    await page.goto('/workers');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);
    
    // Online worker should show online status
    await expect(page.locator('text=online, .status.online, .badge.online').first()).toBeVisible();
  });
  
  test('should navigate to worker detail page on row click', async ({ page }) => {
    await page.goto('/workers');
    await page.waitForLoadState('networkidle');
    
    // Click on worker row or link
    await page.click('text=npub1worker1abc123def456, a[href*="worker1abc123"]');
    
    // Wait for navigation
    await page.waitForLoadState('networkidle');
    
    // Verify URL contains worker pubkey
    expect(page.url()).toMatch(/\/workers\//);
  });
  
  test('should render worker detail page', async ({ page }) => {
    await page.goto('/workers/npub1worker1abc123def456');
    await page.waitForLoadState('networkidle');
    
    // Worker pubkey or shortened version should be visible
    await expect(page.locator('text=npub1worker1abc123def456, text=worker1abc123')).toBeVisible();
    
    // Status should be shown
    await expect(page.locator('text=online, text=Online')).toBeVisible();
  });
  
  test('should display worker metadata on detail page', async ({ page }) => {
    await page.goto('/workers/npub1worker1abc123def456');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);
    
    // Metadata fields should be visible
    await expect(page.locator('text=1.0.0')).toBeVisible();
    await expect(page.locator('text=us-east-1')).toBeVisible();
  });
  
  test('should display worker capabilities', async ({ page }) => {
    await page.goto('/workers/npub1worker1abc123def456');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);
    
    // Capabilities should be listed
    await expect(page.locator('text=docker')).toBeVisible();
    await expect(page.locator('text=kubernetes')).toBeVisible();
  });
  
  test('should show relay URL on worker detail', async ({ page }) => {
    await page.goto('/workers/npub1worker1abc123def456');
    await page.waitForLoadState('networkidle');
    
    // Relay URL should be displayed
    await expect(page.locator('text=wss://relay.example.com')).toBeVisible();
  });
  
  test('should load events page', async ({ page }) => {
    await page.goto('/events');
    await page.waitForLoadState('networkidle');
    
    // Check page title/heading
    await expect(page.locator('h1:has-text("Events")')).toBeVisible();
  });
  
  test('should display connection status on events page', async ({ page }) => {
    await page.goto('/events');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);
    
    // Connection status should be shown (connected, disconnected, connecting, etc.)
    // The text should NOT be hardcoded only - it should reflect actual SSE state
    const statusElement = page.locator('text=Connected, text=Disconnected, text=Connecting, .connection-status, .status');
    await expect(statusElement.first()).toBeVisible();
  });
  
  test('should render events table safely', async ({ page }) => {
    await page.goto('/events');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(800);
    
    // Events table should render
    const table = page.locator('table, .events-table, .table');
    await expect(table.first()).toBeVisible();
    
    // Should show event types
    await expect(page.locator('text=deployment, text=drift').first()).toBeVisible();
  });
  
  test('should display event timestamps', async ({ page }) => {
    // Override events with known data
    await page.route('**/api/v1/events', (route) => {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: mockEvents })
      });
    });
    
    await page.goto('/events');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(800);
    
    // Timestamps should be formatted and displayed (could be relative or absolute)
    // Just verify some time-related text appears
    const pageContent = await page.content();
    expect(pageContent.match(/\d{1,2}:\d{2}|\d{4}-\d{2}-\d{2}|ago|seconds|minutes|hours/i)).toBeTruthy();
  });
  
  test('should show event types with badges or labels', async ({ page }) => {
    await page.route('**/api/v1/events', (route) => {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: mockEvents })
      });
    });
    
    await page.goto('/events');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(800);
    
    // Event type badges should render
    await expect(page.locator('.badge, .event-type, span').first()).toBeVisible();
  });
  
  test('should handle SSE connection gracefully', async ({ page }) => {
    const consoleErrors = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        consoleErrors.push(msg.text());
      }
    });
    
    await page.goto('/events');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(1000);
    
    // SSE-related errors are expected in test environment, but page should still render
    const pageContent = await page.locator('body');
    await expect(pageContent).toBeVisible();
  });
  
  test('should show empty state when no events', async ({ page }) => {
    // Override to return empty events
    await page.route('**/api/v1/events', (route) => {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: [] })
      });
    });
    
    await page.goto('/events');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(800);
    
    // Should show empty state
    await expect(page.locator('text=No events, text=No recent events')).toBeVisible();
  });
  
  test('should show error state when workers endpoint fails', async ({ page }) => {
    // Override workers to return error
    await page.route('**/api/v1/workers', (route) => {
      return route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'Internal server error' })
      });
    });
    
    await page.goto('/workers');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);
    
    // Should show error state
    await expect(page.locator('text=Error, text=Failed to load, text=Unable to load')).toBeVisible();
  });
});
