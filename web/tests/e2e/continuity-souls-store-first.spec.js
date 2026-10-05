import { test, expect } from '@playwright/test';
import { E2E_SERVICE_PUBKEY, TEST_PUBKEY, e2eTestPubkey, installE2EMocks } from './helpers.js';

const now = Math.floor(Date.now() / 1000);
const untrusted = e2eTestPubkey('untrusted-continuity-souls');

// Kinds owned by the continuity and SoulFactory read models (A-35/A-36).
const READ_MODEL_KINDS = [30351, 30353, 31400, 31401, 31402, 31403, 31404, 38430, 38431,
  31950, 31951, 31952, 31953, 1950, 1951, 6950, 7950, 30317];

const status = (service, pubkey = E2E_SERVICE_PUBKEY, createdAt = now) => ({
  kind: 30351, pubkey, created_at: createdAt,
  tags: [['d', `continuity-status:${service}`], ['service', service], ['t', 'continuity'], ['t', 'continuity-status']],
  content: JSON.stringify({ service_key: service, active_profile: 'full', operation_state: 'steady' })
});
const soul = (agentId, name, pubkey = E2E_SERVICE_PUBKEY, createdAt = now) => ({
  kind: 31951, pubkey, created_at: createdAt,
  tags: [['d', agentId], ['name', name], ['status', 'active'], ['runtime', 'openclaw']], content: `# ${name}`
});
const fleetConfig = (pubkey = TEST_PUBKEY, model = '') => ({
  kind: 31953, pubkey, created_at: now,
  tags: [['d', 'soulfactory-fleet-config/v1'], ['schema', 'soulfactory-fleet-config/v1']],
  content: JSON.stringify({ schema: 'soulfactory-fleet-config/v1', template: {}, defaults: { model, bindings: [], required_plugins: [] } })
});

const failoverRequest = (service, pubkey) => ({
  kind: 38430, pubkey, created_at: now,
  tags: [['service', service], ['worker', 'standby-a']], content: JSON.stringify({ reason: 'primary unavailable' })
});

async function setup(page) {
  await installE2EMocks(page, { nostrEvents: [
    status('svc-cached'), status('svc-forged', untrusted),
    // Operator-signed kinds are trusted from the signed-in key only.
    failoverRequest('svc-mine', TEST_PUBKEY), failoverRequest('svc-other-operator', untrusted),
    soul('cached-soul', 'Cached Fleet Soul'), soul('forged-soul', 'Forged Fleet Soul', untrusted),
    // A stranger re-signing a trusted Soul's coordinate must not replace it.
    soul('cached-soul', 'Hijacked Fleet Soul', untrusted, now + 60),
    fleetConfig()
  ] });
}

/** Kinds persisted in the browser's verified IndexedDB event store. */
async function cachedKinds(page) {
  return page.evaluate((prefix) => new Promise((resolve, reject) => {
    const request = indexedDB.open(`bahia-events-${prefix}`);
    request.onerror = () => reject(request.error);
    request.onsuccess = () => {
      const db = request.result;
      const all = db.transaction('events', 'readonly').objectStore('events').getAll();
      all.onsuccess = () => { resolve([...new Set(all.result.map((event) => event.kind))]); db.close(); };
      all.onerror = () => reject(all.error);
    };
  }), E2E_SERVICE_PUBKEY.slice(0, 8));
}

/** Empty the mock relay and refuse every socket, then reload: only the cache can render. */
async function reloadWithRelayUnreachable(page) {
  await page.evaluate(() => {
    localStorage.setItem('__bahia_e2e_nostr_events', '[]');
    sessionStorage.setItem('__bahia_e2e_relay_offline', '1');
  });
  await page.reload();
}

const noOpenSocket = (page) => page.evaluate(() =>
  (window.__BAHIA_E2E_WS_CONNECTIONS || []).every((socket) => socket.readyState !== WebSocket.OPEN));

