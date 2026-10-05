import { test, expect } from '@playwright/test';
import { createLLMState, installPublicLLMControlplaneHarness } from './harnesses/llm-controlplane-public.js';

const forbiddenRestCalls = [];

test.beforeEach(async ({ page }) => {
  forbiddenRestCalls.length = 0;
  await page.route('**/api/v1/llm/**', route => {
    forbiddenRestCalls.push(route.request().url());
    route.fulfill({ status: 500, body: 'unexpected llm rest call' });
  });
});

async function acceptedIntent(page, op) {
  const overlay = page.getByTestId('llm-pending-intents');
  await expect(overlay).toContainText('Pending');
  const operation = op === 'create' ? 'llm/route-create' : `llm/${op}`;
  await expect.poll(() => page.evaluate(name => window.__BAHIA_E2E_LLM_REQUESTS.some(item =>
    item.kind === 30900 && item.operation === name), operation)).toBe(true);
  const request = await page.evaluate(name => window.__BAHIA_E2E_LLM_REQUESTS.findLast(item =>
    item.kind === 30900 && item.operation === name), operation);
  expect(request).toBeTruthy();
  expect(request.tags).toEqual(expect.arrayContaining([['domain', 'llm'], ['op', op]]));
  await page.evaluate(() => window.__BAHIA_E2E_LLM_ACCEPT_INTENTS());
  await expect(overlay).toHaveCount(0);
  return request;
}

test('LLM route, release, and deploy publish signed intents and wait for acceptance', async ({ page }) => {
  await installPublicLLMControlplaneHarness(page, { initialState: createLLMState() });
  await page.goto('/llm');
  await expect(page.getByRole('heading', { name: 'LLM Control Plane' })).toBeVisible();

  await page.locator('input[name="route-name"]').fill('chat-prod');
  await page.locator('input[name="public-model"]').fill('bahia/chat');
  await page.locator('[data-testid="llm-create-route-form"]').getByRole('button', { name: 'Create route' }).click();
  await acceptedIntent(page, 'create');

  await page.locator('select[name="release-route"]').selectOption({ label: 'chat-prod' });
  await page.locator('input[name="release-version"]').fill('v1');
  await page.locator('input[name="model-ref"]').fill('hf://meta-llama/Llama-3');
  await page.locator('select[name="backend-mode"]').selectOption('external');
  await page.locator('input[name="external-base-url"]').fill('https://llm-v1.example.com');
  await page.locator('[data-testid="llm-register-release-form"]').getByRole('button', { name: 'Register release' }).click();
  await acceptedIntent(page, 'release-register');

  await page.locator('select[name="deploy-route"]').selectOption({ label: 'chat-prod' });
  await page.locator('select[name="deploy-environment"]').selectOption({ label: 'production' });
  await page.locator('select[name="deploy-release"]').selectOption({ label: 'v1 · chat-prod' });
  await page.locator('[data-testid="llm-request-deploy-form"]').getByRole('button', { name: 'Request deployment' }).click();
  const deploy = await acceptedIntent(page, 'deploy');
  expect(JSON.parse(deploy.content)).toMatchObject({ environment_id: 'env-prod', release_id: expect.any(String) });
  expect(forbiddenRestCalls).toEqual([]);
});
test('LLM rollback publishes a signed intent without inventing synchronous completion', async ({ page }) => {
  await installPublicLLMControlplaneHarness(page, { initialState: createLLMState({
    routes: [{ id: 'llm-route-1', route_id: 'llm-route-1', name: 'chat-prod',
      gateway_config: { public_model: 'bahia/chat', path: '/v1/models/chat-prod' },
      created_at: '2026-05-04T00:00:00.000Z' }],
    releases: [{ id: 'llm-release-1', route_id: 'llm-route-1', version: 'v1',
      model_ref: 'hf://meta-llama/Llama-3', created_at: '2026-05-04T00:05:00.000Z' }],
    routeStates: [{ route_id: 'llm-route-1', environment_id: 'env-prod',
      desired_release_id: 'llm-release-1', drift_status: 'in_sync', gateway_status: 'synced',
      updated_at: '2026-05-04T00:10:00.000Z' }]
  }) });
  await page.goto('/llm');
  await expect(page.getByTestId('llm-route-state-table')).toContainText('chat-prod');
  await page.getByTestId('llm-route-state-table').getByRole('button', { name: 'Rollback' }).first().click();
  const rollback = await acceptedIntent(page, 'rollback');
  expect(JSON.parse(rollback.content)).toMatchObject({ route_id: 'llm-route-1', environment_id: 'env-prod' });
  expect(JSON.parse(rollback.content)).not.toHaveProperty('release_id');
  expect(forbiddenRestCalls).toEqual([]);
});
