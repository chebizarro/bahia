import { test, expect } from '@playwright/test';
import { DASHBOARD_WIDGET } from '../../src/lib/nostr/kinds.gen.js';
import { E2E_SERVICE_PUBKEY, e2eTestPubkey, installE2EMocks, signE2EEvent } from './helpers.js';

const trusted = e2eTestPubkey('widget-publisher');
const untrusted = e2eTestPubkey('untrusted-widget-publisher');
const now = Math.floor(Date.now() / 1000);

function widget(title, pubkey) {
  return {
    kind: DASHBOARD_WIDGET,
    pubkey,
    created_at: now,
    tags: [['d', 'cpu:host-a:api:5m']],
    content: JSON.stringify({
      type: 'dashboard_widget',
      version: 1,
      widget_kind: 'stat',
      meta: { title, alt: `${title} value` },
      scope: { metric: 'cpu', host: 'host-a', service: 'api' },
      query: { from: now - 60, to: now, step: 60, generated_at: now, staleness_ttl: 300, window: '5m' },
      data: { value: 42 },
      presentation: { template: 'ops.stat.v1' }
    })
  };
}

test('widgets use trusted mock-relay events and survive an unreachable-relay reload from IndexedDB', async ({ page }) => {
  await installE2EMocks(page, {
    nostrEvents: [widget('Trusted CPU', trusted), widget('Untrusted CPU', untrusted)],
    widgetAllowedPubkeys: [trusted]
  });

  await page.goto('/widgets');
  await expect(page.getByText('Trusted CPU')).toBeVisible();
  await expect(page.getByText('Untrusted CPU')).toHaveCount(0);
  await expect(page.getByLabel('Deployment widget relays')).toContainText('relay.test.local');

  // A nonconforming relay can send an event outside the requested author filter.
  const foreign = signE2EEvent(widget('Untrusted CPU', untrusted), untrusted);
  const delivered = await page.evaluate(async (event) => {
    for (const socket of window.__BAHIA_E2E_WS_CONNECTIONS || []) {
      for (const [id, filters] of socket.subscriptions || []) {
        if (!filters.some((filter) => filter.kinds?.includes(event.kind))) continue;
        socket.emitMessage(JSON.stringify(['EVENT', id, event]));
        await new Promise((resolve) => requestAnimationFrame(() => queueMicrotask(resolve)));
        return true;
      }
    }
    return false;
  }, foreign);
  expect(delivered).toBe(true);
  await expect(page.getByText('Untrusted CPU')).toHaveCount(0);

  const live = signE2EEvent({ ...widget('Live CPU', trusted), created_at: now + 1 }, trusted);
  const visibleByNextFrame = await page.evaluate(async (event) => {
    const { getEventStore } = await import('/src/lib/nostr/boot.js');
    const store = getEventStore();
    const received = new Promise((resolve) => {
      const unsubscribe = store.subscribe({ kinds: [event.kind], authors: [event.pubkey] }, (item) => {
        if (item.id !== event.id) return;
        unsubscribe();
        resolve();
      });
    });
    window.__bahiaPushNostrEvent(event);
    await received;
    return new Promise((resolve) => requestAnimationFrame(() => {
      queueMicrotask(() => resolve(document.body.textContent.includes('Live CPU')));
    }));
  }, live);
  expect(visibleByNextFrame).toBe(true);
  await expect(page.getByText('Trusted CPU')).toHaveCount(0);
  await expect(page.getByText('Untrusted CPU')).toHaveCount(0);

  // Wait for the actual IndexedDB commit, not a guessed delay after rendering.
  await expect.poll(() => page.evaluate(async ({ dbName, kind, pubkey, eventId }) => {
    const db = await new Promise((resolve, reject) => {
      const request = indexedDB.open(dbName);
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    try {
      const events = await new Promise((resolve, reject) => {
        const request = db.transaction('events', 'readonly').objectStore('events').getAll();
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => reject(request.error);
      });
      return events.some((event) => event.id === eventId && event.kind === kind && event.pubkey === pubkey);
    } finally {
      db.close();
    }
  }, { dbName: `bahia-events-${E2E_SERVICE_PUBKEY.slice(0, 8)}`, kind: DASHBOARD_WIDGET, pubkey: trusted, eventId: live.id })).toBe(true);

  await page.evaluate(() => {
    localStorage.setItem('__bahia_e2e_nostr_events', '[]');
    sessionStorage.setItem('__bahia_e2e_relay_offline', '1');
  });
  await page.reload();
  await expect(page.getByText('Live CPU')).toBeVisible();
  await expect(page.getByText('Untrusted CPU')).toHaveCount(0);
  await expect.poll(() => page.evaluate(() => (window.__BAHIA_E2E_WS_CONNECTIONS || []).every((socket) => socket.readyState !== WebSocket.OPEN))).toBe(true);
});
