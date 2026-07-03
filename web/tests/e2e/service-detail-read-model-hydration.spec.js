import { test, expect } from '@playwright/test';
import { E2E_SERVICE_PUBKEY, installE2EMocks } from './helpers.js';

const BROWSER_RELAY = 'ws://relay.test.local';
const SERVICE_PUBKEY = E2E_SERVICE_PUBKEY;
const CONTROLPLANE_STATE_KIND = 30900;
const SERVICE_SCHEMA = 'bahia.registry.service.v1';

const systemInfo = {
  nostr: {
    browser_relays: [BROWSER_RELAY],
    service_relays: [BROWSER_RELAY],
    service_pubkey: SERVICE_PUBKEY
  },
  features: {
    relay_sidecar: true,
    relay_read_models: true,
    legacy_sse: false,
    publish_enabled: true
  }
};

function serviceProjectionEvent(service, createdAt = 1777852800) {
  return {
    id: `service-projection-${service.id}`,
    kind: CONTROLPLANE_STATE_KIND,
    pubkey: SERVICE_PUBKEY,
    created_at: createdAt,
    tags: [
      ['domain', 'controlplane'],
      ['schema', SERVICE_SCHEMA],
      ['d', service.id],
      ['service', service.id],
      ['name', service.name],
      ['deleted', 'false']
    ],
    content: JSON.stringify({ schema: SERVICE_SCHEMA, ...service, deleted: false }),
    sig: '0'.repeat(128)
  };
}

async function installRelayMetadataMock(page) {
  await page.route('http://relay.test.local/**', (route) => route.fulfill({
    status: 200,
    contentType: 'application/nostr+json',
    headers: {
      'Access-Control-Allow-Origin': '*'
    },
    body: JSON.stringify({
      name: 'Bahia E2E relay',
      supported_nips: [1, 11, 42, 65],
      limitation: { auth_required: false }
    })
  }));
}

test.describe('service detail relay read-model hydration', () => {
  test.beforeEach(async ({ page }) => {
    await installE2EMocks(page, {
      authenticated: true,
      extension: true,
      nostrEvents: [],
      systemInfo
    });
    await installRelayMetadataMock(page);
  });

  test('renders a service projection delivered by a live 30900 relay event after initial EOSE', async ({ page }) => {
    await page.goto('/services/svc-live-1');

    await expect(page.getByText('Error: Service not found')).toBeVisible();

    await page.evaluate((event) => {
      window.__bahiaPushNostrEvent(event);
    }, serviceProjectionEvent({
      id: 'svc-live-1',
      name: 'Live Relay Service',
      slug: 'live-relay-service',
      artifact_repo: 'registry.example/live-relay-service',
      repo_url: 'nostr://repo/live-relay-service',
      runtime_type: 'docker',
      default_branch: 'main',
      status: 'running',
      created_at: '2026-07-03T12:00:00.000Z'
    }));

    await expect(page.getByRole('heading', { name: 'Live Relay Service' })).toBeVisible();
    await expect(page.getByText('registry.example/live-relay-service')).toBeVisible();
    await expect(page.getByText('Error: Service not found')).toHaveCount(0);
  });
});
