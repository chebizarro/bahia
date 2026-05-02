import { test } from '@playwright/test';
import { installE2EMocks } from './helpers.js';
import { DashboardPage } from './page-objects/dashboard.page.js';

test.describe('Dashboard Page Object Smoke', () => {
  test('loads dashboard via page object', async ({ page }) => {
    await installE2EMocks(page);
    const dashboard = new DashboardPage(page);

    await dashboard.goto();
    await dashboard.waitForReady();
    await dashboard.expectLoaded();
  });
});
