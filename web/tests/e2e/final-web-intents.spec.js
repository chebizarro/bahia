import { test, expect } from '@playwright/test';
import { E2E_SERVICE_PUBKEY, installE2EMocks } from './helpers.js';
import { cpStateFixture } from './cp-state-fixtures.js';
import { BAHIA_STATE_SCHEMAS } from '../../src/lib/nostr/kinds.gen.js';

const orgId = '0199c749-9300-7444-8444-444444444444';
const systemInfo = { organization_id: orgId,
  nostr: { browser_relays: ['ws://relay.test.local'], service_pubkey: E2E_SERVICE_PUBKEY },
  features: { relay_sidecar: true, relay_read_models: true, encrypted_nostr_requests: true, legacy_sse: false } };

async function signedIntent(page, domain, op) {
  await expect.poll(() => page.evaluate(({ domain, op }) => window.__BAHIA_E2E_SIGNED_INTENTS
    .some(event => event.tags.some(tag => tag[0] === 'domain' && tag[1] === domain)
      && event.tags.some(tag => tag[0] === 'op' && tag[1] === op)), { domain, op })).toBe(true);
  return page.evaluate(({ domain, op }) => window.__BAHIA_E2E_SIGNED_INTENTS.find(event =>
    event.tags.some(tag => tag[0] === 'domain' && tag[1] === domain)
      && event.tags.some(tag => tag[0] === 'op' && tag[1] === op)), { domain, op });
}

test('adoption scan renders bounded findings only from accepted status data', async ({ page }) => {
  await installE2EMocks(page, { systemInfo });
  await page.goto('/adoption');
  await page.evaluate(() => { window.__BAHIA_E2E_INTENT_STATUS_DATA = { 'adoption.scan': {
    findings: [{ target_name: 'production', container_id: 'container-1', image_ref: 'registry.example/app:v1',
      proposed_service_name: 'app', adoptable: true, warnings_count: 0 }],
    next_offset: 1, total_findings: 1, truncated: false
  } }; });
  const form = page.getByTestId('adoption-scan-form');
  await form.getByLabel('Organization ID').fill(orgId);
  await form.getByLabel('Target name').fill('production');
  await form.getByLabel('Runtime endpoint reference').fill('runtime:production');
  await form.getByRole('button', { name: 'Scan target' }).click();
  const intent = await signedIntent(page, 'adoption', 'scan');
  expect(intent.kind).toBe(30900);
  expect(JSON.parse(intent.content)).toMatchObject({ targets: [{ name: 'production', endpoint_ref: 'runtime:production' }],
    limit: 20, offset: 0 });
  await expect(page.getByRole('row', { name: /production container-1 registry.example\/app:v1 app Yes/ })).toBeVisible();
  await expect(page.getByRole('status')).toContainText('Showing 1 of 1 findings');
});

test('manual security scan sends the complete target and uses accepted status data', async ({ page }) => {
  await installE2EMocks(page, { systemInfo });
  await page.goto('/security');
  const runId = '00000000-0000-4000-8000-000000000008';
  await page.evaluate(runId => { window.__BAHIA_E2E_INTENT_STATUS_DATA = {
    'security.scan-run': { run_id: runId, target_key_hash: 'target-hash', accepted: true }
  }; }, runId);
  const form = page.getByTestId('security-scan-form');
  const target = { type: 'package', package: { ecosystem: 'npm', name: 'left-pad', version: '1.3.0' } };
  await form.getByLabel('Scan target JSON').fill(JSON.stringify(target));
  await form.getByRole('button', { name: 'Run signed scan' }).click();
  const intent = await signedIntent(page, 'security', 'scan-run');
  expect(intent.kind).toBe(30900);
  expect(intent.tags.find(tag => tag[0] === 'd')?.[1]).toMatch(/^security-scan:/);
  expect(JSON.parse(intent.content)).toMatchObject({ target });
  await expect(page.getByRole('status')).toContainText(`Scan accepted (run ${runId})`);
});

