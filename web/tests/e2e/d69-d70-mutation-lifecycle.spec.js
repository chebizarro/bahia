import { test, expect } from '@playwright/test';
import { E2E_SERVICE_PUBKEY, e2eTestPubkey, installE2EMocks } from './helpers.js';
import { cpStateFixture, cpAuditFixture, workerStateFixture } from './cp-state-fixtures.js';
import { BAHIA_STATE_SCHEMAS } from '../../src/lib/nostr/kinds.gen.js';
import { createPublicState, createPublicSystemInfo, installPublicServiceDeploymentHarness } from './harnesses/service-deployment-public.js';

const id = n => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`;
const systemInfo = { organization_id: id(1), nostr: { browser_relays: ['ws://relay.test.local'], service_pubkey: E2E_SERVICE_PUBKEY },
  features: { relay_sidecar: true, relay_read_models: true, encrypted_nostr_requests: true, legacy_sse: false } };
const eventTag = (event, name) => event.tags.find(tag => tag[0] === name)?.[1];

async function acceptedAfterPending(page, domain, op) {
  const overlay = page.getByTestId(`${domain}-pending-intents`);
  await expect(overlay).toContainText('Pending');
  await expect.poll(() => page.evaluate(({ domain, op }) =>
    window.__BAHIA_E2E_SIGNED_INTENTS.some(event => event.tags.some(tag => tag[0] === 'domain' && tag[1] === domain)
      && event.tags.some(tag => tag[0] === 'op' && tag[1] === op)), { domain, op })).toBe(true);
  const intent = await page.evaluate(({ domain, op }) => window.__BAHIA_E2E_SIGNED_INTENTS.find(event =>
    event.tags.some(tag => tag[0] === 'domain' && tag[1] === domain)
      && event.tags.some(tag => tag[0] === 'op' && tag[1] === op)), { domain, op });
  expect(intent.kind).toBe(30900);
  expect(eventTag(intent, 'schema')).toBe(`bahia.intent.${domain}.v1`);
  await page.evaluate(intent => window.__bahiaPushNostrEvent(window.__BAHIA_E2E_MAKE_INTENT_STATUS(intent,
    { id: `accepted-${intent.id}` })), intent);
  await expect(overlay).toHaveCount(0);
  return intent;
}

test('services runtime action stays pending until scoped acceptance', async ({ page }) => {
  await installE2EMocks(page, { systemInfo: createPublicSystemInfo() });
  await installPublicServiceDeploymentHarness(page, { initialState: createPublicState() });
  await page.goto('/services/svc-existing-1');
  await page.evaluate(() => { window.__BAHIA_E2E_INTENT_AUTO_STATUS = false; });
  await page.getByRole('region', { name: 'Runtime actions' }).getByLabel('Environment').selectOption('env-prod');
  await page.getByRole('button', { name: 'Restart runtime' }).click();
  const intent = await acceptedAfterPending(page, 'runtime', 'restart');
  expect(JSON.parse(intent.content)).toMatchObject({ service_id: 'svc-existing-1', environment_id: 'env-prod' });
});

test('deployment approval stays pending until scoped acceptance', async ({ page }) => {
  const initialState = createPublicState({ deploymentIntents: [{ id: id(2), service_id: 'svc-existing-1',
    environment_id: 'env-prod', artifact_id: 'artifact-existing-1', approval_status: 'pending', org_id: id(1),
    updated_at: '2026-10-03T00:00:00Z', created_at: '2026-10-03T00:00:00Z' }] });
  await installE2EMocks(page, { systemInfo: createPublicSystemInfo() });
  await installPublicServiceDeploymentHarness(page, { initialState });
  await page.goto('/deployments/pending');
  await page.evaluate(() => { window.__BAHIA_E2E_INTENT_AUTO_STATUS = false; });
  await page.locator(`.btn-approve[data-id="${id(2)}"]`).click();
  await page.getByRole('button', { name: 'Approve', exact: true }).last().click();
  const intent = await acceptedAfterPending(page, 'deployment', 'approve');
  expect(JSON.parse(intent.content)).toMatchObject({ deployment_intent_id: id(2), expected_updated_at: '2026-10-03T00:00:00Z' });
});

test('DNS zone create stays pending until scoped acceptance', async ({ page }) => {
  await installE2EMocks(page, { systemInfo });
  await page.goto('/dns');
  const form = page.locator('.command-card').first();
  await form.getByLabel('Zone').fill('example.test');
  await form.getByLabel('Backend').fill('primary');
  await form.getByLabel('TTL').fill('60');
  await form.getByRole('button', { name: 'Submit zone request' }).click();
  const intent = await acceptedAfterPending(page, 'dns', 'zone-create');
  expect(eventTag(intent, 'd')).toBe('zone:example.test');
  expect(JSON.parse(intent.content)).toMatchObject({ name: 'example.test', backend_ref: 'primary', ttl: 60 });
});

test('ML model create stays pending until scoped acceptance', async ({ page }) => {
  await installE2EMocks(page, { systemInfo });
  await page.goto('/ml');
  const form = page.getByTestId('ml-registry-mutations').locator('form').first();
  await form.getByLabel('Slug').fill('sample');
  await form.getByLabel('Name').fill('Sample');
  await form.getByRole('button', { name: 'Create model' }).click();
  const intent = await acceptedAfterPending(page, 'ml', 'model-create');
  expect(eventTag(intent, 'd')).toBe('model:sample');
});

test('worker cordon stays pending until scoped acceptance', async ({ page }) => {
  const pubkey = e2eTestPubkey('d70-worker-lifecycle');
  await installE2EMocks(page, { systemInfo, nostrEvents: [workerStateFixture({ pubkey, name: 'D70 Worker',
    status: 'online', scheduling_state: 'active', labels: { region: 'west' } })] });
  await page.goto('/workers');
  page.once('dialog', dialog => dialog.accept('operator request'));
  const row = page.getByRole('row', { name: /D70 Worker/ });
  await row.locator('summary').click();
  await row.getByRole('button', { name: 'Cordon', exact: true }).click();
  const intent = await acceptedAfterPending(page, 'worker', 'cordon');
  expect(JSON.parse(intent.content)).toMatchObject({ worker_pubkey: pubkey, scheduling_state: 'cordoned', labels: { region: 'west' } });
});

test('LLM deploy stays pending until scoped acceptance', async ({ page }) => {
  const routeId = id(3);
  const environmentId = id(4);
  const releaseId = id(5);
  const now = Math.floor(Date.now() / 1000);
  await installE2EMocks(page, { systemInfo, nostrEvents: [
    cpStateFixture({ schema: BAHIA_STATE_SCHEMAS.LLM_ROUTE_REGISTRY, d: `llm-route:${routeId}`,
      content: { id: routeId, name: 'chat-prod', org_id: id(1) }, createdAt: now - 3 }),
    cpStateFixture({ schema: BAHIA_STATE_SCHEMAS.ENVIRONMENT_REGISTRY, d: `environment:${environmentId}`,
      content: { id: environmentId, name: 'production', org_id: id(1) }, createdAt: now - 2 }),
    cpAuditFixture({ type: 'llm.release.registered', entityId: releaseId, createdAt: now - 1,
      data: { schema: 'bahia.result.llm.v1', operation: 'release-register', release_id: releaseId,
        route_id: routeId, version: 'v1' } })
  ] });
  await page.goto('/llm');
  const form = page.getByTestId('llm-request-deploy-form');
  await form.locator('select[name="deploy-route"]').selectOption(routeId);
  await form.locator('select[name="deploy-environment"]').selectOption(environmentId);
  await form.locator('select[name="deploy-release"]').selectOption(releaseId);
  await form.getByRole('button', { name: 'Request deployment' }).click();
  const intent = await acceptedAfterPending(page, 'llm', 'deploy');
  expect(JSON.parse(intent.content)).toMatchObject({ route_id: routeId, environment_id: environmentId, release_id: releaseId });
});

test('backup restore approval stays pending until scoped acceptance', async ({ page }) => {
  const restoreId = id(6);
  await installE2EMocks(page, { systemInfo, nostrEvents: [cpStateFixture({
    schema: BAHIA_STATE_SCHEMAS.BACKUP_RESTORE_STATE, d: `backup-restore:${restoreId}`,
    content: { id: restoreId, restore_id: restoreId, approval_status: 'pending', pending_approval: true,
      status: 'pending', updated_at: '2026-10-03T00:00:00Z' }
  })] });
  await page.goto('/backup/restores');
  page.once('dialog', dialog => dialog.accept('approved'));
  await page.getByRole('button', { name: 'Approve' }).first().click();
  const intent = await acceptedAfterPending(page, 'backup', 'restore-approval');
  expect(JSON.parse(intent.content)).toMatchObject({ restore_id: restoreId, decision: 'approve', message: 'approved' });
});
