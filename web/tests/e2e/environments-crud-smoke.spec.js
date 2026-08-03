import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';
import { SERVICE_PUBKEY, createPublicState, createPublicSystemInfo, installPublicServiceDeploymentHarness } from './harnesses/service-deployment-public.js';

const systemInfo = createPublicSystemInfo();
const ORG_ID = '11111111-1111-4111-8111-111111111111';
const initialState = createPublicState({
  services: [
    { id: 'svc-1', name: 'api-service', runtime_type: 'docker', artifact_repo: 'ghcr.io/example/api', deleted: false, created_at: '2026-05-03T10:00:00.000Z' },
    { id: 'svc-2', name: 'worker-service', runtime_type: 'docker', artifact_repo: 'ghcr.io/example/worker', deleted: false, created_at: '2026-05-03T10:01:00.000Z' }
  ],
  environments: [
    {
      id: 'env-1',
      org_id: ORG_ID,
      name: 'production',
      loom_worker_selector: 'role=prod',
      runtime_config: { cpu_limit: '2', memory_limit: '4Gi' },
      targeting: {
        default_unit_key: 'max',
        secret_scope_mode: 'unit',
        default_reconcile_mode: 'approval_required'
      },
      reconcile_mode: 'approval_required',
      deployment_units: [{
        id: 'unit-max',
        environment_id: 'env-1',
        key: 'max',
        display_name: 'Max Compose',
        runtime_type: 'compose',
        endpoint_ref: 'max',
        compose_dir: '/srv/bahia/compose/gastown',
        ownership_mode: 'bahia_managed',
        reconcile_mode: 'approval_required',
        runtime_config: { execution_mode: 'sdk' },
        implicit: false
      }],
      deploy_strategy: 'replace',
      protected: true,
      deleted: false,
      created_at: '2026-05-03T10:02:00.000Z',
      updated_at: '2026-05-03T10:02:30.000Z'
    },
    {
      id: 'env-2',
      org_id: ORG_ID,
      name: 'staging',
      loom_worker_selector: 'role=staging',
      runtime_config: { cpu_limit: '1', memory_limit: '2Gi' },
      deploy_strategy: 'canary',
      protected: false,
      deleted: false,
      created_at: '2026-05-03T10:03:00.000Z',
      updated_at: '2026-05-03T10:03:30.000Z'
    }
  ],
  serviceStates: [
    {
      id: 'state-1',
      service_id: 'svc-1',
      environment_id: 'env-1',
      artifact_id: 'artifact-aaa111',
      status: 'running',
      drift_status: 'in_sync',
      deployed_at: '2026-05-03T10:04:00.000Z',
      deleted: false
    },
    {
      id: 'state-2',
      service_id: 'svc-2',
      environment_id: 'env-1',
      artifact_id: 'artifact-bbb222',
      status: 'running',
      drift_status: 'drifted',
      deployed_at: '2026-05-03T10:05:00.000Z',
      deleted: false
    }
  ],
  deploymentIntents: [
    {
      id: 'intent-1',
      service_id: 'svc-1',
      environment_id: 'env-1',
      artifact_id: 'artifact-aaa111',
      approval_status: 'approved',
      deployment_status: 'completed',
      created_at: '2026-05-03T10:06:00.000Z'
    },
    {
      id: 'intent-2',
      service_id: 'svc-2',
      environment_id: 'env-1',
      artifact_id: 'artifact-bbb222',
      approval_status: 'pending',
      deployment_status: '',
      created_at: '2026-05-03T10:07:00.000Z'
    }
  ]
});

async function environmentTrace(page) {
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
  await expect.poll(() => environmentTrace(page)).toMatchObject({
    requests: expect.arrayContaining([expect.objectContaining({ kind: 25910, operation })]),
    oks: expect.arrayContaining([expect.objectContaining({ kind: 25910, accepted: true })]),
    results: expect.arrayContaining([expect.objectContaining({ kind: 25910 })]),
    projections: expect.arrayContaining([expect.objectContaining({ kind: 30900 })]),
    kinds: expect.arrayContaining([25910])
  });
  const trace = await environmentTrace(page);
  expect(trace.kinds).not.toContain(5980);
  const request = trace.requests.find((entry) => entry.operation === operation);
  expect(trace.results).toEqual(expect.arrayContaining([expect.objectContaining({ requestEventId: request.eventId })]));
  expect(trace.projections).toEqual(expect.arrayContaining([expect.objectContaining({ requestEventId: request.eventId, kind: 30900 })]));
  return request;
}

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page, { systemInfo });
  await installPublicServiceDeploymentHarness(page, { initialState });
});