test('continuity renders the verified cache on reload with the relay unreachable', async ({ page }) => {
  await setup(page);
  await page.goto('/continuity');
  await expect(page.getByRole('heading', { name: 'svc-cached' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'svc-forged' })).toHaveCount(0);
  await expect.poll(() => cachedKinds(page)).toContain(30351);

  await reloadWithRelayUnreachable(page);
  await expect(page.getByRole('heading', { name: 'svc-cached' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'svc-forged' })).toHaveCount(0);
  await expect(page.getByText('Loading continuity history')).toHaveCount(0);

  // Operator-signed requests: mine is shown, another signer's is not, and the
  // view says so instead of silently omitting other operators' documents.
  await page.getByRole('button', { name: /^Requests/ }).click();
  await expect(page.getByRole('heading', { name: 'svc-mine' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'svc-other-operator' })).toHaveCount(0);
  await expect(page.getByTestId('continuity-operator-scope-note')).toContainText('other fleet operators are not shown');
  await expect.poll(() => noOpenSocket(page)).toBe(true);
});

test('settings/fleet renders cached souls and fleet config on reload with the relay unreachable', async ({ page }) => {
  await setup(page);
  await page.goto('/settings/fleet');
  await expect(page.locator('.rollout-row strong')).toHaveText('Cached Fleet Soul');
  await expect.poll(() => cachedKinds(page)).toEqual(expect.arrayContaining([31951, 31953]));

  await reloadWithRelayUnreachable(page);
  await expect(page.locator('.rollout-row strong')).toHaveText('Cached Fleet Soul');
  await expect(page.getByText('Forged Fleet Soul')).toHaveCount(0);
  await expect(page.getByText('Hijacked Fleet Soul')).toHaveCount(0);
  await expect(page.getByText('Loading the latest operator-authored fleet document')).toHaveCount(0);
  await expect(page.getByText('Loading retained rollout events')).toHaveCount(0);
  await expect(page.getByTestId('fleet-operator-scope-note')).toContainText('another operator is not shown');
  await expect.poll(() => noOpenSocket(page)).toBe(true);
});

test('soul gallery and detail render from cache with the relay unreachable and keep the trusted signer', async ({ page }) => {
  await setup(page);
  await page.goto('/souls');
  await expect(page.getByText('Cached Fleet Soul')).toBeVisible();
  await expect.poll(() => cachedKinds(page)).toContain(31951);

  await reloadWithRelayUnreachable(page);
  await expect(page.getByText('Cached Fleet Soul')).toBeVisible();
  await expect(page.getByText('Forged Fleet Soul')).toHaveCount(0);
  await expect(page.getByText('Loading souls')).toHaveCount(0);

  await page.goto('/souls/cached-soul');
  await expect(page.getByRole('heading', { name: 'Cached Fleet Soul' })).toBeVisible();
  await expect(page.getByText('Hijacked Fleet Soul')).toHaveCount(0);
  await expect(page.getByText('Soul not found')).toHaveCount(0);
  await expect(page.getByTestId('soul-activity-operator-scope-note')).toContainText('other operators are not shown');
  await expect.poll(() => noOpenSocket(page)).toBe(true);
});

