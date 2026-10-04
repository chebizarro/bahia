import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';
import { TEST_ORG_ID, createPublicState, createPublicSystemInfo,
  installPublicServiceDeploymentHarness } from './harnesses/service-deployment-public.js';

async function setup(page) {
  await installE2EMocks(page, { systemInfo: createPublicSystemInfo() });
  await installPublicServiceDeploymentHarness(page, { initialState: createPublicState() });
  await page.goto('/services');
  await expect(page.getByRole('heading', { name: 'Services', exact: true })).toBeVisible();
}

async function create(page, name) {
  await page.getByRole('button', { name: 'Create Service' }).first().click();
  const dialog = page.getByRole('dialog', { name: 'Create Service' });
  await dialog.locator('#service-org-id').fill(TEST_ORG_ID);
  await dialog.locator('#service-name').fill(name);
  await dialog.locator('#artifact-repo-path').fill(`ghcr.io/test/${name}`);
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(dialog).not.toBeVisible();
}

async function requestId(page, operation) {
  await expect.poll(() => page.evaluate(op =>
    window.__BAHIA_E2E_PUBLIC_REQUESTS.find(request => request.operation === op)?.eventId, operation)).toBeTruthy();
  return page.evaluate(op =>
    window.__BAHIA_E2E_PUBLIC_REQUESTS.find(request => request.operation === op).eventId, operation);
}

test('acceptance 8: create is pending until a 30315 accepted status and canonical projection', async ({ page }) => {
  await setup(page);
  await page.evaluate(() => { window.__BAHIA_E2E_INTENT_AUTO_STATUS = false; });
  await create(page, 'pending-service');
  const row = page.getByRole('row', { name: /pending-service/ });
  await expect(row).toContainText(/pending \d+ s/);
  const id = await requestId(page, 'service/create');
  expect(await page.evaluate(eventId => window.__BAHIA_E2E_PUBLIC_RESULTS.some(result =>
    result.requestEventId === eventId), id)).toBe(false);
  await page.evaluate(eventId => window.__BAHIA_E2E_RESOLVE_INTENT(eventId, 'accepted'), id);
  await expect(row).toContainText('confirmed');
  await expect.poll(() => page.evaluate(eventId => window.__BAHIA_E2E_PUBLIC_RESULTS.some(result =>
    result.requestEventId === eventId && result.kind === 30315), id)).toBe(true);
});

test('acceptance 9: stale expected_updated_at yields a visible conflict and re-read action', async ({ page }) => {
  await setup(page);
  await page.goto('/services/svc-existing-1');
  await expect(page.getByRole('heading', { name: 'existing-service' })).toBeVisible();
  await page.evaluate(() => {
    window.__BAHIA_E2E_PUBLIC_STATE.services[0].updated_at = '2026-10-03T00:00:00.000Z';
  });
  await page.getByRole('button', { name: 'Edit', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Edit Service' });
  await dialog.locator('#edit-name').fill('stale-service');
  await dialog.getByRole('button', { name: 'Save' }).click();
  const id = await requestId(page, 'service/update');
  const request = await page.evaluate(eventId => window.__BAHIA_E2E_PUBLIC_REQUESTS.find(item =>
    item.eventId === eventId), id);
  expect(request.payload.expected_updated_at).not.toBe('2026-10-03T00:00:00.000Z');
  await expect(page.getByRole('alert')).toContainText('Revision conflict');
  await expect(page.getByRole('button', { name: 'Re-read canonical state' })).toBeVisible();
  await expect.poll(() => page.evaluate(eventId => window.__BAHIA_E2E_PUBLIC_RESULTS.some(result =>
    result.requestEventId === eventId && result.tags.some(tag => tag[0] === 'status' && tag[1] === 'conflict')), id)).toBe(true);
});

test('acceptance 12: disconnected relay keeps intent pending; reconnect resends without a timer', async ({ page }) => {
  await setup(page);
  await expect.poll(() => page.evaluate(async () => Boolean((await import('/src/lib/nostr/boot.js')).getPool()))).toBe(true);
  await page.evaluate(async () => {
    const { getPool } = await import('/src/lib/nostr/boot.js');
    const { stopIntentClient } = await import('/src/lib/nostr/intent-client.svelte.js');
    stopIntentClient();
    const pool = getPool();
    window.__BAHIA_E2E_RECONNECT = {
      pool,
      connected: pool.getConnectedRelays,
      ready: pool.onRelayReady,
      listeners: []
    };
    pool.getConnectedRelays = () => [];
    pool.onRelayReady = listener => {
      window.__BAHIA_E2E_RECONNECT.listeners.push(listener);
      return () => {};
    };
  });
  await create(page, 'offline-service');
  const row = page.getByRole('row', { name: /offline-service/ });
  await expect(row).toContainText(/pending \d+ s/);
  await expect(page.getByRole('cell', { name: 'offline-service', exact: true })).toBeVisible();
  expect(await page.evaluate(() => window.__BAHIA_E2E_PUBLIC_REQUESTS.filter(request =>
    request.operation === 'service/create').length)).toBe(0);
  await page.evaluate(() => {
    const { connected, ready, listeners } = window.__BAHIA_E2E_RECONNECT;
    const pool = window.__BAHIA_E2E_RECONNECT.pool;
    // Restore connection state, then deliver exactly the pool's reconnect signal.
    pool.getConnectedRelays = connected;
    pool.onRelayReady = ready;
    for (const listener of listeners) listener({ relay: 'ws://relay.test.local', auth: false });
  });
  const id = await requestId(page, 'service/create');
  await expect.poll(() => page.evaluate(eventId => window.__BAHIA_E2E_PUBLIC_RESULTS.some(result =>
    result.requestEventId === eventId && result.kind === 30315), id)).toBe(true);
  await expect(row).toContainText('confirmed');
});
