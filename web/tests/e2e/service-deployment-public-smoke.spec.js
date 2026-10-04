import { test, expect } from '@playwright/test';
import { verifyEvent } from 'nostr-tools';
import { installE2EMocks } from './helpers.js';
import { TEST_ORG_ID, createPublicState, createPublicSystemInfo, installPublicServiceDeploymentHarness, reachDesiredStateReview } from './harnesses/service-deployment-public.js';

const systemInfo = createPublicSystemInfo();
const initialState = createPublicState();

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page, { systemInfo });
  await installPublicServiceDeploymentHarness(page, {
    initialState
  });
});

test.describe('Core service-to-deployment public controlplane smoke', () => {
  test('creates a service and publishes a pending deployment intent over signed Nostr', async ({ page }) => {
    await page.goto('/services');

    await expect(page.getByRole('heading', { name: 'Services', exact: true })).toBeVisible();
    await expect(page.getByRole('cell', { name: 'existing-service', exact: true })).toBeVisible();

    await page.getByRole('button', { name: 'Create Service' }).first().click();
    await expect(page.getByRole('dialog', { name: 'Create Service' })).toBeVisible();
    await page.locator('#service-org-id').fill(TEST_ORG_ID);
    await page.locator('#service-name').fill('created-service');
    await page.locator('#artifact-repo-path').fill('ghcr.io/example/created-service');
    await page.evaluate(() => {
      window.__bahiaCreatePublished = new Promise(resolve => {
        const onRequest = ({ detail }) => {
          if (detail?.tags?.some(tag => tag[0] === 'domain' && tag[1] === 'service') &&
            detail?.tags?.some(tag => tag[0] === 'op' && tag[1] === 'create')) {
            window.removeEventListener('__bahia_e2e_public_request', onRequest);
            resolve(detail);
          }
        };
        window.addEventListener('__bahia_e2e_public_request', onRequest);
      });
    });
    await page.getByRole('dialog', { name: 'Create Service' }).getByRole('button', { name: 'Create' }).click();

    await expect(page.getByRole('dialog', { name: 'Create Service' })).not.toBeVisible();
    expect(verifyEvent(await page.evaluate(() => window.__bahiaCreatePublished))).toBe(true);
    await expect(page.getByRole('cell', { name: 'created-service', exact: true })).toBeVisible();
    await expect(page.getByText('2 services')).toBeVisible();

    await page.goto('/services/svc-existing-1');
    await expect(page.getByRole('heading', { name: 'existing-service' })).toBeVisible();

    await page.getByRole('button', { name: 'Deploy', exact: true }).click();
    await expect(page.getByRole('dialog', { name: 'Create Deployment Intent' })).toBeVisible();
    await page.locator('#deploy-environment').selectOption('env-prod');
    await page.locator('#deploy-artifact').selectOption('artifact-existing-1');
    const deployDialog = page.getByRole('dialog', { name: 'Create Deployment Intent' });
    await reachDesiredStateReview(deployDialog);
    await expect(deployDialog.getByText('Exact signed desired state')).toBeVisible();
    await deployDialog.getByRole('button', { name: 'Sign & submit idempotently' }).click();

    await expect(page).toHaveURL(/\/deployments$/);
    await expect(page.getByTestId('deployment-pending-intents')).toContainText('Pending');
    const intent = await page.evaluate(() => window.__BAHIA_E2E_SIGNED_INTENTS.find(event =>
      event.tags.some(tag => tag[0] === 'domain' && tag[1] === 'deployment')
        && event.tags.some(tag => tag[0] === 'op' && tag[1] === 'create')));
    expect(intent.kind).toBe(30900);
    expect(JSON.parse(intent.content)).toMatchObject({ service_id: 'svc-existing-1',
      environment_id: 'env-prod', artifact_id: 'artifact-existing-1' });
    await page.evaluate(signed => window.__bahiaPushNostrEvent(window.__BAHIA_E2E_MAKE_INTENT_STATUS(signed,
      { id: `accepted-${signed.id}` })), intent);
    await expect(page.getByTestId('deployment-pending-intents')).toHaveCount(0);
  });
});
