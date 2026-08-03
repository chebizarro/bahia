import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';
import { createPublicState, createPublicSystemInfo, installPublicServiceDeploymentHarness } from './harnesses/service-deployment-public.js';

const systemInfo = createPublicSystemInfo();
const initialState = createPublicState({
  services: [{
    id: 'svc-compose',
    name: 'arcana',
    org_id: '11111111-1111-4111-8111-111111111111',
    artifact_repo: 'ghcr.io/example/arcana',
    runtime_type: 'compose',
    default_branch: 'main',
    deleted: false,
    created_at: '2026-08-02T08:00:00.000Z'
  }],
  environments: [
    {
      id: 'env-multi',
      org_id: '11111111-1111-4111-8111-111111111111',
      name: 'production',
      protected: true,
      targeting: { default_unit_key: 'max', secret_scope_mode: 'unit', default_reconcile_mode: 'approval_required' },
      deployment_units: [
        {
          id: 'unit-max',
          environment_id: 'env-multi',
          key: 'max',
          display_name: 'Max Compose',
          runtime_type: 'compose',
          endpoint_ref: 'max',
          compose_dir: '/srv/bahia/compose/max',
          ownership_mode: 'bahia_managed',
          reconcile_mode: 'approval_required',
          runtime_config: { execution_mode: 'sdk' },
          implicit: false
        },
        {
          id: 'unit-east',
          environment_id: 'env-multi',
          key: 'east',
          display_name: 'East Compose',
          runtime_type: 'compose',
          endpoint_ref: 'east',
          compose_dir: '/srv/bahia/compose/east',
          ownership_mode: 'bahia_managed',
          reconcile_mode: 'observe_only',
          runtime_config: { execution_mode: 'sdk' },
          implicit: false
        }
      ],
      updated_at: '2026-08-02T08:01:00.000Z',
      deleted: false
    },
    {
      id: 'env-invalid',
      org_id: '11111111-1111-4111-8111-111111111111',
      name: 'invalid-ownership',
      protected: false,
      targeting: { default_unit_key: 'adopted', secret_scope_mode: 'unit', default_reconcile_mode: 'observe_only' },
      deployment_units: [{
        id: 'unit-adopted',
        environment_id: 'env-invalid',
        key: 'adopted',
        runtime_type: 'compose',
        endpoint_ref: 'external',
        compose_dir: '/srv/external',
        ownership_mode: 'adopted',
        reconcile_mode: 'observe_only',
        implicit: false
      }],
      updated_at: '2026-08-02T08:02:00.000Z',
      deleted: false
    }
  ],
  builds: [],
  artifacts: [{
    id: 'artifact-arcana',
    service_id: 'svc-compose',
    image_repo: 'ghcr.io/example/arcana',
    image_digest: 'sha256:abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd',
    created_at: '2026-08-02T08:03:00.000Z'
  }]
});

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page, { systemInfo });
  await installPublicServiceDeploymentHarness(page, { initialState });
});

test.describe('Explicit deployment-unit targeting', () => {
  test('requires a unit for multi-unit environments and sends only its ID and endpoint alias', async ({ page }) => {
    await page.goto('/services/svc-compose');
    await page.getByRole('button', { name: 'Deploy' }).click();
    const dialog = page.getByRole('dialog', { name: 'Create Deployment Intent' });

    await dialog.getByLabel('Environment *').selectOption('env-multi');
    await expect(dialog.getByLabel('Deployment unit *')).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Create Intent' })).toBeDisabled();
    await expect(dialog.getByRole('alert')).toContainText('Select an explicit deployment unit');

    await dialog.getByLabel('Deployment unit *').selectOption('unit-max');
    await expect(dialog.getByText('Resolved endpoint alias:')).toBeVisible();
    await expect(dialog.getByText('max', { exact: true }).first()).toBeVisible();
    await expect(dialog).not.toContainText('tcp://');
    await expect(dialog).not.toContainText('DOCKER_HOST');
    await expect(dialog).not.toContainText('CLIENT_KEY');

    await dialog.getByLabel('Artifact from recent builds *').selectOption('artifact-arcana');
    await expect(dialog.getByRole('button', { name: 'Create Intent' })).toBeEnabled();
    await dialog.getByRole('button', { name: 'Create Intent' }).click();

    await expect(page).toHaveURL(/\/deployments$/);
    const trace = await page.evaluate(() => ({
      requests: window.__BAHIA_E2E_PUBLIC_REQUESTS,
      intent: window.__BAHIA_E2E_PUBLIC_STATE.deploymentIntents[0]
    }));
    const preview = trace.requests.find((request) => request.operation === 'policy/evaluate');
    const deploy = trace.requests.find((request) => request.operation === 'service/deploy');
    expect(preview.payload).toMatchObject({
      service_id: 'svc-compose',
      environment_id: 'env-multi',
      deployment_unit_id: 'unit-max',
      artifact_id: 'artifact-arcana'
    });
    expect(deploy.payload).toMatchObject({
      service_id: 'svc-compose',
      environment_id: 'env-multi',
      deployment_unit_id: 'unit-max',
      artifact_id: 'artifact-arcana'
    });
    expect(deploy.tags).toEqual(expect.arrayContaining([['unit', 'unit-max']]));
    expect(trace.intent.deployment_unit_id).toBe('unit-max');
    expect(JSON.stringify(deploy)).not.toContain('tcp://');
  });

  test('blocks a Compose target that is missing Bahia-managed ownership', async ({ page }) => {
    await page.goto('/services/svc-compose');
    await page.getByRole('button', { name: 'Deploy' }).click();
    const dialog = page.getByRole('dialog', { name: 'Create Deployment Intent' });

    await dialog.getByLabel('Environment *').selectOption('env-invalid');
    await expect(dialog.getByRole('alert')).toContainText('Bahia-managed ownership');
    await dialog.getByLabel('Artifact from recent builds *').selectOption('artifact-arcana');
    await expect(dialog.getByRole('button', { name: 'Create Intent' })).toBeDisabled();

    const operations = await page.evaluate(() => window.__BAHIA_E2E_PUBLIC_REQUESTS.map((request) => request.operation));
    expect(operations).not.toContain('policy/evaluate');
    expect(operations).not.toContain('service/deploy');
  });
});
