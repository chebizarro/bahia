/**
 * Tests for BahiaEventStore (W1-S1).
 *
 * Covers:
 * - Persistence across "reload" (re-open the DB)
 * - Per-(relay, filter) cursor persistence
 * - Kind-5 removal (and ignoring kind-5 from a different author)
 * - NIP-40 expiry sweep
 * - Addressable latest-wins with tiebreak by lowest id
 * - Namespace isolation between two service pubkeys
 * - Eviction respecting size
 * - Ingestion rejecting bad signatures
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import 'fake-indexeddb/auto';
import { generateSecretKey, getPublicKey, finalizeEvent } from 'nostr-tools';
import { createBahiaEventStore } from '../../src/lib/nostr/store.js';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Generate a keypair. */
function keygen() {
  const sk = generateSecretKey();
  const pk = getPublicKey(sk);
  return { sk, pk };
}

/**
 * Create a properly signed Nostr event.
 * @param {Uint8Array} sk - Secret key
 * @param {object} template - Event fields (kind, content, tags, created_at)
 * @returns {import('../../src/lib/nostr/store-interface.js').NostrEvent}
 */
function signedEvent(sk, template) {
  return finalizeEvent({
    kind: template.kind ?? 1,
    content: template.content ?? '',
    tags: template.tags ?? [],
    created_at: template.created_at ?? Math.floor(Date.now() / 1000),
  }, sk);
}

// Unique prefix per test run to avoid IndexedDB collisions across tests
let testCounter = 0;
function uniquePrefix(pk) {
  testCounter++;
  return pk.slice(0, 6) + String(testCounter).padStart(2, '0');
}

