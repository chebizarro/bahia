import { test, expect } from '@playwright/test';

// Mock API responses to avoid needing a running Go backend
test.beforeEach(async ({ page }) => {
  // Mock all API endpoints with empty/default responses
  await page.route('**/api/v1/**', (route) => {
    const url = route.request().url();
    
    // Return appropriate mock data based on endpoint
    if (url.includes('/services')) {
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
    } else if (url.includes('/events/stream')) {
      // Mock SSE endpoint
      return route.fulfill({
        status: 200,
        contentType: 'text/event-stream',
        body: ''
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

  test('should render navigation without errors', async ({ page }) => {
    const consoleErrors = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        consoleErrors.push(msg.text());
      }
    });

    await page.goto('/');
    await page.waitForLoadState('networkidle');

    // Check that no console errors occurred during navigation
    // Filter out expected SSE-related errors since we're mocking the backend
    const unexpectedErrors = consoleErrors.filter(err => 
      !err.includes('EventSource') && 
      !err.includes('SSE') &&
      !err.includes('event stream')
    );
    
    expect(unexpectedErrors.length).toBe(0);
  });
});
