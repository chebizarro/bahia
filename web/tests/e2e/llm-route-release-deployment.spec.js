import { test, expect } from '@playwright/test';
import { createLLMState, installPublicLLMControlplaneHarness } from './harnesses/llm-controlplane-public.js';

const forbiddenRestCalls = [];

test.beforeEach(async ({ page }) => {
  forbiddenRestCalls.length = 0;
  await page.route('**/api/v1/llm/**', (route) => {
    forbiddenRestCalls.push(route.request().url());
    route.fulfill({ status: 500, body: 'unexpected llm rest call' });
  });
});

test('dedicated LLM browser workflow uses signer-first requests for route, release, deploy, approval, and rollback with relay-backed state', async ({ page }) => {
  await installPublicLLMControlplaneHarness(page, {
    initialState: createLLMState()
  });

  await page.goto('/llm');
  await expect(page.getByRole('heading', { name: 'LLM Control Plane' })).toBeVisible();

  await page.locator('input[name="route-name"]').fill('chat-prod');
  await page.locator('input[name="public-model"]').fill('bahia/chat');
  await page.locator('textarea[name="route-description"]').fill('Public chat completions route');
  await page.locator('[data-testid="llm-create-route-form"]').getByRole('button', { name: 'Create route' }).click();
  await expect(page.getByTestId('llm-notice')).toContainText('Created LLM route');

  await page.locator('select[name="release-route"]').selectOption({ label: 'chat-prod' });
  await page.locator('input[name="release-version"]').fill('v1');
  await page.locator('input[name="model-ref"]').fill('hf://meta-llama/Llama-3');
  await page.locator('select[name="backend-mode"]').selectOption('external');
  await page.locator('input[name="external-base-url"]').fill('https://llm-v1.example.com');
  await page.locator('[data-testid="llm-register-release-form"]').getByRole('button', { name: 'Register release' }).click();
  await expect(page.getByTestId('llm-notice')).toContainText('Registered release v1');

  await page.locator('select[name="deploy-route"]').selectOption({ label: 'chat-prod' });
  await page.locator('select[name="deploy-environment"]').selectOption({ label: 'production' });
  await page.locator('select[name="deploy-release"]').selectOption({ label: 'v1 · chat-prod' });
  await page.locator('[data-testid="llm-request-deploy-form"]').getByRole('button', { name: 'Request deployment' }).click();
  await expect(page.getByTestId('llm-notice')).toContainText('accepted');

  await expect(page.getByTestId('llm-pending-approvals')).toContainText('chat-prod');
  await page.getByRole('button', { name: 'Approve' }).first().click();
  await expect(page.getByTestId('llm-notice')).toContainText('approval decision recorded');
  await expect(page.getByTestId('llm-route-state-table')).toContainText('v1');

  await page.locator('select[name="release-route"]').selectOption({ label: 'chat-prod' });
  await page.locator('input[name="release-version"]').fill('v2');
  await page.locator('input[name="model-ref"]').fill('hf://meta-llama/Llama-3.1');
  await page.locator('select[name="backend-mode"]').selectOption('external');
  await page.locator('input[name="external-base-url"]').fill('https://llm-v2.example.com');
  await page.locator('[data-testid="llm-register-release-form"]').getByRole('button', { name: 'Register release' }).click();
  await expect(page.getByTestId('llm-notice')).toContainText('Registered release v2');

  await page.locator('select[name="deploy-route"]').selectOption({ label: 'chat-prod' });
  await page.locator('select[name="deploy-environment"]').selectOption({ label: 'production' });
  await page.locator('select[name="deploy-release"]').selectOption({ label: 'v2 · chat-prod' });
  await page.locator('[data-testid="llm-request-deploy-form"]').getByRole('button', { name: 'Request deployment' }).click();
  await expect(page.getByTestId('llm-notice')).toContainText('accepted');

  await expect(page.getByTestId('llm-pending-approvals')).toContainText('v2');
  await page.getByRole('button', { name: 'Approve' }).first().click();
  await expect(page.getByTestId('llm-notice')).toContainText('approval decision recorded');
  await expect(page.getByTestId('llm-route-state-table')).toContainText('v2');

  await page.getByTestId('llm-route-state-table').getByRole('button', { name: 'Rollback' }).first().click();
  await expect(page.getByTestId('llm-notice')).toContainText('rollback');
  await expect(page.getByTestId('llm-activity-table')).toContainText('LLM rollback completed');

  const requestKinds = await page.evaluate(() => JSON.parse(localStorage.getItem('__BAHIA_E2E_LLM_REQUEST_KINDS') || '[]'));
  expect(requestKinds).toEqual([25910, 25910, 25910, 25910, 25910, 25910, 25910, 25910]);

  const requests = await page.evaluate(() => JSON.parse(localStorage.getItem('__BAHIA_E2E_LLM_REQUESTS') || '[]'));
  expect(requests).toHaveLength(8);
  expect(requests[7].kind).toBe(25910);
  expect(requests[7].operation).toBe('llm/rollback');
  expect(requests[7].tags).toEqual(expect.arrayContaining([
    ['route', expect.any(String)],
    ['environment', 'env-prod']
  ]));
  expect(requests[7].tags).not.toEqual(expect.arrayContaining([['release', expect.any(String)]]));

  const state = await page.evaluate(() => JSON.parse(localStorage.getItem('__BAHIA_E2E_LLM_STATE') || '{}'));
  const routeState = state.routeStates.find((entry) => entry.environment_id === 'env-prod');
  const v1Release = state.releases.find((entry) => entry.version === 'v1');
  const v2Release = state.releases.find((entry) => entry.version === 'v2');
  expect(routeState.desired_release_id).toBe(v1Release.id);
  expect(routeState.desired_release_id).not.toBe(v2Release.id);
  expect(routeState.drift_status).toBe('in_sync');
  expect(routeState.gateway_status).toBe('synced');

  const rollbackAcceptedEvent = state.activity.find((event) => event.kind === 30315 && event.content?.message === 'LLM rollback intent accepted');
  expect(rollbackAcceptedEvent.tags).toEqual(expect.arrayContaining([
    ['e', requests[7].eventId],
    ['release', v1Release.id],
    ['step', 'accepted']
  ]));

  const rollbackCompletionEvent = state.activity.find((event) => event.kind === 30315 && event.content?.message === 'LLM rollback completed');
  expect(rollbackCompletionEvent.tags).toEqual(expect.arrayContaining([
    ['e', requests[7].eventId],
    ['release', v1Release.id]
  ]));

  expect(forbiddenRestCalls).toEqual([]);
});

