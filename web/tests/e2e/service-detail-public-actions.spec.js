import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';
import { createPublicState, createPublicSystemInfo, installPublicServiceDeploymentHarness } from './harnesses/service-deployment-public.js';

const systemInfo = createPublicSystemInfo();

function serviceDetailState() {
  return createPublicState({
    artifacts: [
      {
        id: 'artifact-existing-1',
        service_id: 'svc-existing-1',
        build_id: 'build-existing-1',
        image_repo: 'ghcr.io/example/existing-service',
        image_tag: 'v1.2.3',
        image_digest: 'sha256:abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd',
        metadata: { build_id: 'build-existing-1' },
        created_at: '2026-05-03T10:06:00.000Z'
      },
      {
        id: 'artifact-previous-1',
        service_id: 'svc-existing-1',
        build_id: 'build-existing-1',
        image_repo: 'ghcr.io/example/existing-service',
        image_tag: 'v1.2.2',
        image_digest: 'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
        metadata: { build_id: 'build-existing-1' },
        created_at: '2026-05-02T10:06:00.000Z'
      }
    ]
  });
}

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page, { systemInfo });
  await installPublicServiceDeploymentHarness(page, { initialState: serviceDetailState() });
});

test.describe('Service detail signer-first public actions', () => {
  test('edits a service and creates a rollback intent through public commands', async ({ page }) => {
    await page.goto('/services/svc-existing-1');
    await expect(page.getByRole('heading', { name: 'existing-service' })).toBeVisible();

    await page.getByRole('button', { name: 'Edit' }).click();
    const editDialog = page.getByRole('dialog', { name: 'Edit Service' });
    await expect(editDialog).toBeVisible();
    await editDialog.locator('#edit-name').fill('existing-service-renamed');
    await editDialog.getByRole('button', { name: 'Save' }).click();

    await expect(editDialog).not.toBeVisible();
    await expect(page.getByRole('heading', { name: 'existing-service-renamed' })).toBeVisible();
    await expect.poll(() => page.evaluate(() => ({
      requestKinds: [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS],
      service: window.__BAHIA_E2E_PUBLIC_STATE.services.find((item) => item.id === 'svc-existing-1')
    }))).toMatchObject({
      requestKinds: expect.arrayContaining([5981]),
      service: expect.objectContaining({ name: 'existing-service-renamed' })
    });

    await page.getByRole('button', { name: 'Rollback' }).click();
    const rollbackDialog = page.getByRole('dialog', { name: 'Confirm Rollback' });
    await expect(rollbackDialog).toBeVisible();
    await rollbackDialog.locator('#rollback-environment').selectOption('env-prod');
    await rollbackDialog.getByRole('button', { name: 'Create Rollback Intent' }).click();

    await expect(page).toHaveURL(/\/deployments$/);
    await expect.poll(() => page.evaluate(() => ({
      requestKinds: [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS],
      intents: window.__BAHIA_E2E_PUBLIC_STATE.deploymentIntents
    }))).toMatchObject({
      requestKinds: expect.arrayContaining([5962]),
      intents: expect.arrayContaining([
        expect.objectContaining({
          service_id: 'svc-existing-1',
          environment_id: 'env-prod',
          source_kind: 'rollback'
        })
      ])
    });
  });

  test('deletes a service through a public signer-first command', async ({ page }) => {
    await page.goto('/services/svc-existing-1');
    await expect(page.getByRole('heading', { name: 'existing-service' })).toBeVisible();

    await page.getByRole('button', { name: 'Delete' }).click();
    const deleteDialog = page.getByRole('dialog', { name: 'Delete Service' });
    await expect(deleteDialog).toBeVisible();
    await deleteDialog.locator('#delete-force').check();
    await deleteDialog.getByRole('button', { name: 'Delete' }).click();

    await expect(page).toHaveURL(/\/services$/);
    await expect.poll(() => page.evaluate(() => ({
      requestKinds: [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS],
      service: window.__BAHIA_E2E_PUBLIC_STATE.services.find((item) => item.id === 'svc-existing-1')
    }))).toMatchObject({
      requestKinds: expect.arrayContaining([5982]),
      service: expect.objectContaining({ deleted: true })
    });

    await page.reload();
    await expect(page.getByRole('cell', { name: 'existing-service', exact: true })).toHaveCount(0);
  });
});
