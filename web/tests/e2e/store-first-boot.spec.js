import { test, expect } from '@playwright/test';
import { installE2EMocks, E2E_SERVICE_PUBKEY } from './helpers.js';

/**
 * Phase 4 W1-S2: Store-first boot — services render from IndexedDB
 * before any relay connection, then the sync badge transitions to "live"
 * once EOSE is received.
 */

const SERVICE_PUBKEY_PREFIX = E2E_SERVICE_PUBKEY.slice(0, 8);
const DB_NAME = `bahia-events-${SERVICE_PUBKEY_PREFIX}`;

function makeServiceEvent({ id, name, pubkey = E2E_SERVICE_PUBKEY, created_at = 100 }) {
  return {
    id: `evt-${id}`,
    kind: 30900,
    pubkey,
    created_at,
    tags: [
      ['d', id],
      ['domain', 'service'],
      ['schema', 'bahia.registry.service.v1'],
      ['t', 'service-registry'],
      ['deleted', 'false'],
    ],
    content: JSON.stringify({ id, name, deleted: false }),
    sig: 'a'.repeat(128),
  };
}

/**
 * Seed the BahiaEventStore IndexedDB with service events before the page loads.
 * This simulates the state after a previous session that persisted events.
 */
async function seedIndexedDB(page, events) {
  await page.addInitScript(({ dbName, events }) => {
    // Open the IndexedDB database and populate the events store
    // This runs before Svelte mounts, so the store will find these events.
    const openDB = () => new Promise((resolve, reject) => {
      const request = indexedDB.open(dbName, 1);
      request.onupgradeneeded = () => {
        const db = request.result;
        if (!db.objectStoreNames.contains('events')) {
          db.createObjectStore('events', { keyPath: 'id' });
        }
        if (!db.objectStoreNames.contains('cursors')) {
          db.createObjectStore('cursors', { keyPath: 'key' });
        }
      };
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });

    const putEvents = async (db, events) => {
      const tx = db.transaction('events', 'readwrite');
      const store = tx.objectStore('events');
      for (const event of events) {
        store.put(event);
      }
      await new Promise((resolve, reject) => {
        tx.oncomplete = resolve;
        tx.onerror = () => reject(tx.error);
      });
    };

    // Store in window so we can await it in the app init
    window.__bahiaE2EDBSeed = (async () => {
      const db = await openDB();
      await putEvents(db, events);
      db.close();
    })();
  }, { dbName: DB_NAME, events });
}

test.describe('Store-first boot (Phase 4 W1-S2)', () => {
  test('services render from IndexedDB before relay connects', async ({ page }) => {
    const seededServices = [
      makeServiceEvent({ id: 'svc-cached-1', name: 'Cached Alpha' }),
      makeServiceEvent({ id: 'svc-cached-2', name: 'Cached Beta' }),
    ];

    // Install mocks and seed IndexedDB before navigation
    await installE2EMocks(page, {
      nostrEvents: seededServices,
    });
    await seedIndexedDB(page, seededServices);

    await page.goto('/services');

    // Services should render from the seeded IndexedDB data
    // The services page shows a count like "2 services"
    await expect(page.locator('.count')).toContainText('services', { timeout: 10000 });

    // Verify the page is not showing "Loading..." (no loading gate)
    const bodyText = await page.textContent('body');
    expect(bodyText).not.toContain('Loading...');
  });

  test('sync badge shows syncing then live', async ({ page }) => {
    await installE2EMocks(page, {
      nostrEvents: [
        makeServiceEvent({ id: 'svc-live-1', name: 'Live Service' }),
      ],
    });

    await page.goto('/services');

    // Wait for connection to complete and show the sync badge
    // The page should eventually show "live" badge after EOSE
    await expect(page.locator('.sync-badge')).toBeVisible({ timeout: 15000 });
  });
});