test.describe('Environments CRUD Smoke Test', () => {
  test('should load environments page from canonical relay read models', async ({ page }) => {
    await page.goto('/environments');

    await expect(page.locator('h1:has-text("Environments")')).toBeVisible();
    await expect(page.locator('text=production')).toBeVisible();
    await expect(page.locator('text=staging')).toBeVisible();
  });

  test('should open Create Environment modal', async ({ page }) => {
    await page.goto('/environments');

    await page.getByRole('button', { name: 'Create Environment' }).click();

    await expect(page.getByRole('dialog', { name: 'Create Environment' })).toBeVisible();
    await expect(page.getByLabel('Organization *')).toBeVisible();
    await expect(page.getByLabel('Name *')).toBeVisible();
    await expect(page.getByLabel('Loom Worker Selector')).toBeVisible();
    await expect(page.getByLabel('Runtime Config (JSON)')).toBeVisible();
    await expect(page.getByLabel('Deploy Strategy *')).toBeVisible();
    await expect(page.getByLabel('Protected (requires approval for deployments)')).toBeVisible();

    const strategySelect = page.getByLabel('Deploy Strategy *');
    await strategySelect.selectOption('rolling');
    await expect(strategySelect).toHaveValue('rolling');
    await strategySelect.selectOption('blue-green');
    await expect(strategySelect).toHaveValue('blue-green');
    await strategySelect.selectOption('canary');
    await expect(strategySelect).toHaveValue('canary');
  });

  test('should create environment through ContextVM and canonical 30900 projection', async ({ page }) => {
    await page.goto('/environments');

    await page.getByRole('button', { name: 'Create Environment' }).click();
    const dialog = page.getByRole('dialog', { name: 'Create Environment' });
    await expect(dialog).toBeVisible();

    await page.getByLabel('Organization *').selectOption(ORG_ID);
    await page.getByLabel('Name *').fill('development');
    await page.getByLabel('Loom Worker Selector').fill('role=dev');
    await page.getByLabel('Runtime Config (JSON)').fill('{"cpu_limit":"1","memory_limit":"1Gi"}');
    await page.getByLabel('Deploy Strategy *').selectOption('blue-green');
    await page.getByLabel('Protected (requires approval for deployments)').check();
    await dialog.getByRole('button', { name: 'Create' }).click();

    await expect(dialog).not.toBeVisible();
    await expect(page.getByRole('cell', { name: 'development', exact: true })).toBeVisible();

    const request = await expectContextVMOperation(page, 'environment/create');
    expect(request.tags).toEqual(expect.arrayContaining([
      ['p', SERVICE_PUBKEY],
      ['encrypted', 'contextvm-jsonrpc-v1'],
      ['method', 'environment/create']
    ]));
  });

  test('should navigate to environment detail page', async ({ page }) => {
    await page.goto('/environments');

    await expect(page.getByRole('row', { name: /production/ })).toBeVisible();
    await page.goto('/environments/env-1');

    expect(page.url()).toMatch(/\/environments\/env-1/);
    await expect(page.getByRole('heading', { name: 'production' })).toBeVisible();
  });

  test('should show environment config, state/drift, deployment history, and edit action on detail page', async ({ page }) => {
    await page.goto('/environments/env-1');

    await expect(page.locator('text=production')).toBeVisible();
    await expect(page.locator('h2:has-text("Runtime Configuration")')).toBeVisible();
    await expect(page.locator('h2:has-text("Deployed Services (2)")')).toBeVisible();
    await expect(page.locator('h2:has-text("Deployment History (2)")')).toBeVisible();
    await expect(page.locator('text=Current State')).toBeVisible();
    await expect(page.locator('text=Drifted Services')).toBeVisible();
    await expect(page.locator('text=In-Sync Services')).toBeVisible();
    await expect(page.locator('button:has-text("Edit"), a:has-text("Edit")').first()).toBeVisible();
  });

  test('should create a max-like Compose target entirely in the environment UI', async ({ page }) => {
    await page.goto('/environments');
    await page.getByRole('button', { name: 'Create Environment' }).click();
    const dialog = page.getByRole('dialog', { name: 'Create Environment' });

    await page.getByLabel('Organization *').selectOption(ORG_ID);
    await page.getByLabel('Name *').fill('max-production');
    await page.getByLabel('Create an explicit Bahia-managed Compose deployment unit').check();
    await page.getByLabel('Unit key *').fill('max');
    await page.getByLabel('Display name').fill('Max Compose');
    await page.getByLabel('Endpoint alias *').fill('max');
    await page.getByLabel('Compose directory *').fill('/srv/bahia/compose/gastown');
    await page.getByLabel('Reconcile mode *').selectOption('approval_required');
    await page.getByLabel('Compose execution *').selectOption('sdk');
    await dialog.getByRole('button', { name: 'Create' }).click();

    await expect(dialog).not.toBeVisible();
    await expect(page.getByRole('cell', { name: 'max-production', exact: true })).toBeVisible();
    const request = await expectContextVMOperation(page, 'environment/create');
    expect(request.payload).toMatchObject({
      org_id: ORG_ID,
      targeting: {
        default_unit_key: 'max',
        secret_scope_mode: 'unit',
        default_reconcile_mode: 'approval_required'
      },
      deployment_units: [{
        key: 'max',
        runtime_type: 'compose',
        endpoint_ref: 'max',
        compose_dir: '/srv/bahia/compose/gastown',
        ownership_mode: 'bahia_managed',
        reconcile_mode: 'approval_required',
        runtime_config: { execution_mode: 'sdk' }
      }]
    });
  });

  test('should list and edit a protected Compose unit through a confirmed full-set update', async ({ page }) => {
    await page.goto('/environments/env-1');

    await expect(page.getByRole('heading', { name: 'Deployment Units (1)' })).toBeVisible();
    await expect(page.getByText('Max Compose').first()).toBeVisible();
    await expect(page.getByText('max', { exact: true }).first()).toBeVisible();
    await expect(page.getByText('/srv/bahia/compose/gastown')).toBeVisible();

    await page.getByRole('button', { name: 'Edit Unit' }).click();
    const editor = page.getByRole('dialog', { name: 'Edit Deployment Unit' });
    await editor.getByLabel('Endpoint alias *').fill('max-west');
    await editor.getByRole('button', { name: 'Review Protected Change' }).click();

    const confirm = page.getByRole('dialog', { name: 'Confirm Protected Target Change' });
    await expect(confirm.getByText('max-west', { exact: true })).toBeVisible();
    await confirm.getByRole('button', { name: 'Sign Target Update' }).click();

    await expect(editor).not.toBeVisible();
    const request = await expectContextVMOperation(page, 'environment/update');
    expect(request.payload.expected_updated_at).toBe('2026-05-03T10:02:30.000Z');
    expect(request.payload.deployment_units).toHaveLength(1);
    expect(request.payload.deployment_units[0]).toMatchObject({
      key: 'max',
      endpoint_ref: 'max-west',
      runtime_type: 'compose',
      ownership_mode: 'bahia_managed'
    });
  });

  test('should reject endpoint URLs before publishing a target update', async ({ page }) => {
    await page.goto('/environments/env-1');
    await page.getByRole('button', { name: 'Edit Unit' }).click();
    const editor = page.getByRole('dialog', { name: 'Edit Deployment Unit' });
    await editor.getByLabel('Endpoint alias *').fill('tcp://docker.example:2376');
    await editor.getByRole('button', { name: 'Review Protected Change' }).click();

    await expect(editor.getByRole('alert')).toContainText('URLs and credentials');
    const operations = await page.evaluate(() => window.__BAHIA_E2E_PUBLIC_REQUESTS.map((request) => request.operation));
    expect(operations).not.toContain('environment/update');
  });

  test('should update environment through ContextVM and canonical 30900 projection', async ({ page }) => {
    await page.goto('/environments/env-1');

    await page.getByRole('button', { name: 'Edit', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: 'Edit Environment' });
    await expect(dialog).toBeVisible();
    await page.getByLabel('Worker Selector').fill('role=prod-updated');
    await dialog.getByRole('button', { name: /Save|Update/ }).click();

    await expect(dialog).not.toBeVisible();
    const request = await expectContextVMOperation(page, 'environment/update');
    expect(request.tags).toEqual(expect.arrayContaining([
      ['environment', 'env-1'],
      ['p', SERVICE_PUBKEY],
      ['encrypted', 'contextvm-jsonrpc-v1'],
      ['method', 'environment/update']
    ]));
  });

  test('should show delete environment confirmation', async ({ page }) => {
    await page.goto('/environments/env-2');

    await page.getByRole('button', { name: 'Delete' }).click();

    await expect(page.getByRole('dialog', { name: 'Delete Environment' })).toBeVisible();
  });

  test('should delete environment through ContextVM and canonical tombstone projection', async ({ page }) => {
    await page.goto('/environments/env-2');

    await page.getByRole('button', { name: 'Delete' }).click();
    await page.getByRole('dialog', { name: 'Delete Environment' }).getByRole('button', { name: 'Delete' }).click();

    await expect(page).toHaveURL(/\/environments$/);
    const request = await expectContextVMOperation(page, 'environment/delete');
    expect(request.tags).toEqual(expect.arrayContaining([
      ['environment', 'env-2'],
      ['p', SERVICE_PUBKEY],
      ['encrypted', 'contextvm-jsonrpc-v1'],
      ['method', 'environment/delete']
    ]));
  });

  test('should cancel environment deletion', async ({ page }) => {
    await page.goto('/environments/env-2');

    await page.getByRole('button', { name: 'Delete' }).click();
    await page.getByRole('button', { name: 'Cancel' }).click();

    await expect(page.locator('text=Are you sure').first()).not.toBeVisible();
  });
});
