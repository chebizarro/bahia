import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';
import { createPublicSystemInfo } from './harnesses/service-deployment-public.js';

const id = n => `018f6a60-0000-7000-8000-${String(n).padStart(12, '0')}`;
const orgId = id(1);
const serviceId = id(2);
const environmentId = id(3);
const artifactId = id(5);
const digest = `sha256:${'a'.repeat(64)}`;

async function submit(page, operation, payload) {
  await page.evaluate(({ operation, payload }) => {
    window.__LAST_OP = {};
    window.__LAST_OP.outcome = (async () => {
      if (operation === 'dns/drift-remediate') {
        const { remediateDNSDrift } = await import('/src/lib/stores/dns.svelte.js');
        const run = await remediateDNSDrift(payload);
        window.__LAST_OP.run = run;
        return run.result;
      }
      const controlplane = await import('/src/lib/stores/public-controlplane.svelte.js');
      if (operation === 'artifact/register') {
        const submitted = await controlplane.registerArtifact(payload);
        const { acceptedIntentStatus } = await import('/src/lib/nostr/intent-client.svelte.js');
        return acceptedIntentStatus(submitted);
      }
      if (operation === 'deployment/preview') return controlplane.previewServiceDeployment(payload);
      return controlplane.evaluatePolicy(payload);
    })().then(value => ({ value }), error => ({ error: error.message }))
      .then(outcome => { window.__LAST_OP.settled = outcome; return outcome; });
  }, { operation, payload });
  const [domain, op] = operation.split('/');
  await expect.poll(() => page.evaluate(({ domain, op }) => window.__LAST_OP.settled?.error ||
    window.__BAHIA_E2E_SIGNED_INTENTS.find(event =>
      event.tags.some(tag => tag[0] === 'domain' && tag[1] === domain) &&
      event.tags.some(tag => tag[0] === 'op' && tag[1] === op)), { domain, op })).toBeTruthy();
  const error = await page.evaluate(() => window.__LAST_OP.settled?.error);
  if (error) throw new Error(error);
  const intent = await page.evaluate(({ domain, op }) => window.__BAHIA_E2E_SIGNED_INTENTS.find(event =>
    event.tags.some(tag => tag[0] === 'domain' && tag[1] === domain) &&
    event.tags.some(tag => tag[0] === 'op' && tag[1] === op)), { domain, op });
  await expect.poll(() => page.evaluate(intentId => window.__LAST_OP &&
    import('/src/lib/nostr/intent-client.svelte.js').then(module =>
      module.pendingIntentRows.some(row => row.intentId === intentId && row.status === 'pending')),
    intent.tags.find(tag => tag[0] === 'intent_id')[1])).toBe(true);
  return intent;
}

async function accept(page, intent, fields) {
  await page.evaluate(({ intent, fields }) => window.__bahiaPushNostrEvent(
    window.__BAHIA_E2E_MAKE_INTENT_STATUS(intent, { id: `accepted-${intent.id}`, ...fields })),
  { intent, fields });
  const outcome = await page.evaluate(() => window.__LAST_OP.outcome);
  expect(outcome.error).toBeUndefined();
  await expect.poll(() => page.evaluate(intentId => import('/src/lib/nostr/intent-client.svelte.js').then(module =>
    module.pendingIntentRows.some(row => row.intentId === intentId)),
  intent.tags.find(tag => tag[0] === 'intent_id')[1])).toBe(false);
  return outcome.value;
}

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page, { systemInfo: createPublicSystemInfo() });
  await page.goto('/services');
  await expect(page.getByRole('heading', { name: 'Services', exact: true })).toBeVisible();
});

test('artifact register stays pending until accepted status carries artifact data', async ({ page }) => {
  const intent = await submit(page, 'artifact/register', { id: artifactId, org_id: orgId, build_id: id(4),
    service_id: serviceId, image_repo: 'registry.example/api', image_tag: 'v1', image_digest: digest });
  expect(intent.tags).toContainEqual(['d', `artifact:${artifactId}`]);
  expect(await accept(page, intent, { data: { artifact_id: artifactId } })).toMatchObject({ data: { artifact_id: artifactId } });
});

test('DNS drift remediation stays pending until accepted status data completes the run', async ({ page }) => {
  const intent = await submit(page, 'dns/drift-remediate', { zone: 'example.com' });
  expect(intent.tags).toContainEqual(['d', 'dns-remediate:example.com']);
  expect(await accept(page, intent, { data: { status: 'succeeded', message: 'remediated' } }))
    .toMatchObject({ status: 'succeeded', message: 'remediated' });
  expect(await page.evaluate(() => window.__LAST_OP.run.phase)).toBe('completed');
});

test('deployment preview renders only the accepted bounded 30315 plan', async ({ page }) => {
  const intent = await submit(page, 'deployment/preview', { org_id: orgId, service_id: serviceId,
    environment_id: environmentId, artifact_id: artifactId, managed_runtime_config: { replicas: 2 } });
  expect(intent.tags).toContainEqual(['d', `deployment-preview:${serviceId}:${environmentId}`]);
  expect(JSON.parse(intent.content)).toMatchObject({ compact: true });
  const data = { desired_state_hash: digest, desired_state_summary: { image_ref: `registry.example/api@${digest}` },
    policy: { allowed: true, warnings: 0, blockers: 0 } };
  expect(await accept(page, intent, { data })).toEqual(data);
});

test('policy evaluate renders evaluation from accepted 30315, not a request response', async ({ page }) => {
  const intent = await submit(page, 'policy/evaluate', { org_id: orgId, artifact_id: artifactId,
    environment_id: environmentId });
  expect(intent.tags).toContainEqual(['d', `evaluation:${artifactId}:${environmentId}`]);
  const evaluation = { allowed: false, warnings: 0, blockers: 1,
    results: [{ policy_id: id(10), passed: false }] };
  expect(await accept(page, intent, { evaluation })).toEqual(evaluation);
});
