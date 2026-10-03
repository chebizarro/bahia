import { test, expect } from '@playwright/test';
import { installE2EMocks, E2E_SERVICE_PUBKEY } from './helpers.js';
import { cpStateFixture } from './cp-state-fixtures.js';

const createdAt = Math.floor(Date.now() / 1000);
const fixture = (schema, d, name, offset = 0) => cpStateFixture({
  schema, d, createdAt: createdAt + offset,
  content: { id: d, name, deleted: false }
});

async function setup(page) {
  await page.route('**/api/v1/route-canaries**', route => route.fulfill({ json: { data: [] } }));
  await installE2EMocks(page, {
    nostrEvents: [
      fixture('bahia.registry.service.v1', 'svc-core', 'Core Service'),
      fixture('bahia.registry.environment.v1', 'env-core', 'Core Environment'),
      fixture('bahia.registry.policy.v1', 'policy-core', 'Core Policy')
    ]
  });
}

test('core list and detail navigation reuses store data without a new core REQ or loading gate', async ({ page }) => {
  await setup(page);
  await page.goto('/services');
  await expect(page.getByRole('cell', { name: 'Core Service' })).toBeVisible();
  const first = await page.evaluate(() => {
    window.__coreReqs = [];
    const original = WebSocket.prototype.send;
    WebSocket.prototype.send = function(data) {
      try {
        const frame = JSON.parse(data);
        if (frame[0] === 'REQ' && frame.slice(2).some(filter => filter['#t']?.some(topic =>
          ['service-registry', 'environment-registry', 'policy-registry'].includes(topic)))) window.__coreReqs.push(frame);
      } catch {}
      return original.call(this, data);
    };
    return window.__BAHIA_E2E_WS_CONNECTIONS.length;
  });
  await page.getByRole('row', { name: /Core Service/ }).click();
  await expect(page.getByRole('heading', { name: 'Core Service' })).toBeVisible();
  await page.locator('a[href="/services"].back').click();
  await page.getByRole('button', { name: 'Open navigation menu' }).click();
  await page.locator('#navigation-drawer a[href="/environments"]').click();
  await expect(page.getByText('Core Environment')).toBeVisible();
  await page.getByRole('link', { name: 'Core Environment' }).click();
  await expect(page.getByRole('heading', { name: 'Core Environment' })).toBeVisible();
  await page.locator('a[href="/environments"].back').click();
  await page.getByRole('button', { name: 'Open navigation menu' }).click();
  await page.locator('#navigation-drawer a[href="/policies"]').click();
  await expect(page.getByText('Core Policy')).toBeVisible();
  await page.getByRole('link', { name: 'Core Policy' }).click();
  await expect(page.getByRole('heading', { name: 'Core Policy' })).toBeVisible();
  await page.locator('a[href="/policies"].back').click();
  await page.getByRole('button', { name: 'Open navigation menu' }).click();
  await page.locator('#navigation-drawer a[href="/services"]').click();
  await expect(page.getByRole('cell', { name: 'Core Service' })).toBeVisible();
  const state = await page.evaluate(() => ({ reqs: window.__coreReqs.length, sockets: window.__BAHIA_E2E_WS_CONNECTIONS.length, text: document.body.textContent }));
  expect(state.reqs).toBe(0);
  expect(state.sockets).toBe(first);
  expect(state.text).not.toContain('Loading...');
});

for (const domain of [
  { route: '/services', schema: 'bahia.registry.service.v1', topic: 'service-registry', module: 'services', array: 'services', label: 'Service' },
  { route: '/environments', schema: 'bahia.registry.environment.v1', topic: 'environment-registry', module: 'environments', array: 'environments', label: 'Environment' },
  { route: '/policies', schema: 'bahia.registry.policy.v1', topic: 'policy-registry', module: 'deployments', array: 'policies', label: 'Policy' }
]) {
  test(`${domain.label}: live 30900 appears by the next frame and kind-5 removes it`, async ({ page }) => {
    await setup(page);
    await page.goto(domain.route);
    await expect(page.getByRole('cell', { name: `Core ${domain.label}` })).toBeVisible();
    const d = `live-${domain.label.toLowerCase()}`;
    const name = `Live ${domain.label}`;
    const live = fixture(domain.schema, d, name, 1);
    const visibleInFrame = await page.evaluate(async ({ live, module, array, topic, d, name }) => {
      const { getEventStore } = await import('/src/lib/nostr/boot.js');
      const collection = await import(`/src/lib/stores/collections/${module}.svelte.js`);
      return new Promise((resolve) => {
        const off = getEventStore().subscribe({ kinds: [30900], '#t': [topic] }, (event) => {
          if (!event.tags.some(tag => tag[0] === 'd' && tag[1] === d)) return;
          off();
          requestAnimationFrame(() => resolve(collection[array].some(row => row.name === name)));
        });
        window.__bahiaPushNostrEvent(live);
      });
    }, { live, module: domain.module, array: domain.array, topic: domain.topic, d, name });
    expect(visibleInFrame).toBe(true);
    await expect(page.getByRole('cell', { name })).toBeVisible();

    await page.evaluate(({ pubkey, time, d }) => window.__bahiaPushNostrEvent({
      kind: 5, pubkey, created_at: time,
      tags: [['a', `30900:${pubkey}:${d}`]], content: ''
    }), { pubkey: E2E_SERVICE_PUBKEY, time: createdAt + 2, d });
    await expect(page.getByRole('cell', { name })).toHaveCount(0);
  });
}
