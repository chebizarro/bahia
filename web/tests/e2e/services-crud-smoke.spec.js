import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';
import { SERVICE_PUBKEY, createPublicState, createPublicSystemInfo, installPublicServiceDeploymentHarness } from './harnesses/service-deployment-public.js';

const systemInfo = createPublicSystemInfo();

async function setupServices(page, initialState = createPublicState()) {
  await installE2EMocks(page, { systemInfo });
  await installPublicServiceDeploymentHarness(page, { initialState });
}

async function serviceTrace(page) {
  return page.evaluate(() => ({
    relays: window.__BAHIA_E2E_PUBLIC_PUBLISHES.map((entry) => entry.relay),
    requests: window.__BAHIA_E2E_PUBLIC_REQUESTS,
    oks: window.__BAHIA_E2E_PUBLIC_OKS,
    results: window.__BAHIA_E2E_PUBLIC_RESULTS,
    projections: window.__BAHIA_E2E_PUBLIC_PROJECTIONS,
    kinds: [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS]
  }));
}

async function expectContextVMOperation(page, operation) {
  await expect.poll(() => serviceTrace(page)).toMatchObject({
    requests: expect.arrayContaining([expect.objectContaining({ kind: 25910, operation })]),
    oks: expect.arrayContaining([expect.objectContaining({ kind: 25910, accepted: true })]),
    results: expect.arrayContaining([expect.objectContaining({ kind: 25910 })]),
    projections: expect.arrayContaining([expect.objectContaining({ kind: 30900 })]),
    kinds: expect.arrayContaining([25910])
  });

  const trace = await serviceTrace(page);
  expect(trace.kinds).not.toContain(5980);
  const request = trace.requests.find((entry) => entry.operation === operation);
  expect(request).toBeTruthy();
  expect(trace.results).toEqual(expect.arrayContaining([expect.objectContaining({ requestEventId: request.eventId })]));
  expect(trace.projections).toEqual(expect.arrayContaining([expect.objectContaining({ requestEventId: request.eventId, kind: 30900 })]));
  return request;
}

function pagedServicesState() {
  return createPublicState({
    services: Array.from({ length: 30 }, (_, index) => ({
      id: `svc-page-${String(index + 1).padStart(2, '0')}`,
      name: `service-${index + 1}`,
      repo_url: '',
      artifact_repo: `ghcr.io/acme/service-${index + 1}`,
      runtime_type: index < 15 ? 'docker' : 'kubernetes',
      default_branch: 'main',
      deleted: false,
      created_at: `2026-05-03T10:${String(index).padStart(2, '0')}:00.000Z`
    }))
  });
}

test.describe('Services CRUD Smoke Test', () => {
  test('loads services from canonical relay read models', async ({ page }) => {
    await setupServices(page);

    await page.goto('/services');

    await expect(page.getByRole('heading', { name: 'Services', exact: true })).toBeVisible();
    await expect(page.getByRole('cell', { name: 'existing-service', exact: true })).toBeVisible();
    await expect(page.getByText('1 services')).toBeVisible();
  });

  test('creates a service through ContextVM and canonical 30900 projection', async ({ page }) => {
    await setupServices(page);

    await page.goto('/services');
    await page.getByRole('button', { name: 'Create Service' }).first().click();

    const dialog = page.getByRole('dialog', { name: 'Create Service' });
    await expect(dialog).toBeVisible();

    await dialog.locator('#service-name').fill('test-service');
    await dialog.locator('#artifact-repo-path').fill('ghcr.io/test/test-service');
    await dialog.locator('#runtime-type').selectOption('docker');
    await dialog.locator('#default-branch').fill('main');
    await expect(dialog.getByRole('button', { name: 'Choose Repository' })).toBeVisible();

    await dialog.getByRole('button', { name: 'Create' }).click();

    await expect(dialog).not.toBeVisible();
    await expect(page.getByRole('cell', { name: 'test-service', exact: true })).toBeVisible();
    await expect(page.getByText('2 services')).toBeVisible();

    const request = await expectContextVMOperation(page, 'service/create');
    expect(request.tags).toEqual(expect.arrayContaining([
      ['p', SERVICE_PUBKEY],
      ['encrypted', 'contextvm-jsonrpc-v1'],
      ['method', 'service/create']
    ]));
    expect(JSON.parse(request.content).params).toMatchObject({
      name: 'test-service',
      repo_url: '',
      artifact_repo: 'ghcr.io/test/test-service',
      runtime_type: 'docker',
      default_branch: 'main'
    });
  });

  test('shows validation error for empty name without publishing', async ({ page }) => {
    await setupServices(page);

    await page.goto('/services');
    await page.getByRole('button', { name: 'Create Service' }).first().click();

    const dialog = page.getByRole('dialog', { name: 'Create Service' });
    await expect(dialog).toBeVisible();
    await dialog.locator('#artifact-repo-path').fill('ghcr.io/test/test-service');
    await dialog.getByRole('button', { name: 'Create' }).click();

    await expect(dialog).toBeVisible();
    await expect(dialog.locator('#service-name')).toBeFocused();
    await expect.poll(() => serviceTrace(page)).toMatchObject({ requests: [] });
  });

  test('shows validation error for empty artifact repository without publishing', async ({ page }) => {
    await setupServices(page);

    await page.goto('/services');
    await page.getByRole('button', { name: 'Create Service' }).first().click();

    const dialog = page.getByRole('dialog', { name: 'Create Service' });
    await expect(dialog).toBeVisible();
    await dialog.locator('#service-name').fill('test-service');
    await dialog.getByRole('button', { name: 'Create' }).click();

    await expect(dialog).toBeVisible();
    await expect(dialog.locator('#artifact-repo-path')).toBeFocused();
    await expect.poll(() => serviceTrace(page)).toMatchObject({ requests: [] });
  });

  test('closes the create modal on cancel', async ({ page }) => {
    await setupServices(page);

    await page.goto('/services');
    await page.getByRole('button', { name: 'Create Service' }).first().click();

    const dialog = page.getByRole('dialog', { name: 'Create Service' });
    await expect(dialog).toBeVisible();
    await dialog.getByRole('button', { name: 'Cancel' }).click();

    await expect(dialog).not.toBeVisible();
  });

  test('supports search, runtime filtering, and pagination controls over relay read models', async ({ page }) => {
    await setupServices(page, pagedServicesState());

    await page.goto('/services');

    await expect(page.locator('#service-search')).toBeVisible();
    await expect(page.locator('#runtime-filter')).toBeVisible();
    await expect(page.locator('#page-size')).toBeVisible();

    await expect(page.locator('tbody tr')).toHaveCount(25);
    await expect(page.getByText('Page 1 of 2')).toBeVisible();

    await page.locator('#page-size').selectOption('10');
    await expect(page.getByText('Page 1 of 3')).toBeVisible();
    await expect(page.locator('tbody tr')).toHaveCount(10);

    await page.getByRole('button', { name: 'Next' }).click();
    await expect(page.getByText('Page 2 of 3')).toBeVisible();

    await page.locator('#service-search').fill('service-2');
    await expect(page.locator('tbody tr')).toHaveCount(10);
    await expect(page.getByText('Page 1 of 2')).toBeVisible();

    await page.locator('#runtime-filter').selectOption('kubernetes');
    await expect(page.locator('tbody tr')).toHaveCount(10);
    await expect(page.getByText('Page 1 of 1')).not.toBeVisible();

    await page.locator('#page-size').selectOption('50');
    await expect(page.locator('tbody tr')).toHaveCount(10);
    await expect(page.getByText('Page 1 of 1')).not.toBeVisible();
  });
});
