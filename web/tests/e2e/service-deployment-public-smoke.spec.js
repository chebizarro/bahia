import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';
import { createPublicState, createPublicSystemInfo, installPublicServiceDeploymentHarness } from './harnesses/service-deployment-public.js';

const systemInfo = createPublicSystemInfo();
const initialState = createPublicState();

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page, { systemInfo });
  await installPublicServiceDeploymentHarness(page, { initialState });
});

test.describe('Core service-to-deployment public controlplane smoke', () => {
  test('creates a service and drives deployment approval/history over signer-first public Nostr flows', async ({ page }) => {
    await page.goto('/services');

    await expect(page.getByRole('heading', { name: 'Services' })).toBeVisible();
    await expect(page.getByRole('cell', { name: 'existing-service', exact: true })).toBeVisible();

    await page.getByRole('button', { name: 'Create Service' }).first().click();
    await expect(page.getByRole('dialog', { name: 'Create Service' })).toBeVisible();
    await page.locator('#service-name').fill('created-service');
    await page.locator('#artifact-repo-path').fill('ghcr.io/example/created-service');
    await page.getByRole('dialog', { name: 'Create Service' }).getByRole('button', { name: 'Create' }).click();

    await expect(page.getByRole('dialog', { name: 'Create Service' })).not.toBeVisible();

    await page.goto('/services/svc-existing-1');
    await expect(page.getByRole('heading', { name: 'existing-service' })).toBeVisible();

    await page.getByRole('button', { name: 'Deploy' }).click();
    await expect(page.getByRole('dialog', { name: 'Create Deployment Intent' })).toBeVisible();
    await page.locator('#deploy-environment').selectOption('env-prod');
    await page.locator('#deploy-artifact').selectOption('artifact-existing-1');
    await page.getByRole('button', { name: 'Create Intent' }).click();

    await expect.poll(() => page.evaluate(() => ({
      requestKinds: [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS],
      intentCount: window.__BAHIA_E2E_PUBLIC_STATE.deploymentIntents.length
    }))).toMatchObject({
      requestKinds: expect.arrayContaining([5961]),
      intentCount: 1
    });

    await expect(page).toHaveURL(/\/deployments$/);
    await page.reload();
    await expect(page.getByRole('heading', { name: 'Deployment History' })).toBeVisible();
    await expect(page.locator('tbody')).toContainText('existing-service');
    await expect(page.locator('tbody')).toContainText('pending');

    await page.goto('/deployments/pending');
    await expect(page.locator('tbody')).toContainText('existing-service');
    await page.locator('button:has-text("Approve")').first().click();
    await expect(page.getByRole('dialog', { name: 'Approve Deployment' })).toBeVisible();
    await page.getByRole('dialog', { name: 'Approve Deployment' }).getByRole('button', { name: 'Approve' }).click();
    await expect.poll(() => page.evaluate(() => ({
      requestKinds: [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS],
      runCount: window.__BAHIA_E2E_PUBLIC_STATE.deploymentRuns.length,
      approvalStates: window.__BAHIA_E2E_PUBLIC_STATE.deploymentIntents.map((intent) => intent.approval_status)
    }))).toMatchObject({
      requestKinds: expect.arrayContaining([5966]),
      runCount: 1,
      approvalStates: expect.arrayContaining(['approved'])
    });
    await page.reload();
    await expect(page.getByText('No pending approvals')).toBeVisible();

    await page.goto('/deployments');
    await page.reload();
    await expect(page.locator('tbody')).toContainText('existing-service');
    await expect(page.locator('tbody')).toContainText('completed');

    const intentLink = page.locator('tbody tr').first();
    await intentLink.click();
    await expect(page.getByRole('heading', { name: 'Deployment Intent' })).toBeVisible();
    await expect(page.getByText('Deployment Runs (1)')).toBeVisible();

    const transportTrace = await page.evaluate(() => ({
      relays: window.__BAHIA_E2E_PUBLIC_PUBLISHES.map((entry) => entry.relay),
      kinds: [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS]
    }));

    expect(transportTrace.relays.length).toBeGreaterThanOrEqual(4);
    expect(transportTrace.kinds).toEqual(expect.arrayContaining([5964, 5989, 5961, 5966]));
    expect(transportTrace.kinds).not.toContain(5980);
  });
});
