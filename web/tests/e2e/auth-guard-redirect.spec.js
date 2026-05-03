import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

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
    routeRoleRequirements: {
      '/settings': ['admin']
    }
  });

  await page.goto('/settings');

  await expect(page).toHaveURL('/settings');
  await expect(page.getByText('You do not have permission to view this page.')).toBeVisible();
});

test('shows compatibility-needed state on /orgs when REST compatibility is unavailable', async ({ page }) => {
  let orgsRequestCount = 0;

  await page.route('**/api/v1/orgs**', (route) => {
    orgsRequestCount += 1;
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: [] })
    });
  });

  await installE2EMocks(page, {
    authenticated: true,
    extension: true,
    systemInfo: {
      nostr: {
        browser_relays: [],
        service_pubkey: 'b'.repeat(64)
      },
      features: {
        relay_sidecar: true,
        relay_read_models: true,
        legacy_sse: false
      }
    }
  });

  await page.goto('/orgs');

  await expect(page).toHaveURL('/orgs');
  await expect(page.getByText('This page currently requires REST compatibility auth.')).toBeVisible();
  await expect(page.getByText('Enable backend direct_nostr_http_auth to access it.')).toBeVisible();
  await expect.poll(() => orgsRequestCount).toBe(0);
});
