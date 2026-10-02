/**
 * Tests for pool-welshman.js (W1-S1).
 *
 * Uses welshman's MockAdapter to simulate relay behaviour without a
 * real WebSocket.
 *
 * Covers:
 * - Ref-counted REQs (two consumers share one REQ; last unsub closes it)
 * - since=cursor on (re)subscribe
 * - Cursor commit on EOSE / live events
 * - Per-relay OK tracking for publish
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import 'fake-indexeddb/auto';
import { generateSecretKey, getPublicKey, finalizeEvent } from 'nostr-tools';
import { MockAdapter } from '@welshman/net';
import { createBahiaPool } from '../../src/lib/nostr/pool-welshman.js';
import { createBahiaEventStore } from '../../src/lib/nostr/store.js';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function keygen() {
  const sk = generateSecretKey();
  return { sk, pk: getPublicKey(sk) };
}

function signedEvent(sk, template) {
  return finalizeEvent({
    kind: template.kind ?? 1,
    content: template.content ?? '',
    tags: template.tags ?? [],
    created_at: template.created_at ?? Math.floor(Date.now() / 1000),
  }, sk);
}

const alice = keygen();
let testCounter = 100;
function uniquePrefix() {
  testCounter++;
  return alice.pk.slice(0, 6) + String(testCounter).padStart(2, '0');
}

const RELAY_URL = 'wss://test-relay.example';

/**
 * Create a MockAdapter-backed pool + store for testing.
 *
 * Returns { pool, store, adapter, sentMessages }.
 * `sentMessages` is an array of all ClientMessages sent to the adapter.
 * `adapter.receive(message)` simulates a relay sending a message back.
 */
