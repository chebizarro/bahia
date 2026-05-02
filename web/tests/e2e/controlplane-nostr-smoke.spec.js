import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

const SERVICE_PUBKEY = 'b'.repeat(64);
const WORKER_PUBKEY = 'c'.repeat(64);
const now = Math.floor(Date.now() / 1000);

function nostrEvent({ id, kind, pubkey = SERVICE_PUBKEY, created_at = now, tags = [], content = {} }) {
  return {
    id,
    kind,
    pubkey,
    created_at,
    tags,
    content: JSON.stringify(content),
    sig: '0'.repeat(128)
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
  nostrEvent({
    id: 'svc-1-event',
    kind: 31962,
    tags: [['d', 'svc-1'], ['deleted', 'false'], ['name', 'web-app']],
    content: { id: 'svc-1', name: 'web-app', runtime_type: 'docker', deleted: false }
  }),
  nostrEvent({
    id: 'env-1-event',
    kind: 31963,
    tags: [['d', 'env-1'], ['deleted', 'false'], ['name', 'production']],
    content: { id: 'env-1', name: 'production', protected: true, deleted: false }
  }),
  nostrEvent({
    id: 'state-1-event',
    kind: 31961,
    tags: [['d', 'svc-1:env-1'], ['service', 'svc-1'], ['environment', 'env-1'], ['deleted', 'false']],
    content: { service_id: 'svc-1', environment_id: 'env-1', drift_status: 'drifted', deleted: false }
  }),
  nostrEvent({
    id: 'worker-1-event',
    kind: 10100,
    pubkey: WORKER_PUBKEY,
    content: { name: 'worker-one', description: 'relay worker' }
  }),
  nostrEvent({
    id: 'audit-1-event',
    kind: 31006,
    tags: [['event_type', 'service.created'], ['d', 'svc-1'], ['service', 'svc-1']],
    content: { event_type: 'service.created', entity_id: 'svc-1', data: { name: 'web-app' } }
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
    await expect(page.getByText('ws://relay.test.local')).toBeVisible();
  });
});
