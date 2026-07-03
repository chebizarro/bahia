import { test, expect } from '@playwright/test';
import { installE2EMocks, TEST_PUBKEY } from './helpers.js';
import {
  SERVICE_PUBKEY,
  createPublicState,
  installPublicServiceDeploymentHarness
} from './harnesses/service-deployment-public.js';
import {
  ENCRYPTED_RELAY,
  KIND_CONTEXTVM,
  KIND_GIFT_WRAP,
  installEncryptedNotificationHarness
} from './harnesses/notifications-encrypted.js';

const PUBLIC_RELAY = 'ws://relay.test.local';

const systemInfo = {
  nostr: {
    browser_relays: [PUBLIC_RELAY],
    service_relays: [PUBLIC_RELAY],
    contextvm_relays: [ENCRYPTED_RELAY],
    service_pubkey: SERVICE_PUBKEY
  },
  features: {
    relay_sidecar: true,
    relay_read_models: true,
    encrypted_nostr_requests: true,
    legacy_sse: false
  },
  registries: []
};

const initialChannels = [
  {
    id: 'ch-1',
    name: 'Ops Webhook',
    channel_type: 'webhook',
    config: { url: 'https://hooks.example.com/ops' },
    event_filter: { types: ['deployment.failed'] },
    enabled: true,
    created_at: '2026-05-03T10:00:00.000Z',
    updated_at: '2026-05-03T10:00:00.000Z'
  }
];

const contextVMRelaySet = {
  id: 'e2e-contextvm-relays',
  kind: 30002,
  pubkey: SERVICE_PUBKEY,
  created_at: 1,
  tags: [['d', 'bahia-contextvm-v1'], ['relay', ENCRYPTED_RELAY]],
  content: '',
  sig: '0'.repeat(128)
};

test.describe('Mixed public plus encrypted browser session transport', () => {
  test('keeps public signer-first and encrypted notification journeys on the shared Bahia relay set', async ({ page }) => {
    await installE2EMocks(page, { systemInfo, nostrEvents: [contextVMRelaySet] });
    await installPublicServiceDeploymentHarness(page, {
      publicRelay: PUBLIC_RELAY,
      initialState: createPublicState(),
      emitCreateServiceProjection: true
    });
    await installEncryptedNotificationHarness(page, {
      publicRelay: PUBLIC_RELAY,
      encryptedRelay: ENCRYPTED_RELAY,
      initialChannels,
      initialLogs: []
    });

    await page.goto('/services');
    await expect(page.getByRole('heading', { name: 'Services', exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Create Service' }).first().click();
    await page.locator('#service-name').fill('mixed-created-service');
    await page.locator('#artifact-repo-path').fill('ghcr.io/example/mixed-created-service');
    await page.getByRole('dialog', { name: 'Create Service' }).getByRole('button', { name: 'Create' }).click();
    await expect(page.getByRole('cell', { name: 'mixed-created-service', exact: true })).toBeVisible();

    await page.goto('/notifications');
    await expect(page.getByRole('heading', { name: 'Notifications' })).toBeVisible();
    const row = page.locator('tr', { hasText: 'Ops Webhook' });
    await expect(row).toBeVisible();
    await row.getByRole('button', { name: 'Test' }).click();
    await expect(page.getByText('Test notification sent to Ops Webhook')).toBeVisible();

    const trace = await page.evaluate(() => ({
      publicRequests: window.__BAHIA_E2E_PUBLIC_REQUESTS,
      publicOks: window.__BAHIA_E2E_PUBLIC_OKS,
      publicResults: window.__BAHIA_E2E_PUBLIC_RESULTS,
      encryptedRequests: window.__BAHIA_E2E_ENCRYPTED_REQUESTS,
      encryptedOks: window.__BAHIA_E2E_ENCRYPTED_OKS,
      encryptedResults: window.__BAHIA_E2E_ENCRYPTED_RESULTS,
      encryptedOperations: window.__BAHIA_E2E_ENCRYPTED_OPERATIONS
    }));
    const normalizeRelay = (relay) => String(relay || '').replace(/\/$/, '');

    expect(trace.publicRequests).toEqual(expect.arrayContaining([
      expect.objectContaining({ kind: 25910, operation: 'service/create' })
    ]));
    expect(trace.publicRequests.every((request) => request.kind === 25910)).toBe(true);
    expect(trace.publicRequests.every((request) => normalizeRelay(request.relay) === ENCRYPTED_RELAY)).toBe(true);
    expect(trace.publicRequests.some((request) => request.kind === KIND_GIFT_WRAP)).toBe(false);
    for (const request of trace.publicRequests) {
      expect(trace.publicOks).toEqual(expect.arrayContaining([
        expect.objectContaining({ eventId: request.eventId, accepted: true })
      ]));
      expect(trace.publicResults).toEqual(expect.arrayContaining([
        expect.objectContaining({ requestEventId: request.eventId })
      ]));
    }

    expect(trace.encryptedOperations).toEqual(expect.arrayContaining([
      'notifications.channels.list',
      'notifications.channels.test'
    ]));
    expect(trace.encryptedRequests.length).toBeGreaterThanOrEqual(2);
    expect(trace.encryptedRequests.every((request) => request.kind === KIND_GIFT_WRAP && normalizeRelay(request.relay) === ENCRYPTED_RELAY)).toBe(true);
    expect(trace.encryptedRequests.some((request) => normalizeRelay(request.relay) === PUBLIC_RELAY)).toBe(false);
    for (const request of trace.encryptedRequests) {
      expect(request.innerKind).toBe(KIND_CONTEXTVM);
      expect(request.requesterPubkey).toBe(TEST_PUBKEY);
      expect(request.wrapperPubkey).toMatch(/^[0-9a-f]{64}$/);
      expect(request.tags).toEqual(expect.arrayContaining([['p', SERVICE_PUBKEY]]));
      expect(trace.encryptedOks).toEqual(expect.arrayContaining([
        expect.objectContaining({ eventId: request.eventId, kind: KIND_GIFT_WRAP, accepted: true })
      ]));
      expect(trace.encryptedResults).toEqual(expect.arrayContaining([
        expect.objectContaining({
          requestEventId: request.eventId,
          kind: KIND_GIFT_WRAP,
          requesterPubkey: TEST_PUBKEY,
          tags: expect.arrayContaining([
            ['e', request.eventId],
            ['p', TEST_PUBKEY],
            ['encrypted', 'contextvm-jsonrpc-v1']
          ])
        })
      ]));
    }
  });
});
