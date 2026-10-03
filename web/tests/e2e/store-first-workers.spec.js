import { test, expect } from '@playwright/test';
import { E2E_SERVICE_PUBKEY, e2eTestPubkey, installE2EMocks, signE2EEvent } from './helpers.js';
import { workerStateFixture } from './cp-state-fixtures.js';
import { WORKER_STATUS, WORKER_RESULT } from '../../src/lib/nostr/kinds.gen.js';

const workerPubkey = e2eTestPubkey('store-first-worker');
const requestId = '7'.repeat(64);
const now = Math.floor(Date.now() / 1000);

function workerFixture(status = 'online', createdAt = now - 3) {
  return workerStateFixture({ pubkey: workerPubkey, name: 'Store Worker', status, scheduling_state: status === 'cordoned' ? 'cordoned' : 'active' }, { createdAt });
}

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page, {
    nostrEvents: [
      workerFixture(),
      signE2EEvent({ kind: WORKER_STATUS, created_at: now - 2, tags: [['e', requestId], ['worker', workerPubkey], ['status', 'running']], content: JSON.stringify({ message: 'draining' }) }, E2E_SERVICE_PUBKEY)
    ]
  });
});

test('workers and operations render from store on return without worker REST reads or spinners', async ({ page }) => {
  const workerReads = [];
  page.on('request', (request) => {
    if (new URL(request.url()).pathname.startsWith('/api/v1/workers')) workerReads.push(request.url());
  });
  await page.goto('/workers');
  const row = page.getByRole('row', { name: /Store Worker/ });
  await expect(row).toBeVisible();
  await expect(row).toContainText('running');
  await expect(page.getByText('Loading...', { exact: true })).toHaveCount(0);

  await row.locator('a.worker-link').click();
  await expect(page.getByRole('heading', { name: 'Store Worker' })).toBeVisible();
  await page.locator('a.back[href="/workers"]').click();
  await expect(row).toBeVisible();
  await expect(page.getByText('Loading...', { exact: true })).toHaveCount(0);
  expect(workerReads).toEqual([]);
});

test('live worker state and operation result reach /workers without a collection reload', async ({ page }) => {
  await page.goto('/workers');
  const row = page.getByRole('row', { name: /Store Worker/ });
  await expect(row).toContainText('running');

  const result = signE2EEvent({
    kind: WORKER_RESULT,
    created_at: now,
    tags: [['e', requestId], ['worker', workerPubkey], ['status', 'success']],
    content: JSON.stringify({ message: 'drained' })
  }, E2E_SERVICE_PUBKEY);
  const afterOneFrame = await page.evaluate(async ({ stateEvent, resultEvent }) => {
    const { getEventStore } = await import('/src/lib/nostr/boot.js');
    const store = getEventStore();
    const events = [
      await window.__bahiaE2ESignMockEvent(stateEvent, stateEvent.pubkey),
      resultEvent
    ];
    const ingested = events.map((event) => new Promise((resolve) => {
      const unsubscribe = store.subscribe({ kinds: [event.kind] }, (received) => {
        if (received.id !== event.id) return;
        unsubscribe();
        resolve();
      });
    }));
    for (const event of events) {
      for (const socket of window.__BAHIA_E2E_WS_CONNECTIONS || []) {
        for (const [id, filters] of socket.subscriptions || []) {
          if (!filters.some((filter) => filter.kinds?.includes(event.kind)
            && (!filter.authors || filter.authors.includes(event.pubkey))
            && (!filter['#t'] || event.tags.some((tag) => tag[0] === 't' && filter['#t'].includes(tag[1]))))) continue;
          socket.emitMessage(JSON.stringify(['EVENT', id, event]));
        }
      }
    }
    await Promise.all(ingested);
    return new Promise((resolve) => requestAnimationFrame(() => {
      queueMicrotask(() => resolve(document.querySelector('tbody')?.textContent || ''));
    }));
  }, { stateEvent: workerFixture('cordoned', now + 1), resultEvent: result });

  expect(afterOneFrame).toContain('cordoned');
  expect(afterOneFrame).toContain('success');
  await expect(row).toContainText('cordoned');
  await expect(row).toContainText('success');
  await expect(page.getByText('Loading...', { exact: true })).toHaveCount(0);
});