test('live events from trusted signers appear, untrusted ones do not, and returning opens no REQ', async ({ page }) => {
  await setup(page);
  await page.goto('/continuity');
  await expect(page.getByRole('heading', { name: 'svc-cached' })).toBeVisible();

  // The forged event is delivered first on the same sockets, so once the
  // trusted one is rendered the forged one has already been handled.
  await page.evaluate(([forged, live]) => {
    window.__bahiaPushNostrEvent(forged);
    window.__bahiaPushNostrEvent(live);
  }, [status('svc-live-forged', untrusted, now + 1), status('svc-live', E2E_SERVICE_PUBKEY, now + 1)]);
  await expect(page.getByRole('heading', { name: 'svc-live', exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'svc-live-forged' })).toHaveCount(0);

  await page.getByRole('button', { name: 'Open navigation menu' }).click();
  await page.locator('#navigation-drawer a[href="/settings"]').click();
  await page.getByRole('link', { name: /OpenClaw Fleet/ }).click();
  await expect(page.locator('.rollout-row strong')).toHaveText('Cached Fleet Soul');
  await page.evaluate(([forged, live]) => {
    window.__bahiaPushNostrEvent(forged);
    window.__bahiaPushNostrEvent(live);
  }, [soul('live-forged', 'Live Forged Soul', untrusted, now + 1), soul('live-soul', 'Live Fleet Soul', E2E_SERVICE_PUBKEY, now + 1)]);
  await expect(page.locator('.rollout-row strong', { hasText: 'Live Fleet Soul' })).toBeVisible();
  await expect(page.getByText('Live Forged Soul')).toHaveCount(0);

  // Wait until the app-lifetime readers finished paging stored history, so
  // any REQ recorded from here on would be caused by navigation.
  await expect.poll(() => page.evaluate(async () => {
    const { continuityCatchup } = await import('/src/lib/nostr/continuity.ts');
    const { readModelMeta } = await import('/src/lib/stores/souls.svelte.js');
    return continuityCatchup().complete && readModelMeta.souls?.complete === true;
  })).toBe(true);

  // Both views are now warm. Record every REQ that touches their kinds.
  const sockets = await page.evaluate((kinds) => {
    window.__readModelReqs = [];
    const original = WebSocket.prototype.send;
    WebSocket.prototype.send = function(data) {
      try {
        const frame = JSON.parse(data);
        if (frame[0] === 'REQ' && frame.slice(2).some((filter) => filter.kinds?.some((kind) => kinds.includes(kind)))) {
          window.__readModelReqs.push(frame);
        }
      } catch {}
      return original.call(this, data);
    };
    return window.__BAHIA_E2E_WS_CONNECTIONS.length;
  }, READ_MODEL_KINDS);

  await page.locator('a.back-link[href="/settings"]').click();
  await page.getByRole('button', { name: 'Open navigation menu' }).click();
  await page.locator('#navigation-drawer a[href="/continuity"]').click();
  await expect(page.getByRole('heading', { name: 'svc-live', exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Open navigation menu' }).click();
  await page.locator('#navigation-drawer a[href="/settings"]').click();
  await page.getByRole('link', { name: /OpenClaw Fleet/ }).click();
  await expect(page.locator('.rollout-row strong', { hasText: 'Live Fleet Soul' })).toBeVisible();
  await page.getByRole('button', { name: 'Open navigation menu' }).click();
  await page.locator('#navigation-drawer a[href="/souls"]').click();
  await expect(page.getByText('Live Fleet Soul')).toBeVisible();
  await page.getByText('Live Fleet Soul').first().click();
  await expect(page.getByRole('heading', { name: 'Live Fleet Soul' })).toBeVisible();

  const state = await page.evaluate(() => ({
    reqs: window.__readModelReqs, sockets: window.__BAHIA_E2E_WS_CONNECTIONS.length, text: document.body.textContent
  }));
  expect(state.reqs).toEqual([]);
  expect(state.sockets).toBe(sockets);
  expect(state.text).not.toContain('Loading continuity history');
});

test('boot issues discovery and the core read model before the history readers, with no relay answer needed', async ({ page }) => {
  await setup(page);
  // Registered after the relay double, so this wraps its send().
  await page.addInitScript(() => {
    window.__reqOrder = [];
    const original = WebSocket.prototype.send;
    WebSocket.prototype.send = function(data) {
      try { const frame = JSON.parse(data); if (frame[0] === 'REQ') window.__reqOrder.push(frame.slice(2)); } catch {}
      return original.call(this, data);
    };
  });
  await page.goto('/continuity');
  await expect(page.getByRole('heading', { name: 'svc-cached' })).toBeVisible();
  await expect.poll(() => page.evaluate(async () =>
    (await import('/src/lib/nostr/continuity.ts')).continuityCatchup().complete)).toBe(true);

  const order = await page.evaluate(() => {
    const first = (matches) => window.__reqOrder.findIndex((filters) => filters.some(matches));
    return {
      discovery: first((filter) => filter.kinds?.includes(11316)),
      core: first((filter) => filter['#t']?.includes('service-registry')),
      continuity: first((filter) => filter.kinds?.includes(30351)),
      souls: first((filter) => filter.kinds?.includes(31951))
    };
  });
  expect(order.discovery).toBeGreaterThanOrEqual(0);
  expect(order.core).toBeGreaterThanOrEqual(0);
  expect(order.continuity).toBeGreaterThan(Math.max(order.discovery, order.core));
  expect(order.souls).toBeGreaterThan(Math.max(order.discovery, order.core));

  // The readers are issued by ordering, not by a relay answering: with every
  // socket refused they are still started (and simply stay pending).
  await reloadWithRelayUnreachable(page);
  await expect(page.getByRole('heading', { name: 'svc-cached' })).toBeVisible();
  await expect.poll(() => page.evaluate(async () =>
    (await import('/src/lib/nostr/continuity.ts')).continuityCatchup().relaySummary.length)).toBeGreaterThan(0);
  expect(await page.evaluate(async () =>
    (await import('/src/lib/nostr/continuity.ts')).continuityCatchup().complete)).toBe(false);
});
