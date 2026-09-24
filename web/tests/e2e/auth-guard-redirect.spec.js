import { test, expect } from '@playwright/test';
import { E2E_SERVICE_PUBKEY, installE2EMocks } from './helpers.js';
import { HTTP_AUTH } from '../../src/lib/nostr/kinds.gen.js';

test.beforeEach(async ({ page }) => {
  await page.route('**/api/v1/**', (route) => {
    const url = route.request().url();

    if (url.includes('/services') || url.includes('/environments') || url.includes('/workers') || url.includes('/events')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: [] })
      });
    }

    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: null })
    });
  });
});

test('redirects unauthenticated users away from protected routes', async ({ page }) => {
  await installE2EMocks(page, { authenticated: false, extension: false });

  await page.goto('/services');

  await expect(page).toHaveURL('/');
});

test('shows permission denied when user lacks required route role', async ({ page }) => {
  await installE2EMocks(page, {
    authenticated: true,
    extension: true,
    backendRole: 'viewer',
    routeRoleRequirements: {
      '/settings': ['admin']
    }
  });

  await page.goto('/settings');

  await expect(page).toHaveURL('/settings');
  await expect(page.getByText('You do not have permission to view this page.')).toBeVisible();
});

test('denies protected routes when backend membership auth is unavailable', async ({ page }) => {
  let orgsRequestCount = 0;

  await installE2EMocks(page, {
    authenticated: true,
    extension: true,
    systemInfo: {
      nostr: {
        browser_relays: ['ws://relay.test.local'],
        service_pubkey: E2E_SERVICE_PUBKEY
      },
      features: {
        direct_nostr_http_auth: false,
        relay_sidecar: true,
        relay_read_models: true,
        legacy_sse: false
      }
    }
  });

  await page.route('**/api/v1/orgs**', (route) => {
    orgsRequestCount += 1;
    return route.fulfill({ json: { data: [] } });
  });

  await page.goto('/orgs');

  await expect(page).toHaveURL('/');
  await expect(page.getByRole('heading', { name: 'Dashboard', exact: true })).toBeVisible();
  await expect.poll(() => orgsRequestCount).toBe(0);
});

for (const accepted of [true, false]) {
  test(`waits for backend membership before ${accepted ? 'rendering' : 'denying'} a protected route`, async ({ page }) => {
    await installE2EMocks(page);
    let releaseProbe;
    const probeReleased = new Promise((resolve) => { releaseProbe = resolve; });
    let observeProbe;
    const probeReceived = new Promise((resolve) => { observeProbe = resolve; });
    await page.route('**/api/v1/orgs', async (route) => {
      observeProbe(route.request());
      await probeReleased;
      await route.fulfill({
        status: accepted ? 200 : 403,
        json: accepted
          ? { data: [{ id: 'org-e2e', name: 'E2E organization', role: 'owner' }] }
          : { error: 'Platform membership required' }
      });
    });

    await page.goto('/settings');
    try {
      const request = await probeReceived;
      const authorization = request.headers().authorization;
      expect(authorization).toMatch(/^Nostr /);
      const event = JSON.parse(Buffer.from(authorization.slice(6), 'base64').toString());
      expect(event.kind).toBe(HTTP_AUTH);
      expect(event.tags).toEqual(expect.arrayContaining([
        ['u', request.url()], ['method', 'GET']
      ]));
      await expect(page.getByText('Checking authentication...')).toBeVisible();
      await expect(page).toHaveURL('/settings');
      await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toHaveCount(0);
    } finally {
      releaseProbe();
    }

    if (accepted) {
      await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toBeVisible();
      await expect(page).toHaveURL('/settings');
    } else {
      await expect(page).toHaveURL('/');
      await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toHaveCount(0);
    }
  });
}
