import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';
import { createPublicState, createPublicSystemInfo, installPublicServiceDeploymentHarness } from './harnesses/service-deployment-public.js';

const systemInfo = createPublicSystemInfo();
const initialState = createPublicState();
const delayedPreviewState = createPublicState({
  artifacts: [
    ...initialState.artifacts,
    {
      id: 'artifact-existing-2',
      service_id: 'svc-existing-1',
      build_id: 'build-existing-1',
      image_repo: 'ghcr.io/example/existing-service',
      image_tag: 'v1.2.4',
      image_digest: 'sha256:bbbbbbabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd',
      metadata: { build_id: 'build-existing-1' },
      created_at: '2026-05-03T10:07:00.000Z'
    }
  ]
});

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

  test('blocks intent creation while policy preview is still loading, then allows creation after a successful preview resolves', async ({ page }) => {
    await installE2EMocks(page, { systemInfo });
    await installPublicServiceDeploymentHarness(page, {
      initialState,
      policyPreviewMode: 'delay'
    });

    const dialog = await openDeployModal(page);

    await expect(dialog.getByText('Evaluating policies...')).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Create Intent' })).toBeDisabled();

    await dialog.locator('form').evaluate((form) => form.requestSubmit());
    await expect(dialog.getByText('Policy preview must finish before you can create an intent.')).toBeVisible();

    let requestKinds = await page.evaluate(() => [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS]);
    expect(requestKinds).toContain(5989);
    expect(requestKinds).not.toContain(5961);

    await page.evaluate(() => window.__BAHIA_E2E_PUBLIC_RESOLVE_POLICY_PREVIEW?.('allow'));

    await expect(dialog.getByText('Evaluating policies...')).not.toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Create Intent' })).toBeEnabled();

    await dialog.getByRole('button', { name: 'Create Intent' }).click();
    await expect.poll(() => page.evaluate(() => ({
      requestKinds: [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS],
      intentCount: window.__BAHIA_E2E_PUBLIC_STATE.deploymentIntents.length
    }))).toMatchObject({
      requestKinds: expect.arrayContaining([5989, 5961]),
      intentCount: 1
    });

    requestKinds = await page.evaluate(() => [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS]);
    expect(requestKinds).toContain(5961);
  });

  test('ignores stale delayed preview responses after a newer preview request supersedes them', async ({ page }) => {
    await installE2EMocks(page, { systemInfo });
    await installPublicServiceDeploymentHarness(page, {
      initialState: delayedPreviewState,
      policyPreviewMode: 'delay'
    });

    const dialog = await openDeployModal(page);

    await expect(dialog.getByText('Evaluating policies...')).toBeVisible();

    const firstPendingIds = await page.evaluate(() => window.__BAHIA_E2E_PUBLIC_LIST_PENDING_POLICY_PREVIEWS?.() || []);
    expect(firstPendingIds).toHaveLength(1);

    await page.locator('#deploy-artifact').selectOption('artifact-existing-2');
    await expect(dialog.getByText('Evaluating policies...')).toBeVisible();

    const pendingIds = await page.evaluate(() => window.__BAHIA_E2E_PUBLIC_LIST_PENDING_POLICY_PREVIEWS?.() || []);
    expect(pendingIds).toHaveLength(2);

    await page.evaluate(([staleId]) => window.__BAHIA_E2E_PUBLIC_RESOLVE_POLICY_PREVIEW?.(staleId, 'allow'), [firstPendingIds[0]]);

    await expect(dialog.getByText('Evaluating policies...')).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Create Intent' })).toBeDisabled();

    const latestId = pendingIds.find((id) => id !== firstPendingIds[0]);
    await page.evaluate(([requestId]) => window.__BAHIA_E2E_PUBLIC_RESOLVE_POLICY_PREVIEW?.(requestId, 'allow'), [latestId]);

    await expect(dialog.getByText('Evaluating policies...')).not.toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Create Intent' })).toBeEnabled();
  });
});