test('environment placement publishes a revision-guarded worker-policy intent', async ({ page }) => {
  const environmentId = '00000000-0000-4000-8000-000000000009';
  const updatedAt = '2026-10-03T00:00:00Z';
  await installE2EMocks(page, { systemInfo, nostrEvents: [cpStateFixture({
    schema: BAHIA_STATE_SCHEMAS.ENVIRONMENT_REGISTRY, d: `environment:${environmentId}`,
    content: { id: environmentId, org_id: orgId, name: 'production', runtime_config: {}, updated_at: updatedAt }
  })] });
  await page.goto(`/environments/${environmentId}`);
  await page.getByRole('button', { name: 'Edit placement policy' }).click();
  const dialog = page.getByRole('dialog', { name: 'Edit Worker Placement Policy' });
  await dialog.getByLabel('Label selector').fill('role=inference');
  await dialog.getByRole('button', { name: 'Publish signed policy intent' }).click();
  const intent = await signedIntent(page, 'environment', 'worker-policy-apply');
  expect(intent.kind).toBe(30900);
  expect(intent.tags).toContainEqual(['d', environmentId]);
  expect(JSON.parse(intent.content)).toMatchObject({ environment_id: environmentId,
    expected_updated_at: updatedAt, policy: { label_selector: { role: 'inference' } } });
  await expect(page.getByText('Signed worker placement policy pending canonical confirmation')).toBeVisible();
});

test('ML recipe, run, approval and rollback publish signed intents', async ({ page }) => {
  const endpointId = '00000000-0000-4000-8000-000000000005';
  await installE2EMocks(page, { systemInfo, nostrEvents: [cpStateFixture({
    schema: BAHIA_STATE_SCHEMAS.ML_INFERENCE_ENDPOINT_REGISTRY, d: `endpoint:${endpointId}`,
    content: { id: endpointId, name: 'sample-endpoint', environment_id: orgId, updated_at: '2026-10-03T00:00:00Z' }
  })] });
  await page.goto('/ml');
  await page.getByRole('heading', { name: 'Apply ML recipe' }).scrollIntoViewIfNeeded();
  const apply = page.locator('form', { has: page.getByRole('button', { name: 'Apply recipe' }) });
  await apply.getByLabel('Name').fill('sample');
  await apply.getByLabel('Version').fill('v1');
  await apply.getByLabel('Recipe YAML').fill('name: sample');
  await apply.getByRole('button', { name: 'Apply recipe' }).click();
  expect(JSON.parse((await signedIntent(page, 'ml', 'recipe-apply')).content)).toMatchObject({ name: 'sample', version: 'v1' });

  const run = page.locator('form', { has: page.getByRole('button', { name: 'Run recipe' }) });
  const recipeId = '00000000-0000-4000-8000-000000000006';
  await run.getByLabel('Canonical recipe ID').fill(recipeId);
  await run.getByLabel('Inputs JSON').fill('{"batch":2}');
  await run.getByRole('button', { name: 'Run recipe' }).click();
  expect(JSON.parse((await signedIntent(page, 'ml', 'recipe-run')).content)).toMatchObject({ recipe_id: recipeId, inputs: { batch: 2 } });

  const approval = page.locator('form', { has: page.getByRole('button', { name: 'Submit decision' }) });
  const deploymentIntentId = '00000000-0000-4000-8000-000000000007';
  await approval.getByLabel('Deployment intent ID').fill(deploymentIntentId);
  await approval.getByRole('button', { name: 'Submit decision' }).click();
  expect(JSON.parse((await signedIntent(page, 'ml', 'inference-approval')).content)).toMatchObject({ intent_id: deploymentIntentId, decision: 'approve' });

  const rollback = page.locator('form', { has: page.getByRole('button', { name: 'Request rollback' }) });
  await rollback.getByLabel('Endpoint').selectOption(endpointId);
  await rollback.getByRole('button', { name: 'Request rollback' }).click();
  expect(JSON.parse((await signedIntent(page, 'ml', 'inference-rollback')).content)).toMatchObject({ endpoint_id: endpointId });
});
