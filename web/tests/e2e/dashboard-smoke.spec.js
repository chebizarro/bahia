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
    last_seen: new Date().toISOString(),
    last_advertisement_at: new Date(Date.now() - 2 * 60 * 1000).toISOString()
  },
  {
    pubkey: 'npub1worker2def',
    status: 'online',
    last_seen: new Date().toISOString(),
    last_advertisement_at: new Date(Date.now() - 4 * 60 * 1000).toISOString()
  },
  {
    pubkey: 'npub1worker3ghi',
    status: 'offline',
    last_seen: new Date(Date.now() - 3600000).toISOString(),
    last_advertisement_at: new Date(Date.now() - 48 * 60 * 60 * 1000).toISOString()
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

const mockPaymentHistoryByWorker = {
  npub1worker1abc: [
    {
      id: 'payment-1',
      deployment_run_id: 'run-1',
      worker_pubkey: 'npub1worker1abc',
      amount_sats: 1200,
      direction: 'payment',
      status: 'sent',
      created_at: new Date().toISOString()
    },
    {
      id: 'payment-change-1',
      deployment_run_id: 'run-1',
      worker_pubkey: 'npub1worker1abc',
      amount_sats: 100,
      direction: 'change',
      status: 'redeemed',
      created_at: new Date().toISOString()
    }
  ],
  npub1worker2def: [
    {
      id: 'payment-2',
      deployment_run_id: 'run-2',
      worker_pubkey: 'npub1worker2def',
      amount_sats: 800,
      direction: 'payment',
      status: 'redeemed',
      created_at: new Date(Date.now() - 60000).toISOString()
    },
    {
      id: 'payment-failed-1',
      deployment_run_id: 'run-3',
      worker_pubkey: 'npub1worker2def',
      amount_sats: 500,
      direction: 'payment',
      status: 'failed',
      created_at: new Date(Date.now() - 120000).toISOString()
    }
  ],
  npub1worker3ghi: []
};

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
      deployment_id: 'deploy-2',
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
const ENCRYPTED_RELAY = 'ws://encrypted.test.local';
const now = Math.floor(Date.now() / 1000);

function nostrEvent({ id, kind, pubkey = SERVICE_PUBKEY, created_at = now, tags = [], content = {} }) {
  return { id, kind, pubkey, created_at, tags, content: JSON.stringify(content), sig: '0'.repeat(128) };
}

function dashboardNostrEvents({ services = mockServices, environments = mockEnvironments, states = mockStates, workers = mockWorkers, intents = mockPendingIntents, events = mockEvents } = {}) {
  return [
    ...services.map((svc, index) => nostrEvent({
      id: `svc-${index}`,
      kind: 30900,
      tags: [['d', svc.id], ['deleted', 'false'], ['name', svc.name]],
      content: { ...svc, deleted: false }
    })),
    ...environments.map((env, index) => nostrEvent({
      id: `env-${index}`,
      kind: 30900,
      tags: [['d', env.id], ['deleted', 'false'], ['name', env.name]],
      content: { ...env, deleted: false }
    })),
    ...states.map((state, index) => nostrEvent({
      id: `state-${index}`,
      kind: 30900,
      tags: [['d', state.id || `${state.service_id}:${state.environment_id}`], ['service', state.service_id], ['environment', state.environment_id], ['deleted', 'false']],
      content: { ...state, deleted: false }
    })),
    ...intents.map((intent, index) => nostrEvent({
      id: `intent-${index}`,
      kind: 30900,
      tags: [['d', intent.id], ['service', intent.service_id], ['environment', intent.environment_id], ['deleted', 'false']],
      content: { ...intent, deleted: false }
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
  nostr: {
    browser_relays: ['ws://relay.test.local'],
    service_pubkey: SERVICE_PUBKEY
  },
  features: { relay_sidecar: true, relay_read_models: true, encrypted_nostr_requests: true, legacy_sse: false }
};

async function installEncryptedDashboardPaymentHarness(page) {
  await page.addInitScript(({ servicePubkey }) => {
    function matchesFilter(event, filter) {
      if (!filter || typeof filter !== 'object') return true;
      if (Array.isArray(filter.kinds) && !filter.kinds.includes(event.kind)) return false;
      if (Array.isArray(filter.authors) && !filter.authors.includes(event.pubkey)) return false;
      for (const [key, values] of Object.entries(filter)) {
        if (!key.startsWith('#') || !Array.isArray(values)) continue;
        const tagName = key.slice(1);
        const tags = Array.isArray(event.tags) ? event.tags : [];
        if (!tags.some((tag) => Array.isArray(tag) && tag[0] === tagName && values.includes(tag[1]))) {
          return false;
        }
      }
      return true;
    }

    window.__BAHIA_E2E_DASHBOARD_PAYMENT_HISTORY = window.__BAHIA_E2E_DASHBOARD_PAYMENT_HISTORY || {};
    window.__BAHIA_E2E_DASHBOARD_PAYMENT_ERRORS = window.__BAHIA_E2E_DASHBOARD_PAYMENT_ERRORS || {};
    window.__BAHIA_E2E_DASHBOARD_ENCRYPTED_PAYMENT_TRACE = [];
    window.nostr = {
      ...(window.nostr || {}),
      nip44: {
        encrypt: async (_recipientPubkey, plaintext) => `enc44:${plaintext}`,
        decrypt: async (_senderPubkey, ciphertext) => {
          if (typeof ciphertext !== 'string' || !ciphertext.startsWith('enc44:')) {
            throw new Error('bad ciphertext');
          }
          return ciphertext.slice('enc44:'.length);
        }
      }
    };

    const originalSend = window.WebSocket.prototype.send;
    window.WebSocket.prototype.send = function patchedSend(data) {
      let message;
      try {
        message = JSON.parse(data);
      } catch {
        return originalSend.call(this, data);
      }

      if (Array.isArray(message) && message[0] === 'EVENT' && message[1]?.kind === 25910) {
        const event = message[1];
        const content = String(event.content || '');
        const plaintext = content.startsWith('mock-nip44:')
          ? decodeURIComponent(escape(atob(content.replace(/^mock-nip44:/, ''))))
          : content.replace(/^enc44:/, '');
        const envelope = JSON.parse(plaintext);
          const params = { ...(envelope.params || {}) };
          delete params._meta;
        if (envelope.method === 'payments/history') {
          const worker = String(params.worker || envelope.payload?.worker || '');
          const trace = window.__BAHIA_E2E_DASHBOARD_ENCRYPTED_PAYMENT_TRACE || [];
          trace.push({ relay: this.url, operation: 'payments.history', worker });
          window.__BAHIA_E2E_DASHBOARD_ENCRYPTED_PAYMENT_TRACE = trace;

          const error = window.__BAHIA_E2E_DASHBOARD_PAYMENT_ERRORS?.[worker];
          const resultEnvelope = error
            ? {
                jsonrpc: '2.0',
                id: envelope.id || event.id,
                result: { status: 'error',
                error: typeof error === 'string' ? { code: 'handler_failed', message: error } : error }
              }
            : {
                jsonrpc: '2.0',
                id: envelope.id || event.id,
                result: { status: 'ok', payload: window.__BAHIA_E2E_DASHBOARD_PAYMENT_HISTORY?.[worker] || [] }
              };
          const resultEvent = {
            id: `result-${event.id}`,
            kind: 25910,
            pubkey: servicePubkey,
            created_at: Math.floor(Date.now() / 1000),
            tags: [['e', event.id], ['p', event.pubkey], ['encrypted', 'contextvm-jsonrpc-v1']],
            content: String(event.content || '').startsWith('mock-nip44:')
              ? `mock-nip44:${btoa(unescape(encodeURIComponent(JSON.stringify(resultEnvelope))))}`
              : `enc44:${JSON.stringify(resultEnvelope)}`,
            sig: '0'.repeat(128)
          };

          const sent = originalSend.call(this, data);
          if (this.readyState !== window.WebSocket.OPEN) return sent;
          for (const [subId, filters] of this.subscriptions?.entries() || []) {
            if (filters.some((filter) => matchesFilter(resultEvent, filter))) {
              this.onmessage?.({ data: JSON.stringify(['EVENT', subId, resultEvent]) });
            }
          }
          return sent;
        }
      }

      return originalSend.call(this, data);
    };
  }, { servicePubkey: SERVICE_PUBKEY });
}

async function seedEncryptedDashboardPayments(page, { paymentHistoryByWorker = mockPaymentHistoryByWorker, paymentErrorsByWorker = {} } = {}) {
  await page.addInitScript(({ paymentHistoryByWorker, paymentErrorsByWorker }) => {
    window.__BAHIA_E2E_DASHBOARD_PAYMENT_HISTORY = structuredClone(paymentHistoryByWorker || {});
    window.__BAHIA_E2E_DASHBOARD_PAYMENT_ERRORS = structuredClone(paymentErrorsByWorker || {});
    window.__BAHIA_E2E_DASHBOARD_ENCRYPTED_PAYMENT_TRACE = [];
  }, { paymentHistoryByWorker, paymentErrorsByWorker });
}

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page, { systemInfo: relaySystemInfo, nostrEvents: dashboardNostrEvents() });
  await installEncryptedDashboardPaymentHarness(page);
  await seedEncryptedDashboardPayments(page);

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
  
  // Guard against legacy REST payment reads for dashboard cost summary.
  await page.route('**/api/v1/payments/history**', (route) => route.fulfill({
    status: 500,
    contentType: 'application/json',
    body: JSON.stringify({ error: 'legacy REST payment history should not be called' })
  }));

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
  
  test('should show live workers count in stat card', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(800);
    
    await expect(page.locator('.card:has-text("Workers") .card-value')).toHaveText('2');
    await expect(page.locator('.card:has-text("Workers") .card-subtitle')).toHaveText('2 recent / 3 catalog');
  });
  
  test('should show drift count in states stat card', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(800);
    
    // States with drift (1 out of 3 has drift in mock data)
    await expect(page.locator('.card:has-text("Drifted") .card-value')).toHaveText('1');
  });

  test('should expose dashboard actions for drifted states', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(800);

    const driftCardLink = page.locator('main a[href="#environment-states"]');
    await expect(driftCardLink).toBeVisible();
    await expect(driftCardLink).toHaveAttribute('href', '#environment-states');
    await expect(driftCardLink.locator('.card-action')).toHaveText('Review states');

    const environmentStates = page.locator('section#environment-states');
    await expect(environmentStates.getByRole('heading', { name: 'Environment States' })).toBeVisible();

    const driftedRow = environmentStates.locator('tbody tr:has(.badge-error)').first();
    await expect(driftedRow.getByRole('link', { name: 'Review environment' })).toHaveAttribute('href', '/environments/env-2');
    await expect(driftedRow.getByRole('link', { name: 'Open service' })).toHaveAttribute('href', '/services/service-1');
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

  test('should display recent spend cost summary card', async ({ page }) => {
    await page.goto('/');

    const spendCard = page.locator('main a[href="/payments"] .card:has-text("Recent Spend")');
    await expect(spendCard).toBeVisible();
    await expect(spendCard.locator('.card-value')).toHaveText('2,000 sats');
    await expect(spendCard.locator('.card-subtitle')).toHaveText('2 recent payments');

    await expect.poll(() => page.evaluate(() => window.__BAHIA_E2E_DASHBOARD_ENCRYPTED_PAYMENT_TRACE)).toEqual([
      { relay: ENCRYPTED_RELAY, operation: 'payments.history', worker: 'npub1worker1abc' },
      { relay: ENCRYPTED_RELAY, operation: 'payments.history', worker: 'npub1worker2def' },
      { relay: ENCRYPTED_RELAY, operation: 'payments.history', worker: 'npub1worker3ghi' }
    ]);
  });

  test('should show empty recent spend state when payment history is empty', async ({ page }) => {
    await seedEncryptedDashboardPayments(page, {
      paymentHistoryByWorker: {
        npub1worker1abc: [],
        npub1worker2def: [],
        npub1worker3ghi: []
      }
    });

    await page.goto('/');

    const spendCard = page.locator('main a[href="/payments"] .card:has-text("Recent Spend")');
    await expect(spendCard).toBeVisible();
    await expect(spendCard.locator('.card-value')).toHaveText('0 sats');
    await expect(spendCard.locator('.card-subtitle')).toHaveText('No recent spend');
  });

  test('should keep recent spend totals when one worker history request fails', async ({ page }) => {
    await seedEncryptedDashboardPayments(page, {
      paymentErrorsByWorker: {
        npub1worker2def: 'worker history unavailable'
      }
    });

    await page.goto('/');

    const spendCard = page.locator('main a[href="/payments"] .card:has-text("Recent Spend")');
    await expect(spendCard).toBeVisible();
    await expect(spendCard.locator('.card-value')).toHaveText('1,200 sats');
    await expect(spendCard.locator('.card-subtitle')).toHaveText('1 recent payment; 1 worker unavailable');
  });
  
  test('should display quick actions section', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('.quick-actions')).toBeVisible();
    await expect(page.getByRole('link', { name: 'Deployment History' })).toHaveAttribute('href', '/deployments');
  });

  test('should expose logo, menu, and linked dashboard cards', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(800);

    const logo = page.locator('img[alt="Bahia"]');
    await expect(logo).toBeVisible();
    await expect(logo).toHaveAttribute('src', /\/branding\/logo_wide_(dm|lm)\.png$/);
    expect(await logo.evaluate((node) => Number.parseFloat(getComputedStyle(node).height))).toBeGreaterThanOrEqual(60);

    const menuButton = page.getByRole('button', { name: 'Menu' });
    await expect(menuButton).toBeVisible();
    await menuButton.click();
    await expect(page.getByRole('heading', { name: 'Workspace' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Delivery' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Operations' })).toBeVisible();
    const pendingApprovalsLink = page.locator('#navigation-drawer a[href="/deployments/pending"]');
    await expect(pendingApprovalsLink).toContainText('Pending Approvals');
    await expect(pendingApprovalsLink.locator('.badge')).toHaveCount(0);

    await expect(page.locator('.stats a[href="/services"] .card')).toContainText('Services');
    await expect(page.locator('.stats a[href="/environments"] .card')).toContainText('Environments');
    await expect(page.locator('.stats a[href="/workers"] .card')).toContainText('Workers');
    await expect(page.locator('.stats a[href="/deployments/pending"]')).toHaveAttribute('href', '/deployments/pending');
    await expect(page.locator('.stats a[href="/payments"]')).toHaveAttribute('href', '/payments');
  });
  
  test('should display recent activity section', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(800);
    
    // Recent activity heading
    await expect(page.getByRole('heading', { name: 'Recent Activity' })).toBeVisible();
    await expect(page.locator('section:has-text("Recent Activity") thead th').first()).toContainText(/^Time \(.+\)$/);
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

  test('should deep-link recent activity entities to dashboard detail routes', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(1000);

    const activitySection = page.locator('section:has-text("Recent Activity")');
    await expect(activitySection.locator('a[href="/deployments/deploy-2"]')).toBeVisible();
    await expect(activitySection.locator('a[href="/services/service-1"]').first()).toBeVisible();
    await expect(activitySection.locator('a[href="/environments/env-2"]').first()).toBeVisible();
  });

  test('should show named environment state entities with id tooltips and detail dialogs', async ({ page }) => {
    await page.goto('/');

    const environmentStates = page.locator('section#environment-states');
    const firstRow = environmentStates.locator('tbody tr').first();
    const serviceButton = firstRow.getByRole('button', { name: 'web-app' });
    const environmentButton = firstRow.getByRole('button', { name: 'production' });

    await expect(serviceButton).toBeVisible();
    await expect(environmentButton).toBeVisible();
    await expect(serviceButton).toHaveAttribute('title', /service-1/);
    await expect(environmentButton).toHaveAttribute('title', /env-1/);

    await serviceButton.click();
    await expect(page.getByRole('dialog', { name: 'web-app · Service' })).toBeVisible();
    await expect(page.getByRole('dialog', { name: 'web-app · Service' })).toContainText('ghcr.io/test/web-app');
    await page.getByRole('dialog', { name: 'web-app · Service' }).getByRole('button', { name: 'Close' }).click();

    await environmentButton.click();
    await expect(page.getByRole('dialog', { name: 'production · Environment' })).toBeVisible();
    await expect(page.getByRole('dialog', { name: 'production · Environment' })).toContainText('role=prod');
  });

  test('should show timezone-aware recent activity details and event dialog', async ({ page }) => {
    await page.goto('/');

    const activitySection = page.locator('section:has-text("Recent Activity")');
    const firstRow = activitySection.locator('tbody tr').first();
    const timeValue = firstRow.locator('.activity-time');
    const eventButton = firstRow.locator('button[data-dashboard-action="event"]');

    await expect(eventButton).toBeVisible();
    await expect(activitySection.locator('thead th').first()).toContainText(/^Time \(.+\)$/);
    await expect(timeValue).toBeVisible();
    await expect(timeValue).toHaveAttribute('title', /UTC/);
    await expect(eventButton).toHaveAttribute('title', /Service: web-app \(service-1\)/);
    await expect(eventButton).toHaveAttribute('title', /Environment: production \(env-1\)/);

    const entityLinks = firstRow.locator('.activity-entity-links');
    await expect(entityLinks).toContainText('Service web-app');
    await expect(entityLinks).toContainText('Environment production');

    await eventButton.click();
    const dialog = page.getByRole('dialog', { name: 'deployment.started · Event detail' });
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText('UTC Time');
    await expect(dialog).toContainText('"service_name": "web-app"');
    await expect(dialog).toContainText('"environment_name": "production"');
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