async function createTestPool() {
  const prefix = uniquePrefix();
  const store = createBahiaEventStore({ servicePubkeyPrefix: prefix });
  await store.open();

  const sentMessages = [];

  /** @type {MockAdapter | null} */
  let mockAdapter = null;

  const pool = createBahiaPool({
    store,
    context: {
      getAdapter: (url) => {
        mockAdapter = new MockAdapter(url, (msg) => {
          sentMessages.push(msg);
        });
        return mockAdapter;
      },
    },
  });

  return {
    pool,
    store,
    get adapter() { return mockAdapter; },
    sentMessages,
    async cleanup() {
      pool.destroy();
      await store.close();
    },
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('pool-welshman', () => {
  /** @type {Awaited<ReturnType<typeof createTestPool>>} */
  let ctx;

  beforeEach(async () => {
    ctx = await createTestPool();
  });

  afterEach(async () => {
    await ctx.cleanup();
  });

  // ── Ref-counted REQs ──────────────────────────────────────────────

  describe('ref-counted subscriptions', () => {
    it('creates a subscription that can be unsubscribed', async () => {
      const handle = ctx.pool.subscribe({
        relays: [RELAY_URL],
        filters: [{ kinds: [1] }],
      });

      expect(handle.id).toBeTruthy();
      expect(ctx.pool.getSubscriptionCount()).toBe(1);

      handle.unsubscribe();
      expect(ctx.pool.getSubscriptionCount()).toBe(0);
    });

    it('two refs keep the subscription alive; last unsub closes it', () => {
      const handle1 = ctx.pool.subscribe({
        relays: [RELAY_URL],
        filters: [{ kinds: [30900] }],
      });

      // Add a second reference to the same subscription
      const handle2 = ctx.pool.addRef(handle1.id);
      expect(handle2).not.toBeNull();

      const sub = ctx.pool.getSubscription(handle1.id);
      expect(sub.refCount).toBe(2);

      // First unsub decrements but keeps alive
      handle1.unsubscribe();
      const subAfter1 = ctx.pool.getSubscription(handle1.id);
      expect(subAfter1).toBeDefined();
      expect(subAfter1.refCount).toBe(1);

      // Second unsub actually closes it
      handle2.unsubscribe();
      expect(ctx.pool.getSubscription(handle1.id)).toBeUndefined();
      expect(ctx.pool.getSubscriptionCount()).toBe(0);
    });

    it('addRef returns null for non-existent subscription', () => {
      expect(ctx.pool.addRef('does-not-exist')).toBeNull();
    });
  });

  // ── since=cursor on subscribe ─────────────────────────────────────

  describe('cursor-based since', () => {
    it('applies stored cursor as since on subscribe', async () => {
      // Pre-set a cursor
      ctx.store.setCursor(RELAY_URL, 'my-filter', 1700000500);

      // Wait for fire-and-forget write
      await new Promise(r => setTimeout(r, 20));

      const handle = ctx.pool.subscribe({
        relays: [RELAY_URL],
        filters: [{ kinds: [30900] }],
        filterKey: 'my-filter',
      });

      // Wait for the async request to fire
      await new Promise(r => setTimeout(r, 20));

      // The REQ message should have since=1700000500
      const reqMsg = ctx.sentMessages.find(m => m[0] === 'REQ');
      expect(reqMsg).toBeDefined();
      // REQ message format: ["REQ", subId, filter1, filter2, ...]
      const filter = reqMsg[2];
      expect(filter.since).toBe(1700000500);

      handle.unsubscribe();
    });

    it('uses filter.since when no cursor is stored', async () => {
      const handle = ctx.pool.subscribe({
        relays: [RELAY_URL],
        filters: [{ kinds: [30900], since: 1700000000 }],
        filterKey: 'no-cursor',
      });

      await new Promise(r => setTimeout(r, 20));

      const reqMsg = ctx.sentMessages.find(m => m[0] === 'REQ');
      expect(reqMsg).toBeDefined();
      const filter = reqMsg[2];
      expect(filter.since).toBe(1700000000);

      handle.unsubscribe();
    });
  });

  // ── Cursor commit on EOSE / live events ───────────────────────────

  describe('cursor commit', () => {
    it('updates cursor on live events and commits on EOSE', async () => {
      const eoseReceived = [];
      const handle = ctx.pool.subscribe({
        relays: [RELAY_URL],
        filters: [{ kinds: [1] }],
        filterKey: 'live-test',
        onEose: (url) => eoseReceived.push(url),
      });

      await new Promise(r => setTimeout(r, 20));

      // Find the subscription id from the REQ message
      const reqMsg = ctx.sentMessages.find(m => m[0] === 'REQ');
      expect(reqMsg).toBeDefined();
      const subId = reqMsg[1];

      // Simulate relay sending an event
      const event1 = signedEvent(alice.sk, {
        kind: 1,
        content: 'hello from relay',
        created_at: 1700000100,
      });
      ctx.adapter.receive(['EVENT', subId, event1]);

      await new Promise(r => setTimeout(r, 20));

      // Cursor should be updated to the event's timestamp
      expect(ctx.store.getCursor(RELAY_URL, 'live-test')).toBe(1700000100);

      // Simulate a newer event
      const event2 = signedEvent(alice.sk, {
        kind: 1,
        content: 'hello again',
        created_at: 1700000200,
      });
      ctx.adapter.receive(['EVENT', subId, event2]);

      await new Promise(r => setTimeout(r, 20));

      // Cursor should advance to the newer timestamp
      expect(ctx.store.getCursor(RELAY_URL, 'live-test')).toBe(1700000200);

      // Simulate EOSE
      ctx.adapter.receive(['EOSE', subId]);

      await new Promise(r => setTimeout(r, 20));

      // EOSE callback should have fired
      expect(eoseReceived).toContain(RELAY_URL);

      // Cursor should still be at the highest value
      expect(ctx.store.getCursor(RELAY_URL, 'live-test')).toBe(1700000200);

      handle.unsubscribe();
    });

    it('does not advance cursor for older events', async () => {
      const handle = ctx.pool.subscribe({
        relays: [RELAY_URL],
        filters: [{ kinds: [1] }],
        filterKey: 'no-regress',
      });

      await new Promise(r => setTimeout(r, 20));

      const reqMsg = ctx.sentMessages.find(m => m[0] === 'REQ');
      const subId = reqMsg[1];

      // Send newer event first
      const newer = signedEvent(alice.sk, {
        kind: 1,
        content: 'newer',
        created_at: 1700000300,
      });
      ctx.adapter.receive(['EVENT', subId, newer]);
      await new Promise(r => setTimeout(r, 20));
      expect(ctx.store.getCursor(RELAY_URL, 'no-regress')).toBe(1700000300);

      // Send older event — cursor should not regress
      const older = signedEvent(alice.sk, {
        kind: 1,
        content: 'older',
        created_at: 1700000100,
      });
      ctx.adapter.receive(['EVENT', subId, older]);
      await new Promise(r => setTimeout(r, 20));
      expect(ctx.store.getCursor(RELAY_URL, 'no-regress')).toBe(1700000300);

      handle.unsubscribe();
    });
  });

  // ── Per-relay OK tracking for publish ─────────────────────────────

  describe('publish with per-relay OK', () => {
    it('reports success when relay sends OK true', async () => {
      const event = signedEvent(alice.sk, { kind: 1, content: 'publish me' });

      // Start the publish (it will wait for OK)
      const publishPromise = ctx.pool.publishEvent({
        event,
        relays: [RELAY_URL],
        timeout: 5000,
      });

      // Wait for the EVENT message to be sent
      await new Promise(r => setTimeout(r, 20));

      const eventMsg = ctx.sentMessages.find(m => m[0] === 'EVENT');
      expect(eventMsg).toBeDefined();
      expect(eventMsg[1].id).toBe(event.id);

      // Simulate relay OK response
      ctx.adapter.receive(['OK', event.id, true, '']);

      const results = await publishPromise;
      expect(results[RELAY_URL]).toBeDefined();
      expect(results[RELAY_URL].status).toBe('success');
    });

    it('reports failure when relay sends OK false', async () => {
      const event = signedEvent(alice.sk, { kind: 1, content: 'will fail' });

      const publishPromise = ctx.pool.publishEvent({
        event,
        relays: [RELAY_URL],
        timeout: 5000,
      });

      await new Promise(r => setTimeout(r, 20));

      // Simulate relay rejection
      ctx.adapter.receive(['OK', event.id, false, 'blocked: spam']);

      const results = await publishPromise;
      expect(results[RELAY_URL]).toBeDefined();
      expect(results[RELAY_URL].status).toBe('failure');
      expect(results[RELAY_URL].detail).toContain('blocked');
    });

    it('reports timeout when relay does not respond', async () => {
      const event = signedEvent(alice.sk, { kind: 1, content: 'timeout' });

      const results = await ctx.pool.publishEvent({
        event,
        relays: [RELAY_URL],
        timeout: 50, // very short timeout
      });

      expect(results[RELAY_URL]).toBeDefined();
      expect(results[RELAY_URL].status).toBe('timeout');
    });
  });
});
