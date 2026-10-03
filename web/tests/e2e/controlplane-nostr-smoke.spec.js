import { test, expect } from '@playwright/test';
import { BAHIA_STATE_SCHEMAS, cpAuditFixture, cpStateFixture } from './cp-state-fixtures.js';
import { E2E_SERVICE_PUBKEY, installE2EMocks, e2eTestPubkey } from './helpers.js';

const SERVICE_PUBKEY = E2E_SERVICE_PUBKEY;
const WORKER_PUBKEY = e2eTestPubkey('worker');
const now = Math.floor(Date.now() / 1000);

function nostrEvent({ id, kind, pubkey = SERVICE_PUBKEY, created_at = now, tags = [], content = {} }) {
  return {
    id,
    kind,
    pubkey,
    created_at,
    tags,
    content: JSON.stringify(content),
  };
}

const relaySystemInfo = {
  nostr: {
    browser_relays: ['ws://relay.test.local'],
    service_pubkey: SERVICE_PUBKEY
  },
  features: {
    relay_sidecar: true,
    relay_read_models: true,
    legacy_sse: false
  }
};

const nostrEvents = [
  cpStateFixture({
    id: 'svc-1-event',
    schema: BAHIA_STATE_SCHEMAS.SERVICE_REGISTRY,
    d: 'svc-1',
    tags: [['name', 'web-app']],
    content: { id: 'svc-1', name: 'web-app', runtime_type: 'docker' }
  }),
  cpStateFixture({
    id: 'env-1-event',
    schema: BAHIA_STATE_SCHEMAS.ENVIRONMENT_REGISTRY,
    d: 'env-1',
    tags: [['name', 'production']],
    content: { id: 'env-1', name: 'production', protected: true }
  }),
  cpStateFixture({
    id: 'state-1-event',
    schema: BAHIA_STATE_SCHEMAS.SERVICE_STATE,
    d: 'service:svc-1:environment:env-1',
    tags: [['service', 'svc-1'], ['environment', 'env-1']],
    content: { service_id: 'svc-1', environment_id: 'env-1', drift_status: 'drifted' }
  }),
  nostrEvent({
    id: 'worker-1-event',
    kind: 10100,
    pubkey: WORKER_PUBKEY,
    content: { name: 'worker-one', description: 'relay worker' }
  }),
  cpAuditFixture({
    id: 'audit-1-event',
    type: 'service.created',
    entityId: 'svc-1',
    state: 'svc-1',
    data: { name: 'web-app' },
    tags: [['service', 'svc-1']]
  })
];

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page, { systemInfo: relaySystemInfo, nostrEvents });

  // Pending approvals card still queries REST; keep it deterministic.
  await page.route('**/api/v1/services/*/environments/*/intents', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ data: [] })
  }));
});

test.describe('Relay-backed controlplane smoke', () => {
  test('dashboard renders first-party state from Nostr read models', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await expect(page.locator('.card:has-text("Services") .card-value')).toHaveText('1');
    await expect(page.locator('.card:has-text("Environments") .card-value')).toHaveText('1');
    await expect(page.locator('.card:has-text("Workers") .card-value')).toHaveText('1');
    await expect(page.locator('.card:has-text("Drifted") .card-value')).toHaveText('1');
    await expect(page.getByText('service.created')).toBeVisible();
  });

  test('events page reports Nostr relay status and activity', async ({ page }) => {
    await page.goto('/events');
    await page.waitForLoadState('networkidle');
    await expect(page.getByRole('heading', { name: 'Live Events' })).toBeVisible();
    await expect(page.locator('.status')).toContainText('Nostr relay');
    await expect(page.getByText('service.created')).toBeVisible();
    await expect(page.locator('.hint')).toContainText('ws://relay.test.local');
  });
});
