import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';
import { createPublicState, createPublicSystemInfo, installPublicServiceDeploymentHarness } from './harnesses/service-deployment-public.js';

const systemInfo = createPublicSystemInfo();

async function openCreateDialog(page) {
  await page.getByRole('button', { name: 'Create Service' }).first().click();
  await expect(page.getByRole('dialog', { name: 'Create Service' })).toBeVisible();
  return page.getByRole('dialog', { name: 'Create Service' });
}

async function submitCreate(dialog, { name, artifactRepo, runtimeType = 'docker' }) {
  await dialog.locator('#service-name').fill(name);
  await dialog.locator('#artifact-repo-path').fill(artifactRepo);
  await dialog.locator('#runtime-type').selectOption(runtimeType);
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(dialog).not.toBeVisible();
}

function pagedServicesState() {
  return createPublicState({
    services: Array.from({ length: 30 }, (_, index) => ({
      id: `svc-page-${String(index + 1).padStart(2, '0')}`,
      name: `service-${String(index + 1).padStart(2, '0')}`,
      repo_url: '',
      artifact_repo: `ghcr.io/example/service-${String(index + 1).padStart(2, '0')}`,
      runtime_type: 'docker',
      default_branch: 'main',
      deleted: false,
      created_at: `2026-05-03T10:${String(index).padStart(2, '0')}:00.000Z`
    }))
  });
}

test.describe('Service create visibility with preserved list state', () => {
  test('preserves active runtime/search filters and does not force-show a non-matching created service', async ({ page }) => {
    await installE2EMocks(page, { systemInfo });
    await installPublicServiceDeploymentHarness(page, {
      initialState: createPublicState(),
      emitCreateServiceProjection: false
    });

    await page.goto('/services');
    await expect(page.getByRole('heading', { name: 'Services' })).toBeVisible();

    await page.locator('#service-search').fill('existing');
    await page.locator('#runtime-filter').selectOption('docker');
    await expect(page.getByRole('cell', { name: 'existing-service', exact: true })).toBeVisible();

    const dialog = await openCreateDialog(page);
    await submitCreate(dialog, {
      name: 'compose-hidden',
      artifactRepo: 'ghcr.io/example/compose-hidden',
      runtimeType: 'compose'
    });

    await expect(page.locator('#service-search')).toHaveValue('existing');
    await expect(page.locator('#runtime-filter')).toHaveValue('docker');
    await expect(page.getByRole('cell', { name: 'existing-service', exact: true })).toBeVisible();
    await expect(page.getByRole('cell', { name: 'compose-hidden', exact: true })).not.toBeVisible();
    await expect(page.getByText('2 services')).toBeVisible();
  });

  test('shows the created service immediately when it already matches the active search view', async ({ page }) => {
    await installE2EMocks(page, { systemInfo });
    await installPublicServiceDeploymentHarness(page, {
      initialState: createPublicState(),
      emitCreateServiceProjection: false
    });

    await page.goto('/services');
    await expect(page.getByRole('heading', { name: 'Services' })).toBeVisible();

    await page.locator('#service-search').fill('created-match');
    await expect(page.getByText('No services match current filters')).toBeVisible();

    const dialog = await openCreateDialog(page);
    await submitCreate(dialog, {
      name: 'created-match',
      artifactRepo: 'ghcr.io/example/created-match'
    });

    await expect(page.locator('#service-search')).toHaveValue('created-match');
    await expect(page.getByRole('cell', { name: 'created-match', exact: true })).toBeVisible();
    await expect(page.getByText('No services match current filters')).not.toBeVisible();
  });

  test('preserves the current page instead of resetting pagination after create', async ({ page }) => {
    await installE2EMocks(page, { systemInfo });
    await installPublicServiceDeploymentHarness(page, {
      initialState: pagedServicesState(),
      emitCreateServiceProjection: false
    });

    await page.goto('/services');
    await expect(page.getByRole('heading', { name: 'Services' })).toBeVisible();

    await page.locator('#page-size').selectOption('10');
    await expect(page.getByText('Page 1 of 3')).toBeVisible();
    await page.getByRole('button', { name: 'Next' }).click();
    await expect(page.getByText('Page 2 of 3')).toBeVisible();
    await expect(page.getByRole('cell', { name: 'service-11', exact: true })).toBeVisible();

    const dialog = await openCreateDialog(page);
    await submitCreate(dialog, {
      name: 'zzz-created-page',
      artifactRepo: 'ghcr.io/example/zzz-created-page'
    });

    await expect(page.getByText('Page 2 of 4')).toBeVisible();
    await expect(page.getByRole('cell', { name: 'service-11', exact: true })).toBeVisible();
    await expect(page.getByRole('cell', { name: 'zzz-created-page', exact: true })).not.toBeVisible();
    await expect(page.getByText('31 services')).toBeVisible();
  });
});
