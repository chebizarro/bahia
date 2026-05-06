import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';
import { createPublicState, createPublicSystemInfo, installPublicServiceDeploymentHarness } from './harnesses/service-deployment-public.js';

const systemInfo = createPublicSystemInfo();
const initialState = createPublicState();

async function openDeployModal(page) {
  await page.goto('/services/svc-existing-1');
  await expect(page.getByRole('heading', { name: 'existing-service' })).toBeVisible();
  await page.getByRole('button', { name: 'Deploy' }).click();
  await expect(page.getByRole('dialog', { name: 'Create Deployment Intent' })).toBeVisible();
  await page.locator('#deploy-environment').selectOption('env-prod');
  await page.locator('#deploy-artifact').selectOption('artifact-existing-1');
  return page.getByRole('dialog', { name: 'Create Deployment Intent' });
}

test.describe('Deployment policy preview gate', () => {
  test('blocks intent creation when policy preview fails', async ({ page }) => {
    await installE2EMocks(page, { systemInfo });
    await installPublicServiceDeploymentHarness(page, {
      initialState,
      policyPreviewMode: 'error',
      policyPreviewError: 'preview backend offline'
    });

    const dialog = await openDeployModal(page);

    await expect(dialog.getByText('Policy preview unavailable: preview backend offline. A successful preview is required before creating a deployment intent.')).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Create Intent' })).toBeDisabled();

    await dialog.locator('form').evaluate((form) => form.requestSubmit());
    await expect(dialog.getByText('Policy preview must succeed before you can create an intent: preview backend offline')).toBeVisible();

    await expect.poll(() => page.evaluate(() => ({
      requestKinds: [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS],
      intentCount: window.__BAHIA_E2E_PUBLIC_STATE.deploymentIntents.length
    }))).toMatchObject({
      requestKinds: expect.arrayContaining([5989]),
      intentCount: 0
    });

    const requestKinds = await page.evaluate(() => [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS]);
    expect(requestKinds).not.toContain(5961);
  });

  test('blocks intent creation when policy preview reports blockers', async ({ page }) => {
    await installE2EMocks(page, { systemInfo });
    await installPublicServiceDeploymentHarness(page, {
      initialState,
      policyPreviewMode: 'block'
    });

    const dialog = await openDeployModal(page);

    await expect(dialog.getByText('Policy preview reported blocking failures. Resolve them before creating a deployment intent.')).toBeVisible();
    await expect(dialog.getByText('Artifact is missing a required signature.')).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Create Intent' })).toBeDisabled();

    await dialog.locator('form').evaluate((form) => form.requestSubmit());
    await expect(dialog.getByText('Resolve policy blockers before you can create an intent.')).toBeVisible();

    await expect.poll(() => page.evaluate(() => ({
      requestKinds: [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS],
      intentCount: window.__BAHIA_E2E_PUBLIC_STATE.deploymentIntents.length
    }))).toMatchObject({
      requestKinds: expect.arrayContaining([5989]),
      intentCount: 0
    });

    const requestKinds = await page.evaluate(() => [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS]);
    expect(requestKinds).not.toContain(5961);
  });
});
