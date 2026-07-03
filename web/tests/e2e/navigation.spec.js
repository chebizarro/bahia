import { test, expect } from '@playwright/test';
import { E2E_SERVICE_PUBKEY, installE2EMocks } from './helpers.js';
import { attachRuntimeErrorGuards } from './helpers-console.js';

const BROWSER_RELAY = 'ws://relay.test.local';
const SERVICE_PUBKEY = E2E_SERVICE_PUBKEY;
const systemInfo = {
  nostr: {
    browser_relays: [BROWSER_RELAY],
    service_pubkey: SERVICE_PUBKEY
  },
  features: {
    relay_sidecar: true,
    relay_read_models: true,
    legacy_sse: false
  }
};

// Mock API responses to avoid needing a running Go backend
test.beforeEach(async ({ page }) => {
  await installE2EMocks(page, { authenticated: false, extension: false, nostrEvents: [], systemInfo });

  // Mock all API endpoints with empty/default responses
  await page.route('**/api/v1/**', (route) => {
    const url = route.request().url();
    
    // Return appropriate mock data based on endpoint
    if (url.includes('/system/info')) {
      return route.fulfill({ 
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          data: {
            nostr: systemInfo.nostr,
            features: systemInfo.features
          }
        })
      });
    } else if (url.includes('/services')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: [] })
      });
    } else if (url.includes('/environments')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: [] })
      });
    }
    
    // Default response for unknown endpoints
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: null })
    });
  });
});

test.describe('Navigation', () => {
  test('should load the home page', async ({ page }) => {
    await page.goto('/');
    
    // Wait for the page to load
    await page.waitForLoadState('networkidle');
    
    // Check that the app rendered
    // The exact content will depend on your +page.svelte, but we can check for basic structure
    const body = await page.locator('body');
    await expect(body).toBeVisible();
  });

  test('should navigate to services page', async ({ page }) => {
    await page.goto('/');
    
    // Wait for navigation elements to load
    await page.waitForLoadState('networkidle');
    
    // Navigate to services page
    await page.goto('/services');
    
    // Verify we're on the services page
    await expect(page).toHaveURL('/services');
    
    // Check for services page content
    // This will pass even with an empty list since we're mocking empty data
    const pageContent = await page.locator('body');
    await expect(pageContent).toBeVisible();
  });

  test('should navigate to environments page', async ({ page }) => {
    await page.goto('/environments');
    
    // Verify we're on the environments page
    await expect(page).toHaveURL('/environments');
    
    const pageContent = await page.locator('body');
    await expect(pageContent).toBeVisible();
  });

  test('exposes global and contextual documentation actions', async ({ page }) => {
    await page.goto('/services');
    await page.waitForLoadState('networkidle');

    await expect(page.getByRole('link', { name: 'Docs' })).toHaveAttribute('href', '/docs');
    await expect(page.getByRole('link', { name: 'Open Services documentation' })).toHaveAttribute('href', '/docs/features-services');

    await page.goto('/settings');
    await page.waitForLoadState('networkidle');

    await expect(page.getByRole('link', { name: 'Docs' })).toHaveAttribute('href', '/docs');
    await expect(page.getByRole('link', { name: /^Help:/ })).toHaveCount(0);
  });

  test('should render navigation without errors', async ({ page }) => {
    const assertNoRuntimeErrors = await attachRuntimeErrorGuards(page);

    await page.goto('/');
    await page.waitForLoadState('networkidle');

    await assertNoRuntimeErrors();
  });
});
