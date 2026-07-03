import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';
import { E2E_SERVICE_PUBKEY } from './helpers.js';
import { attachRuntimeErrorGuards } from './helpers-console.js';

const SERVICE_PUBKEY = E2E_SERVICE_PUBKEY;
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
    kind: 30900,
    tags: [['domain', 'controlplane'], ['schema', 'bahia.registry.service.v1'], ['d', 'svc-1'], ['deleted', 'false'], ['name', 'web-app']],
    content: { schema: 'bahia.registry.service.v1', id: 'svc-1', name: 'web-app', runtime_type: 'docker', deleted: false }
  }),
  nostrEvent({
    id: 'env-1-event',
    kind: 30900,
    tags: [['domain', 'controlplane'], ['schema', 'bahia.registry.environment.v1'], ['d', 'env-1'], ['deleted', 'false'], ['name', 'production']],
    content: { schema: 'bahia.registry.environment.v1', id: 'env-1', name: 'production', protected: true, deleted: false }
  }),
  nostrEvent({
    id: 'state-1-event',
    kind: 30900,
    tags: [['domain', 'controlplane'], ['schema', 'bahia.state.service.v1'], ['d', 'svc-1:env-1'], ['service', 'svc-1'], ['environment', 'env-1'], ['deleted', 'false']],
    content: { schema: 'bahia.state.service.v1', service_id: 'svc-1', environment_id: 'env-1', drift_status: 'drifted', deleted: false }
  }),
  nostrEvent({
    id: 'worker-1-event',
    kind: 10100,
    pubkey: WORKER_PUBKEY,
    content: { name: 'worker-one', description: 'relay worker' }
  }),
  nostrEvent({
    id: 'audit-1-event',
    kind: 4903,
    tags: [['domain', 'controlplane'], ['schema', 'bahia.audit.v1'], ['type', 'service.created'], ['event_type', 'service.created'], ['d', 'svc-1'], ['service', 'svc-1']],
    content: { schema: 'bahia.audit.v1', type: 'service.created', event_type: 'service.created', entity_id: 'svc-1', data: { name: 'web-app' } }
  })
];

test('production bundle boots relay-backed dashboard without uncaught runtime errors', async ({ page }) => {
  const assertNoRuntimeErrors = await attachRuntimeErrorGuards(page);
  await installE2EMocks(page, { systemInfo: relaySystemInfo, nostrEvents });

  await page.route('**/api/v1/services/*/environments/*/intents', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ data: [] })
  }));

  await page.goto('/');
  await page.waitForLoadState('networkidle');

  await expect(page.locator('.card:has-text("Services") .card-value')).toHaveText('1');
  await expect(page.locator('.card:has-text("Environments") .card-value')).toHaveText('1');
  await expect(page.locator('.card:has-text("Workers") .card-value')).toHaveText('1');
  await expect(page.getByText('service.created')).toBeVisible();

  await assertNoRuntimeErrors();
});
