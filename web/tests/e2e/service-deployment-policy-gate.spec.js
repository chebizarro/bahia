import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';
import {
  advanceDesiredStateWizardToReliability,
  createPublicState,
  createPublicSystemInfo,
  installPublicServiceDeploymentHarness
} from './harnesses/service-deployment-public.js';

const systemInfo = createPublicSystemInfo();
const initialState = createPublicState();

async function openDeployReliabilityStep(page) {
  await page.goto('/services/svc-existing-1');
  await expect(page.getByRole('heading', { name: 'existing-service' })).toBeVisible();
  await page.getByRole('button', { name: 'Deploy' }).click();
  const dialog = page.getByRole('dialog', { name: 'Create Deployment Intent' });
  await dialog.getByLabel('Environment *').selectOption('env-prod');
  await dialog.getByLabel('Artifact from recent builds *').selectOption('artifact-existing-1');
  await advanceDesiredStateWizardToReliability(dialog);
  return dialog;
}

test.describe('Deployment policy preview gate', () => {
  test('blocks submission when the signed desired-state preview fails', async ({ page }) => {
    await installE2EMocks(page, { systemInfo });
    await installPublicServiceDeploymentHarness(page, {
      initialState,
      policyPreviewMode: 'error',
      policyPreviewError: 'preview backend offline'
    });

    const dialog = await openDeployReliabilityStep(page);
    await dialog.getByRole('button', { name: 'Continue' }).click();

    await expect(dialog.getByText('preview backend offline')).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Sign & submit idempotently' })).toHaveCount(0);

    const operations = await page.evaluate(() => window.__BAHIA_E2E_PUBLIC_REQUESTS.map((request) => request.operation));
    expect(operations).toContain('service/deploy-preview');
    expect(operations).not.toContain('service/deploy');
  });

  test('shows policy blockers beside the exact desired state and disables signing', async ({ page }) => {
    await installE2EMocks(page, { systemInfo });
    await installPublicServiceDeploymentHarness(page, {
      initialState,
      policyPreviewMode: 'block'
    });

    const dialog = await openDeployReliabilityStep(page);
    await dialog.getByRole('button', { name: 'Continue' }).click();

    await expect(dialog.getByText('Exact signed desired state')).toBeVisible();
    await expect(dialog.getByText('Policy preview reported blocking failures. Resolve them before creating a deployment intent.')).toBeVisible();
    await expect(dialog.getByText('Artifact is missing a required signature.')).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Sign & submit idempotently' })).toBeDisabled();

    const operations = await page.evaluate(() => window.__BAHIA_E2E_PUBLIC_REQUESTS.map((request) => request.operation));
    expect(operations).not.toContain('service/deploy');
  });

  test('waits for policy evaluation, then signs and submits the reviewed hash', async ({ page }) => {
    await installE2EMocks(page, { systemInfo });
    await installPublicServiceDeploymentHarness(page, {
      initialState,
      policyPreviewMode: 'delay'
    });

    const dialog = await openDeployReliabilityStep(page);
    await dialog.getByRole('button', { name: 'Continue' }).click();
    await expect.poll(() => page.evaluate(() => window.__BAHIA_E2E_PUBLIC_LIST_PENDING_POLICY_PREVIEWS?.().length || 0)).toBe(1);
    await expect(dialog.getByRole('button', { name: 'Continue' })).toBeDisabled();

    await page.evaluate(() => window.__BAHIA_E2E_PUBLIC_RESOLVE_POLICY_PREVIEW?.('allow'));

    await expect(dialog.getByText('Exact signed desired state')).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Sign & submit idempotently' })).toBeEnabled();
    await dialog.getByRole('button', { name: 'Sign & submit idempotently' }).click();

    await expect(page).toHaveURL(/\/deployments$/);
    const trace = await page.evaluate(() => window.__BAHIA_E2E_PUBLIC_REQUESTS);
    const deploy = trace.find((request) => request.operation === 'service/deploy');
    expect(deploy.payload.expected_desired_state_hash).toBe(`sha256:${'d'.repeat(64)}`);
    expect(deploy.payload.idempotency_key).toBe(deploy.payload.expected_desired_state_hash);
  });
});
