import { test, expect } from '@playwright/test';
import { attachRuntimeErrorGuards } from './helpers-console.js';
import {
  installEmptyRestFallbacks,
  installRelayBackedBrowserContext,
  startBahiaTestRelay
} from './relay-harness.js';

let relay;

test.describe.serial('relay-backed Bahia web functionality', () => {
  test.beforeAll(async () => {
    relay = await startBahiaTestRelay();
  });

  test.afterAll(async () => {
    await relay?.stop();
  });

  test.beforeEach(async ({ page }) => {
    await installRelayBackedBrowserContext(page, relay, { authenticated: true });
    await installEmptyRestFallbacks(page);
  });

  test('dashboard hydrates first-party read models through real REQ/EVENT/EOSE', async ({ page }) => {
    const assertNoRuntimeErrors = await attachRuntimeErrorGuards(page);

    await page.goto('/');

    await expect(page.locator('.card:has-text("Services") .card-value')).toHaveText('1');
    await expect(page.locator('.card:has-text("Environments") .card-value')).toHaveText('1');
    await expect(page.locator('.card:has-text("Workers") .card-value')).toHaveText('1');
    await expect(page.locator('.card:has-text("Drifted") .card-value')).toHaveText('1');
    await expect(page.getByText('service.created')).toBeVisible();
    await assertNoRuntimeErrors();
  });

  test('service, deployment, package, and worker routes render seeded relay projections', async ({ page }) => {
    const assertNoRuntimeErrors = await attachRuntimeErrorGuards(page);

    await page.goto('/services');
    await expect(page.getByText('Checkout API')).toBeVisible();

    await page.goto('/deployments');
    await expect(page.locator('body')).toContainText(/intent-1|run-1|Checkout API/);

    await page.goto('/packages');
    await expect(page.locator('body')).toContainText('packages');

    await page.goto('/workers');
    await expect(page.getByText('worker-one')).toBeVisible();
    await assertNoRuntimeErrors();
  });

  test('DNS and FIPS mesh pages use relay metadata and DNS read models', async ({ page }) => {
    const assertNoRuntimeErrors = await attachRuntimeErrorGuards(page);

    await page.goto('/dns');
    await expect(page.getByRole('heading', { name: 'DNS management' })).toBeVisible();
    await expect(page.getByText('prod.example.com')).toBeVisible();

    await page.getByRole('button', { name: 'Endpoints' }).click();
    await expect(page.getByText('checkout.prod.example.com')).toBeVisible();

    await page.getByRole('button', { name: 'FIPS/Mesh' }).click();
    await expect(page.getByRole('heading', { name: 'FIPS mesh' })).toBeVisible();
    await expect(page.getByRole('cell', { name: /worker-one/ })).toBeVisible();
    await assertNoRuntimeErrors();
  });

  test('events consume relay-backed audit events', async ({ page }) => {
    const assertNoRuntimeErrors = await attachRuntimeErrorGuards(page);

    await page.goto('/events');
    await expect(page.getByRole('heading', { name: 'Live Events' })).toBeVisible();
    await expect(page.getByText('service.created')).toBeVisible();
    await expect(page.locator('.hint')).toContainText(relay.wsUrl);
    await assertNoRuntimeErrors();
  });
});
