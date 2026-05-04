import { test, expect } from '@playwright/test';
import { createLLMState, installPublicLLMControlplaneHarness } from './harnesses/llm-controlplane-public.js';

const forbiddenRestCalls = [];

test.beforeEach(async ({ page }) => {
  forbiddenRestCalls.length = 0;
  await page.route('**/api/v1/llm/**', (route) => {
    forbiddenRestCalls.push(route.request().url());
    route.fulfill({ status: 500, body: 'unexpected llm rest call' });
  });

  await installPublicLLMControlplaneHarness(page, {
    initialState: createLLMState()
  });
});

test('dedicated LLM browser workflow uses signer-first requests and relay-backed state', async ({ page }) => {
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
  await page.locator('input[name="external-base-url"]').fill('https://llm.example.com');
  await page.locator('[data-testid="llm-register-release-form"]').getByRole('button', { name: 'Register release' }).click();
  await expect(page.getByTestId('llm-notice')).toContainText('Registered release v1');

  await page.locator('select[name="deploy-route"]').selectOption({ label: 'chat-prod' });
  await page.locator('select[name="deploy-environment"]').selectOption({ label: 'production' });
  const releaseValue = await page.locator('select[name="deploy-release"] option').nth(1).getAttribute('value');
  await page.locator('select[name="deploy-release"]').selectOption(releaseValue);
  await page.locator('[data-testid="llm-request-deploy-form"]').getByRole('button', { name: 'Request deployment' }).click();
  await expect(page.getByTestId('llm-notice')).toContainText('accepted');

  await expect(page.getByTestId('llm-pending-approvals')).toContainText('chat-prod');
  const approveButton = page.getByRole('button', { name: 'Approve' }).first();
  await approveButton.click();
  await expect(page.getByTestId('llm-notice')).toContainText('approval decision recorded');

  await expect(page.getByTestId('llm-route-state-table')).toContainText('synced');
  await expect(page.getByTestId('llm-route-state-table')).toContainText('in_sync');
  await expect(page.getByTestId('llm-activity-table')).toContainText('completed');

  const requestKinds = await page.evaluate(() => JSON.parse(localStorage.getItem('__BAHIA_E2E_LLM_REQUEST_KINDS') || '[]'));
  expect(requestKinds).toEqual([5971, 5972, 5973, 5974]);

  const requests = await page.evaluate(() => JSON.parse(localStorage.getItem('__BAHIA_E2E_LLM_REQUESTS') || '[]'));
  expect(requests).toHaveLength(4);
  expect(requests[0].kind).toBe(5971);
  expect(requests[1].kind).toBe(5972);
  expect(requests[1].tags).toEqual(expect.arrayContaining([['route', expect.any(String)]]));
  expect(requests[2].kind).toBe(5973);
  expect(requests[2].tags).toEqual(expect.arrayContaining([
    ['route', expect.any(String)],
    ['environment', 'env-prod'],
    ['release', expect.any(String)]
  ]));
  expect(requests[3].kind).toBe(5974);
  expect(requests[3].tags).toEqual(expect.arrayContaining([
    ['intent', expect.any(String)],
    ['decision', 'approve']
  ]));

  const state = await page.evaluate(() => JSON.parse(localStorage.getItem('__BAHIA_E2E_LLM_STATE') || '{}'));
  const completionEvent = state.activity.find((event) => event.kind === 7973 && event.content?.status === 'completed');
  expect(completionEvent.tags).toEqual(expect.arrayContaining([
    ['e', requests[2].eventId],
    ['intent', expect.any(String)]
  ]));

  expect(forbiddenRestCalls).toEqual([]);
});
