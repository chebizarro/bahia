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

test('allows authenticated users to access routes with no role requirement', async ({ page }) => {
  // §6.2: authenticated = persisted signer-verified session, no REST probe.
  // /orgs has no role requirement, so authenticated users can access it directly.
  await installE2EMocks(page, {
    authenticated: true,
    extension: true
  });

  await page.goto('/orgs');

  // Authenticated user should stay on /orgs, not be redirected
  await expect(page).toHaveURL('/orgs');
});

test('renders protected route immediately for authenticated user without backend probe', async ({ page }) => {
  // §6.2: No REST probe, no /api/v1/orgs call — auth is from persisted session + relay roles.
  let apiProbeCount = 0;

  await installE2EMocks(page, {
    authenticated: true,
    extension: true
  });

  await page.route('**/api/v1/orgs**', (route) => {
    apiProbeCount += 1;
    return route.fulfill({ json: { data: [] } });
  });

  await page.goto('/settings');

  // Settings requires 'owner' role but we didn't set one, so it should show permission denied
  await expect(page).toHaveURL('/settings');
  // No /api/v1/orgs probe was needed for auth
  expect(apiProbeCount).toBe(0);
});
