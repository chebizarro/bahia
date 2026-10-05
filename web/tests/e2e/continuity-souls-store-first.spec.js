import { test, expect } from '@playwright/test';
import { E2E_SERVICE_PUBKEY, TEST_PUBKEY, e2eTestPubkey, installE2EMocks } from './helpers.js';

const now = Math.floor(Date.now() / 1000);
const untrusted = e2eTestPubkey('untrusted-continuity-souls');
const status = (service, pubkey = E2E_SERVICE_PUBKEY) => ({
  kind: 30351, pubkey, created_at: now,
  tags: [['d', `continuity-status:${service}`], ['service', service], ['t', 'continuity'], ['t', 'continuity-status']],
  content: JSON.stringify({ service_key: service, active_profile: 'full', operation_state: 'steady' })
});
const soul = (agent, pubkey = E2E_SERVICE_PUBKEY) => ({
  kind: 31951, pubkey, created_at: now,
  tags: [['d', agent], ['name', agent], ['status', 'active'], ['runtime', 'openclaw']], content: `# ${agent}`
});
const fleetConfig = {
  kind: 31953, pubkey: TEST_PUBKEY, created_at: now,
  tags: [['d', 'soulfactory-fleet-config/v1'], ['schema', 'soulfactory-fleet-config/v1']],
  content: JSON.stringify({ schema: 'soulfactory-fleet-config/v1', template: {}, defaults: { model: '', bindings: [], required_plugins: [] } })
};

async function cachedKinds(page, kinds) {
  return page.evaluate(({ prefix, kinds }) => new Promise((resolve, reject) => {
    const request = indexedDB.open(`bahia-events-${prefix}`);
    request.onerror = () => reject(request.error);
    request.onsuccess = () => {
      const db = request.result;
      const tx = db.transaction('events', 'readonly');
      const all = tx.objectStore('events').getAll();
      all.onsuccess = () => { resolve(all.result.filter((event) => kinds.includes(event.kind)).map((event) => event.kind)); db.close(); };
      all.onerror = () => reject(all.error);
    };
  }), { prefix: E2E_SERVICE_PUBKEY.slice(0, 8), kinds });
}

async function setup(page) {
  await installE2EMocks(page, { nostrEvents: [
    status('svc-cached'), status('svc-forged', untrusted),
    soul('Cached Fleet Soul'), soul('Forged Fleet Soul', untrusted), fleetConfig
  ] });
}

test('continuity renders verified cache with relay unreachable on reload', async ({ page }) => {
  await setup(page);
  await page.goto('/continuity');
  await expect(page.getByRole('heading', { name: 'svc-cached' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'svc-forged' })).toHaveCount(0);
  await expect.poll(() => cachedKinds(page, [30351])).toContain(30351);
  await page.evaluate(() => localStorage.setItem('__bahia_e2e_relay_unreachable', 'true'));
  await page.reload();
  await expect(page.getByRole('heading', { name: 'svc-cached' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'svc-forged' })).toHaveCount(0);
  expect(await page.locator('body').innerText()).not.toContain('Loading continuity history');


});


test('fleet renders verified cached souls with relay unreachable on reload', async ({ page }) => {
  await setup(page);
  await page.goto('/settings/fleet');
  await expect(page.locator('.rollout-row strong')).toHaveText('Cached Fleet Soul');
  await expect(page.getByText('Forged Fleet Soul')).toHaveCount(0);
  await expect.poll(() => cachedKinds(page, [31951, 31953])).toEqual(expect.arrayContaining([31951, 31953]));
  await page.evaluate(() => localStorage.setItem('__bahia_e2e_relay_unreachable', 'true'));
  await page.reload();
  await expect(page.locator('.rollout-row strong')).toHaveText('Cached Fleet Soul');
  await expect(page.getByText('Forged Fleet Soul')).toHaveCount(0);
  expect(await page.locator('body').innerText()).not.toContain('Loading retained rollout events');
});
test('continuity and souls update by the next frame and return navigation opens no REQ', async ({ page }) => {
  await setup(page);
  await page.goto('/continuity');
  await expect(page.getByRole('heading', { name: 'svc-cached' })).toBeVisible();
  const continuityFrame = await page.evaluate(async (live) => {
    const { getEventStore } = await import('/src/lib/nostr/boot.js');
    return new Promise((resolve) => {
      const off = getEventStore().subscribe({ kinds: [30351], authors: [live.pubkey] }, (event) => {
        if (!event.tags.some((tag) => tag[0] === 'service' && tag[1] === 'svc-live')) return;
        off();
        requestAnimationFrame(() => resolve(!![...document.querySelectorAll('.service-card h2')].find((node) => node.textContent === 'svc-live')));
      });
      window.__bahiaPushNostrEvent(live);
    });
  }, status('svc-live'));
  expect(continuityFrame).toBe(true);

  await page.getByRole('button', { name: 'Open navigation menu' }).click();
  await page.locator('#navigation-drawer a[href="/settings"]').click();
  await page.getByRole('link', { name: /OpenClaw Fleet/ }).click();
  await expect(page.locator('.rollout-row strong')).toHaveText('Cached Fleet Soul');
  const soulFrame = await page.evaluate(async (live) => {
    const { getEventStore } = await import('/src/lib/nostr/boot.js');
    const { souls } = await import('/src/lib/stores/souls.svelte.js');
    return new Promise((resolve) => {
      const off = getEventStore().subscribe({ kinds: [31951], authors: [live.pubkey] }, (event) => {
        if (!event.tags.some((tag) => tag[0] === 'd' && tag[1] === 'Live Fleet Soul')) return;
        off();
        requestAnimationFrame(() => resolve(souls.some((row) => row.agentId === 'Live Fleet Soul')));
      });
      window.__bahiaPushNostrEvent(live);
    });
  }, soul('Live Fleet Soul'));
  expect(soulFrame).toBe(true);
  await expect(page.locator('.rollout-row strong', { hasText: 'Live Fleet Soul' })).toBeVisible();

  await page.evaluate(() => {
    window.__newReqs = [];
    const send = WebSocket.prototype.send;
    WebSocket.prototype.send = function(data) {
      try { const frame = JSON.parse(data); if (frame[0] === 'REQ') window.__newReqs.push(frame); } catch {}
      return send.call(this, data);
    };
  });
  await page.locator('a.back-link[href="/settings"]').click();
  await page.getByRole('button', { name: 'Open navigation menu' }).click();
  await page.locator('#navigation-drawer a[href="/continuity"]').click();
  await expect(page.getByRole('heading', { name: 'svc-cached' })).toBeVisible();
  await page.getByRole('button', { name: 'Open navigation menu' }).click();
  await page.locator('#navigation-drawer a[href="/settings"]').click();
  await page.getByRole('link', { name: /OpenClaw Fleet/ }).click();
  await expect(page.locator('.rollout-row strong')).toHaveText('Cached Fleet Soul');
  expect(await page.evaluate(() => window.__newReqs)).toEqual([]);
});
