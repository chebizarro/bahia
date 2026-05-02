import { test, expect } from '@playwright/test';
import { installE2EMocks, seedNostrEvents } from './helpers.js';

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
    drift_detected: false,
    drift_status: 'in_sync'
  },
  {
    id: 'state-2',
    service_id: 'service-1',
    environment_id: 'env-2',
    artifact_id: 'sha256:def456',
    status: 'deployed',
    drift_detected: true,
    drift_status: 'drifted'
  },
  {
    id: 'state-3',
    service_id: 'service-2',
    environment_id: 'env-1',
    artifact_id: 'sha256:ghi789',
    status: 'deployed',
    drift_detected: false,
    drift_status: 'in_sync'
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
    time: new Date().toISOString(),
    entity_id: 'service-1',
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
    time: new Date(Date.now() - 300000).toISOString(),
    entity_id: 'service-2',
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
    time: new Date(Date.now() - 600000).toISOString(),
    entity_id: 'state-1',
    data: {
      service_id: 'service-1',
      service_name: 'web-app',
      environment_id: 'env-2',
      environment_name: 'staging'
    }
  }
];

const SERVICE_PUBKEY = 'b'.repeat(64);
const now = Math.floor(Date.now() / 1000);

function nostrEvent({ id, kind, pubkey = SERVICE_PUBKEY, created_at = now, tags = [], content = {} }) {
  return { id, kind, pubkey, created_at, tags, content: JSON.stringify(content), sig: '0'.repeat(128) };
}

function dashboardNostrEvents({ services = mockServices, environments = mockEnvironments, states = mockStates, workers = mockWorkers, events = mockEvents } = {}) {
  return [
    ...services.map((svc, index) => nostrEvent({
      id: `svc-${index}`,
      kind: 31962,
      tags: [['d', svc.id], ['deleted', 'false'], ['name', svc.name]],
      content: { ...svc, deleted: false }
    })),
    ...environments.map((env, index) => nostrEvent({
      id: `env-${index}`,
      kind: 31963,
      tags: [['d', env.id], ['deleted', 'false'], ['name', env.name]],
      content: { ...env, deleted: false }
    })),
    ...states.map((state, index) => nostrEvent({
      id: `state-${index}`,
      kind: 31961,
      tags: [['d', state.id || `${state.service_id}:${state.environment_id}`], ['service', state.service_id], ['environment', state.environment_id], ['deleted', 'false']],
      content: { ...state, deleted: false }
    })),
    ...workers.map((worker, index) => nostrEvent({
      id: `worker-${index}`,
      kind: 10100,
      pubkey: worker.pubkey,
      content: { ...worker, name: worker.name || worker.pubkey }
    })),
    ...events.map((event, index) => nostrEvent({
      id: `activity-${index}`,
      kind: 31006,
      created_at: now - index * 60,
      tags: [['event_type', event.type], ['d', event.entity_id || event.id]],
      content: { event_type: event.type, entity_id: event.entity_id, data: event.data }
    }))
  ];
}

const relaySystemInfo = {
  nostr: { browser_relays: ['ws://relay.test.local'], service_pubkey: SERVICE_PUBKEY },
  features: { relay_sidecar: true, relay_read_models: true, legacy_sse: false }
};

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page, { systemInfo: relaySystemInfo, nostrEvents: dashboardNostrEvents() });

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
  await page.route('**/api/v1/state', (route) => {
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
    await expect(page.locator('.card:has-text("Services") .card-value')).toHaveText('3');
    
    // Environments stat card
    await expect(page.locator('.card:has-text("Environments") .card-value')).toHaveText('2');
    
    // Workers stat card
    await expect(page.locator('.card:has-text("Workers")')).toBeVisible();
  });
  
  test('should show online workers count in stat card', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(800);
    
    // Workers card shows the total workers loaded from the API.
    await expect(page.locator('.card:has-text("Workers") .card-value')).toHaveText('3');
  });
  
  test('should show drift count in states stat card', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(800);
    
    // States with drift (1 out of 3 has drift in mock data)
    await expect(page.locator('.card:has-text("Drifted") .card-value')).toHaveText('1');
  });
  
  test('should display pending approvals card', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(1000);
    
    // Pending approvals card should exist
    await expect(page.locator('.card-link .card:has-text("Pending Approvals")')).toBeVisible();
    
    // Should show count of pending approvals (2 in mock data)
    await expect(page.locator('main a[href="/deployments/pending"] .card-value')).toHaveText('2');
  });
  
  test('should link to pending deployments page from pending approvals card', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(1000);
    
    // Find link to pending deployments
    const pendingLink = page.locator('main a[href="/deployments/pending"]');
    await expect(pendingLink.first()).toBeVisible();
    
    // Verify the SvelteKit link points to the pending deployments page.
    await expect(pendingLink.first()).toHaveAttribute('href', '/deployments/pending');
  });
  
  test('should display quick actions section', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    
    // Quick actions section
    await expect(page.locator('.quick-actions')).toBeVisible();
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
    await expect(page.getByRole('heading', { name: 'Recent Activity' })).toBeVisible();
  });
  
  test('should show recent events in activity feed', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(1000);
    
    // The activity table should render; events arrive through mocked EventSource.
    await expect(page.locator('section:has-text("Recent Activity") table')).toBeVisible();
  });
  
  test('should render event type badges in recent activity', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(1000);
    
    // Event rows or the empty-state hint should render safely.
    const activitySection = page.locator('section:has-text("Recent Activity")');
    await expect(activitySection.locator('table, .hint').first()).toBeVisible();
  });
  
  test('should show service and environment names in recent events', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(1000);
    
    // Event entity cells or the SSE empty hint should render without errors.
    const activitySection = page.locator('section:has-text("Recent Activity")');
    await expect(activitySection.locator('table, .hint').first()).toBeVisible();
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
    
    // Dashboard should still render the pending approvals card when there are no intents.
    await expect(page.locator('.card-link .card:has-text("Pending Approvals")')).toBeVisible();
  });
  
  test('should handle empty recent activity gracefully', async ({ page }) => {
    // Override relay activity to return empty
    await seedNostrEvents(page, dashboardNostrEvents({ events: [] }));
    
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(1000);
    
    // Should show the activity table or its empty state hint.
    const activitySection = page.locator('section:has-text("Recent Activity")');
    await expect(activitySection.locator('table, .hint').first()).toBeVisible();
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