test('dedicated LLM browser rollback surfaces backend failure when no previous deployed different release exists', async ({ page }) => {
  await installPublicLLMControlplaneHarness(page, {
    initialState: createLLMState({
      routes: [
        {
          id: 'llm-route-1',
          route_id: 'llm-route-1',
          name: 'chat-prod',
          description: 'Public chat completions route',
          gateway_config: { public_model: 'bahia/chat', path: '/v1/models/chat-prod' },
          created_at: '2026-05-04T00:00:00.000Z'
        }
      ],
      releases: [
        {
          id: 'llm-release-1',
          route_id: 'llm-route-1',
          version: 'v1',
          model_ref: 'hf://meta-llama/Llama-3',
          model_source: 'huggingface',
          external_backend: { base_url: 'https://llm-v1.example.com' },
          created_at: '2026-05-04T00:05:00.000Z'
        }
      ],
      routeStates: [
        {
          route_id: 'llm-route-1',
          environment_id: 'env-prod',
          desired_release_id: 'llm-release-1',
          desired_intent_id: 'llm-intent-1',
          active_run_id: 'llm-run-1',
          drift_status: 'in_sync',
          gateway_status: 'synced',
          backend_health: 'healthy',
          backend_endpoint: 'https://llm-v1.example.com',
          updated_at: '2026-05-04T00:10:00.000Z'
        }
      ],
      deploymentHistory: [
        {
          route_id: 'llm-route-1',
          environment_id: 'env-prod',
          release_id: 'llm-release-1',
          intent_id: 'llm-intent-1',
          deployed_at: '2026-05-04T00:10:00.000Z',
          source_kind: 'deploy'
        }
      ],
      activity: [
        {
          id: 'seed-release',
          kind: 30315,
          tags: [['route', 'llm-route-1'], ['release', 'llm-release-1'], ['status', 'success']],
          content: { route_id: 'llm-route-1', release_id: 'llm-release-1', version: 'v1', status: 'success' },
          created_at: Math.floor(Date.parse('2026-05-04T00:05:00.000Z') / 1000)
        },
        {
          id: 'seed-complete',
          kind: 30315,
          tags: [['route', 'llm-route-1'], ['environment', 'env-prod'], ['release', 'llm-release-1'], ['intent', 'llm-intent-1'], ['status', 'completed']],
          content: {
            route_id: 'llm-route-1',
            environment_id: 'env-prod',
            release_id: 'llm-release-1',
            intent_id: 'llm-intent-1',
            status: 'completed',
            message: 'LLM deployment completed'
          },
          created_at: Math.floor(Date.parse('2026-05-04T00:10:00.000Z') / 1000)
        }
      ]
    })
  });

  await page.goto('/llm');
  await expect(page.getByTestId('llm-route-state-table')).toContainText('chat-prod');

  await page.getByTestId('llm-route-state-table').getByRole('button', { name: 'Rollback' }).first().click();
  await expect(page.getByTestId('llm-notice')).toContainText('no previous successfully deployed LLM release to roll back to');

  const requestKinds = await page.evaluate(() => JSON.parse(localStorage.getItem('__BAHIA_E2E_LLM_REQUEST_KINDS') || '[]'));
  expect(requestKinds).toEqual([25910]);

  const requests = await page.evaluate(() => JSON.parse(localStorage.getItem('__BAHIA_E2E_LLM_REQUESTS') || '[]'));
  expect(requests).toHaveLength(1);
  expect(requests[0].kind).toBe(25910);
  expect(requests[0].operation).toBe('llm/rollback');
  expect(requests[0].tags).toEqual(expect.arrayContaining([
    ['route', 'llm-route-1'],
    ['environment', 'env-prod']
  ]));

  const state = await page.evaluate(() => JSON.parse(localStorage.getItem('__BAHIA_E2E_LLM_STATE') || '{}'));
  const routeState = state.routeStates.find((entry) => entry.route_id === 'llm-route-1' && entry.environment_id === 'env-prod');
  expect(routeState.desired_release_id).toBe('llm-release-1');
  expect(routeState.drift_status).toBe('in_sync');

  expect(forbiddenRestCalls).toEqual([]);
});