// Generate test keys once per module
const alice = keygen();
const bob = keygen();

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('BahiaEventStore', () => {
  /** @type {ReturnType<typeof createBahiaEventStore>} */
  let store;
  let prefix;

  beforeEach(async () => {
    prefix = uniquePrefix(alice.pk);
    store = createBahiaEventStore({ servicePubkeyPrefix: prefix });
    await store.open();
  });

  afterEach(async () => {
    await store.close();
  });

  // ── Ingestion basics ──────────────────────────────────────────────

  it('accepts a properly signed event', () => {
    const event = signedEvent(alice.sk, { kind: 1, content: 'hello' });
    expect(store.ingest(event)).toBe(true);
    expect(store.count).toBe(1);
  });

  it('rejects bad signatures', () => {
    const event = signedEvent(alice.sk, { kind: 1, content: 'hello' });
    // Create a new object (not spread, which copies verifiedSymbol)
    const bad = {
      id: event.id,
      pubkey: event.pubkey,
      created_at: event.created_at,
      kind: event.kind,
      tags: event.tags,
      content: event.content,
      sig: '0'.repeat(128),
    };
    expect(store.ingest(bad)).toBe(false);
    expect(store.count).toBe(0);
  });

  it('rejects events with missing fields', () => {
    expect(store.ingest({ id: 'bad' })).toBe(false);
    expect(store.ingest(null)).toBe(false);
    expect(store.ingest(42)).toBe(false);
  });

  it('deduplicates identical events', () => {
    const event = signedEvent(alice.sk, { kind: 1, content: 'dup' });
    expect(store.ingest(event)).toBe(true);
    expect(store.ingest(event)).toBe(false);
    expect(store.count).toBe(1);
  });

  // ── Persistence across "reload" ───────────────────────────────────

  it('persists events across store close/reopen', async () => {
    const event = signedEvent(alice.sk, { kind: 1, content: 'persist me' });
    store.ingest(event);

    // Wait for fire-and-forget IndexedDB write
    await new Promise(r => setTimeout(r, 50));

    // Close and reopen with the same prefix
    await store.close();
    store = createBahiaEventStore({ servicePubkeyPrefix: prefix });
    await store.open();

    expect(store.count).toBe(1);
    const results = store.query({ kinds: [1] });
    expect(results).toHaveLength(1);
    expect(results[0].id).toBe(event.id);
  });

  // ── Per-(relay, filter) cursor persistence ────────────────────────

  it('persists cursors across store close/reopen', async () => {
    store.setCursor('wss://relay1.example', 'filter-abc', 1700000000);
    store.setCursor('wss://relay2.example', 'filter-xyz', 1700000500);

    await new Promise(r => setTimeout(r, 50));
    await store.close();

    store = createBahiaEventStore({ servicePubkeyPrefix: prefix });
    await store.open();

    expect(store.getCursor('wss://relay1.example', 'filter-abc')).toBe(1700000000);
    expect(store.getCursor('wss://relay2.example', 'filter-xyz')).toBe(1700000500);
    expect(store.getCursor('wss://relay3.example', 'filter-nope')).toBeNull();
  });

  // ── Kind-5 removal ────────────────────────────────────────────────

  it('kind-5 from the same author removes the target event', () => {
    const target = signedEvent(alice.sk, { kind: 30900, content: '{"name":"svc1"}', tags: [['d', 'svc:1']] });
    store.ingest(target);
    expect(store.count).toBe(1);

    const deletion = signedEvent(alice.sk, {
      kind: 5,
      content: '',
      tags: [['e', target.id]],
    });
    store.deleteTombstoned(deletion);

    // Target should be removed (kind 5 itself gets stored)
    const results = store.query({ kinds: [30900] });
    expect(results).toHaveLength(0);
  });

  it('ingests kind-5 through the same subscription path and notifies the view', () => {
    const target = signedEvent(alice.sk, { kind: 30900, tags: [['d', 'svc:live'], ['t', 'service-registry']], content: '{"id":"svc:live"}', created_at: 100 });
    const received = [];
    const unsub = store.subscribe({ kinds: [5], authors: [alice.pk] }, event => received.push(event));
    expect(store.ingest(target)).toBe(true);
    const deletion = signedEvent(alice.sk, { kind: 5, tags: [['a', `30900:${alice.pk}:svc:live`]], created_at: 101 });
    expect(store.ingest(deletion)).toBe(true);
    expect(received.map(event => event.id)).toEqual([deletion.id]);
    expect(store.query({ kinds: [30900], '#t': ['service-registry'] })).toEqual([]);
    unsub();
  });

  it('does not resurrect a deleted coordinate when the IndexedDB store reopens', async () => {
    const prefix = uniquePrefix(alice.pk);
    const database = createBahiaEventStore({ servicePubkeyPrefix: prefix });
    await database.open();
    const target = signedEvent(alice.sk, { kind: 30900, tags: [['d', 'svc:deleted'], ['t', 'service-registry']], content: '{"id":"svc:deleted"}', created_at: 100 });
    const deletion = signedEvent(alice.sk, { kind: 5, tags: [['a', `30900:${alice.pk}:svc:deleted`]], created_at: 101 });
    database.ingest(target);
    database.ingest(deletion);
    // A subsequent transaction is ordered after the store's pending writes.
    const idb = await new Promise((resolve, reject) => {
      const request = indexedDB.open(`bahia-events-${prefix}`, 1);
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    await new Promise((resolve, reject) => {
      const transaction = idb.transaction('events', 'readonly');
      transaction.objectStore('events').getAll();
      transaction.oncomplete = resolve;
      transaction.onerror = () => reject(transaction.error);
    });
    idb.close();
    await database.close();
    const reopened = createBahiaEventStore({ servicePubkeyPrefix: prefix });
    await reopened.open();
    expect(reopened.query({ kinds: [30900], '#t': ['service-registry'] })).toEqual([]);
    await reopened.close();
  });

  it('rejects an older coordinate event delivered after an a-tag deletion', () => {
    const deletion = signedEvent(alice.sk, { kind: 5, tags: [['a', `30900:${alice.pk}:svc:late`]], created_at: 101 });
    expect(store.ingest(deletion)).toBe(true);
    const late = signedEvent(alice.sk, { kind: 30900, tags: [['d', 'svc:late'], ['t', 'service-registry']], content: '{"id":"svc:late"}', created_at: 100 });
    expect(store.ingest(late)).toBe(false);
    expect(store.query({ kinds: [30900], '#d': ['svc:late'] })).toEqual([]);
  });

  it('accepts a canonical revision newer than its coordinate deletion', () => {
    const deletion = signedEvent(alice.sk, { kind: 5, tags: [['a', `30900:${alice.pk}:svc:recreated`]], created_at: 101 });
    expect(store.ingest(deletion)).toBe(true);
    const newer = signedEvent(alice.sk, { kind: 30900, tags: [['d', 'svc:recreated'], ['t', 'service-registry']], content: '{"id":"svc:recreated"}', created_at: 102 });
    expect(store.ingest(newer)).toBe(true);
    expect(store.query({ kinds: [30900], '#d': ['svc:recreated'] }).map(event => event.id)).toEqual([newer.id]);
  });

  it('kind-5 from a DIFFERENT author is ignored', () => {
    const target = signedEvent(alice.sk, { kind: 30900, content: '{"name":"svc1"}', tags: [['d', 'svc:1']] });
    store.ingest(target);
    expect(store.count).toBe(1);

    // Bob tries to delete Alice's event
    const deletion = signedEvent(bob.sk, {
      kind: 5,
      content: '',
      tags: [['e', target.id]],
    });
    store.deleteTombstoned(deletion);

    // Target should still exist
    const results = store.query({ kinds: [30900] });
    expect(results).toHaveLength(1);
    expect(results[0].id).toBe(target.id);
  });

  // ── NIP-40 expiry sweep ───────────────────────────────────────────

  it('sweeps events with expired NIP-40 tags', () => {
    const pastTs = Math.floor(Date.now() / 1000) - 3600; // 1 hour ago
    const expired = signedEvent(alice.sk, {
      kind: 1,
      content: 'will expire',
      tags: [['expiration', String(pastTs)]],
    });
    // Force-ingest: the event was valid when created but is now expired.
    // The welshman repository will accept it; our sweep cleans it up.
    store.repository.publish(expired);

    const futureTs = Math.floor(Date.now() / 1000) + 3600; // 1 hour from now
    const alive = signedEvent(alice.sk, {
      kind: 1,
      content: 'still alive',
      tags: [['expiration', String(futureTs)]],
    });
    store.ingest(alive);

    store.sweepExpired();

    // Only the non-expired event should remain
    const results = store.query({ kinds: [1] });
    expect(results).toHaveLength(1);
    expect(results[0].content).toBe('still alive');
  });

  it('rejects already-expired events on ingest', () => {
    const pastTs = Math.floor(Date.now() / 1000) - 3600;
    const expired = signedEvent(alice.sk, {
      kind: 1,
      content: 'expired',
      tags: [['expiration', String(pastTs)]],
    });
    expect(store.ingest(expired)).toBe(false);
  });

  // ── Addressable latest-wins with tiebreak ─────────────────────────

  it('keeps the newest addressable event (same d tag)', () => {
    const older = signedEvent(alice.sk, {
      kind: 30900,
      content: '{"v":1}',
      tags: [['d', 'svc:test']],
      created_at: 1700000000,
    });
    const newer = signedEvent(alice.sk, {
      kind: 30900,
      content: '{"v":2}',
      tags: [['d', 'svc:test']],
      created_at: 1700000010,
    });

    store.ingest(older);
    store.ingest(newer);

    const results = store.query({ kinds: [30900], '#d': ['svc:test'] });
    expect(results).toHaveLength(1);
    expect(results[0].content).toBe('{"v":2}');
  });

  it('tiebreaks by lowest id when created_at is identical', () => {
    // Generate two events with the same timestamp
    const ts = 1700000000;
    const e1 = signedEvent(alice.sk, {
      kind: 30900,
      content: '{"v":"a"}',
      tags: [['d', 'svc:tie']],
      created_at: ts,
    });
    const e2 = signedEvent(alice.sk, {
      kind: 30900,
      content: '{"v":"b"}',
      tags: [['d', 'svc:tie']],
      created_at: ts,
    });

    // Ingest both — regardless of order, the lowest id should win
    store.ingest(e1);
    store.ingest(e2);

    const results = store.query({ kinds: [30900], '#d': ['svc:tie'] });
    expect(results).toHaveLength(1);

    // NIP-01: lowest id wins when created_at is identical
    const expectedWinner = e1.id < e2.id ? e1 : e2;
    expect(results[0].id).toBe(expectedWinner.id);
  });

  it('hydrates the lowest-id winner from persisted equal-timestamp coordinates', async () => {
    const prefix = uniquePrefix(alice.pk);
    const database = createBahiaEventStore({ servicePubkeyPrefix: prefix });
    await database.open();
    await database.close();
    const first = signedEvent(alice.sk, { kind: 30900, tags: [['d', 'svc:persisted-tie']], content: '{"v":1}', created_at: 1700000000 });
    const second = signedEvent(alice.sk, { kind: 30900, tags: [['d', 'svc:persisted-tie']], content: '{"v":2}', created_at: 1700000000 });
    const idb = await new Promise((resolve, reject) => {
      const request = indexedDB.open(`bahia-events-${prefix}`, 1);
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    await new Promise((resolve, reject) => {
      const transaction = idb.transaction('events', 'readwrite');
      transaction.objectStore('events').put(first);
      transaction.objectStore('events').put(second);
      transaction.oncomplete = resolve;
      transaction.onerror = () => reject(transaction.error);
    });
    idb.close();
    const reopened = createBahiaEventStore({ servicePubkeyPrefix: prefix });
    await reopened.open();
    expect(reopened.query({ kinds: [30900], '#d': ['svc:persisted-tie'] }).map(event => event.id)).toEqual([first.id < second.id ? first.id : second.id]);
    await reopened.close();
  });

  // ── Namespace isolation ───────────────────────────────────────────

  it('isolates events between different service pubkey namespaces', async () => {
    // Store 1 has events
    const event1 = signedEvent(alice.sk, { kind: 1, content: 'store1' });
    store.ingest(event1);
    await new Promise(r => setTimeout(r, 50));
    await store.close();

    // Store 2 with a different prefix
    const prefix2 = uniquePrefix(bob.pk);
    const store2 = createBahiaEventStore({ servicePubkeyPrefix: prefix2 });
    await store2.open();

    expect(store2.count).toBe(0);
    const results = store2.query({ kinds: [1] });
    expect(results).toHaveLength(0);

    await store2.close();

    // Reopen store 1 — its data should still be there
    store = createBahiaEventStore({ servicePubkeyPrefix: prefix });
    await store.open();
    expect(store.count).toBe(1);
  });

  // ── Eviction ──────────────────────────────────────────────────────

  it('eviction removes oldest non-addressable events', async () => {
    // Insert several regular events
    for (let i = 0; i < 10; i++) {
      const event = signedEvent(alice.sk, {
        kind: 1,
        content: `msg-${i}`,
        created_at: 1700000000 + i,
      });
      store.ingest(event);
    }

    // Insert an addressable event (should never be evicted)
    const addressable = signedEvent(alice.sk, {
      kind: 30900,
      content: '{"name":"keep-me"}',
      tags: [['d', 'svc:keep']],
      created_at: 1700000000,
    });
    store.ingest(addressable);

    expect(store.count).toBe(11);

    // Mock navigator.storage.estimate to report over-budget
    const origNavigator = globalThis.navigator;
    globalThis.navigator = {
      storage: {
        estimate: async () => ({ usage: 200 * 1024 * 1024, quota: 500 * 1024 * 1024 }),
      },
    };

    await store.evict();

    // 10% of 10 regular candidates = 1 evicted, so 10 remain total
    expect(store.count).toBe(10);
    // But the addressable event should remain
    const addr = store.query({ kinds: [30900], '#d': ['svc:keep'] });
    expect(addr).toHaveLength(1);

    globalThis.navigator = origNavigator;
  });

  // ── Query ─────────────────────────────────────────────────────────

  it('queries by kind', () => {
    store.ingest(signedEvent(alice.sk, { kind: 1, content: 'note' }));
    store.ingest(signedEvent(alice.sk, { kind: 30900, content: '{}', tags: [['d', 'x']] }));

    expect(store.query({ kinds: [1] })).toHaveLength(1);
    expect(store.query({ kinds: [30900] })).toHaveLength(1);
    expect(store.query({ kinds: [999] })).toHaveLength(0);
  });

  it('queries by author', () => {
    store.ingest(signedEvent(alice.sk, { kind: 1, content: 'alice' }));
    store.ingest(signedEvent(bob.sk, { kind: 1, content: 'bob' }));

    expect(store.query({ authors: [alice.pk] })).toHaveLength(1);
    expect(store.query({ authors: [bob.pk] })).toHaveLength(1);
  });

  // ── Subscribe ─────────────────────────────────────────────────────

  it('notifies subscribers of matching events', () => {
    const received = [];
    const unsub = store.subscribe({ kinds: [1] }, (event) => {
      received.push(event);
    });

    store.ingest(signedEvent(alice.sk, { kind: 1, content: 'live' }));
    store.ingest(signedEvent(alice.sk, { kind: 30900, content: '{}', tags: [['d', 'no-match']] }));

    expect(received).toHaveLength(1);
    expect(received[0].content).toBe('live');

    unsub();

    store.ingest(signedEvent(alice.sk, { kind: 1, content: 'after unsub' }));
    expect(received).toHaveLength(1);  // no new notification
  });

  // ── Replaceable events (kind 0, 3, 10000–19999) ──────────────────

  it('replaces kind 0 (profile) keeping newest', () => {
    const old = signedEvent(alice.sk, {
      kind: 0,
      content: '{"name":"old"}',
      created_at: 1700000000,
    });
    const newer = signedEvent(alice.sk, {
      kind: 0,
      content: '{"name":"new"}',
      created_at: 1700000010,
    });

    store.ingest(old);
    store.ingest(newer);

    const results = store.query({ kinds: [0], authors: [alice.pk] });
    expect(results).toHaveLength(1);
    expect(results[0].content).toBe('{"name":"new"}');
  });
});

// ---------------------------------------------------------------------------
// requestPersistentStorage tests
// ---------------------------------------------------------------------------

import { requestPersistentStorage } from '../../src/lib/nostr/store-interface.js';

describe('requestPersistentStorage', () => {
  it('returns true when navigator.storage.persist() grants permission', async () => {
    const origNavigator = globalThis.navigator;
    globalThis.navigator = {
      storage: { persist: async () => true },
    };

    const result = await requestPersistentStorage();
    expect(result).toBe(true);

    globalThis.navigator = origNavigator;
  });

  it('returns false when navigator.storage.persist() denies permission', async () => {
    const origNavigator = globalThis.navigator;
    globalThis.navigator = {
      storage: { persist: async () => false },
    };

    const result = await requestPersistentStorage();
    expect(result).toBe(false);

    globalThis.navigator = origNavigator;
  });

  it('returns false when navigator.storage is unavailable', async () => {
    const origNavigator = globalThis.navigator;
    globalThis.navigator = {};

    const result = await requestPersistentStorage();
    expect(result).toBe(false);

    globalThis.navigator = origNavigator;
  });

  it('returns false when navigator is undefined (SSR)', async () => {
    const origNavigator = globalThis.navigator;
    delete globalThis.navigator;

    const result = await requestPersistentStorage();
    expect(result).toBe(false);

    globalThis.navigator = origNavigator;
  });

  it('returns false and logs when persist() throws', async () => {
    const origNavigator = globalThis.navigator;
    globalThis.navigator = {
      storage: { persist: async () => { throw new Error('denied'); } },
    };

    const result = await requestPersistentStorage();
    expect(result).toBe(false);

    globalThis.navigator = origNavigator;
  });
});

// ---------------------------------------------------------------------------
// NIP-01 tiebreak: confirm welshman Repository can't override our guard
// ---------------------------------------------------------------------------

import { Repository } from '@welshman/net';

describe('NIP-01 tiebreak protection', () => {
  it('ingestion rejects higher-id event even though welshman Repository would accept it', async () => {
    const prefix = uniquePrefix(alice.pk);
    const store2 = createBahiaEventStore({ servicePubkeyPrefix: prefix });
    await store2.open();

    const ts = 1700000000;
    const e1 = signedEvent(alice.sk, {
      kind: 30900,
      content: '{"v":"first"}',
      tags: [['d', 'svc:tiebreak-guard']],
      created_at: ts,
    });
    const e2 = signedEvent(alice.sk, {
      kind: 30900,
      content: '{"v":"second"}',
      tags: [['d', 'svc:tiebreak-guard']],
      created_at: ts,
    });

    // Determine which has the lower id
    const [lower, higher] = e1.id < e2.id ? [e1, e2] : [e2, e1];

    // Ingest the lower-id event first
    expect(store2.ingest(lower)).toBe(true);

    // Now try to ingest the higher-id event with same created_at
    // Our tiebreak guard should reject it
    expect(store2.ingest(higher)).toBe(false);

    // The repository should still hold only the lower-id event
    const results = store2.query({ kinds: [30900], '#d': ['svc:tiebreak-guard'] });
    expect(results).toHaveLength(1);
    expect(results[0].id).toBe(lower.id);

    // Verify that welshman Repository alone WOULD have accepted it
    // (proving our guard is necessary)
    const rawRepo = new Repository();
    rawRepo.publish(lower);
    const accepted = rawRepo.publish(higher);
    // welshman accepts same-timestamp events (last-write-wins), so
    // the higher-id event would have been accepted
    expect(accepted).toBe(true);
    // And the repository now holds the higher-id event (wrong per NIP-01)
    const rawResults = rawRepo.query([{ kinds: [30900], '#d': ['svc:tiebreak-guard'] }]);
    expect(rawResults[0].id).toBe(higher.id);

    await store2.close();
  });

  it('ingestion allows lower-id event to replace higher-id at same timestamp', async () => {
    const prefix = uniquePrefix(alice.pk);
    const store2 = createBahiaEventStore({ servicePubkeyPrefix: prefix });
    await store2.open();

    const ts = 1700000000;
    const e1 = signedEvent(alice.sk, {
      kind: 30900,
      content: '{"v":"first"}',
      tags: [['d', 'svc:tiebreak-replace']],
      created_at: ts,
    });
    const e2 = signedEvent(alice.sk, {
      kind: 30900,
      content: '{"v":"second"}',
      tags: [['d', 'svc:tiebreak-replace']],
      created_at: ts,
    });

    const [lower, higher] = e1.id < e2.id ? [e1, e2] : [e2, e1];

    // Ingest the higher-id event first
    expect(store2.ingest(higher)).toBe(true);

    // Now ingest the lower-id event — should replace the higher-id one
    expect(store2.ingest(lower)).toBe(true);

    const results = store2.query({ kinds: [30900], '#d': ['svc:tiebreak-replace'] });
    expect(results).toHaveLength(1);
    expect(results[0].id).toBe(lower.id);

    await store2.close();
  });
});
