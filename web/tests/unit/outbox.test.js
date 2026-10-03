import 'fake-indexeddb/auto';
import { describe, expect, it, vi } from 'vitest';
import { createIntentOutbox } from '../../src/lib/nostr/outbox.js';

let sequence = 0;
const event = { id: 'signed-event', sig: 'signature' };
function fixture() {
  const connections = new Set();
  const publishEvent = vi.fn(async ({ relays }) => ({ [relays[0]]: { status: 'success' } }));
  let ready;
  const pool = { publishEvent, getConnectedRelays: () => [...connections],
    onRelayReady: callback => { ready = callback; return () => { ready = null; }; } };
  const changes = [];
  const waiters = [];
  const outbox = createIntentOutbox({ namespace: `test-${++sequence}`, pool, relays: ['relay-a', 'relay-b'],
    onStateChange: entry => {
      changes.push(structuredClone(entry));
      for (const waiter of [...waiters]) if (waiter.predicate(entry)) {
        waiters.splice(waiters.indexOf(waiter), 1); waiter.resolve();
      }
    } });
  const until = predicate => new Promise(resolve => waiters.push({ predicate, resolve }));
  return { outbox, pool, connections, publishEvent, changes, until, reconnect: relay => ready({ relay }) };
}

describe('intent outbox', () => {
  it('does not fail or retry while disconnected; re-sends only on reconnect', async () => {
    const ctx = fixture(); await ctx.outbox.open();
    await ctx.outbox.enqueue(event);
    expect(ctx.publishEvent).not.toHaveBeenCalled();
    expect(ctx.outbox.get(event.id).state).toBe('pending');
    const published = ctx.until(entry => entry.state === 'published');
    ctx.connections.add('relay-a'); ctx.reconnect('relay-a'); await published;
    expect(ctx.publishEvent).toHaveBeenCalledTimes(1);
    expect(ctx.outbox.get(event.id).state).toBe('published');
    ctx.outbox.close();
  });

  it('fails only after every relay permanently returns OK=false', async () => {
    const ctx = fixture(); ctx.connections.add('relay-a'); ctx.connections.add('relay-b');
    ctx.pool.publishEvent = vi.fn(async ({ relays }) => ({ [relays[0]]: { status: 'failure', detail: 'blocked: policy' } }));
    await ctx.outbox.open();
    const failed = ctx.until(entry => entry.state === 'failed');
    await ctx.outbox.enqueue(event); await failed;
    expect(ctx.outbox.get(event.id).state).toBe('failed');
    ctx.outbox.close();
  });

  it('waits for AUTH after auth-required OK=false', async () => {
    const ctx = fixture(); ctx.connections.add('relay-a');
    ctx.pool.publishEvent = vi.fn()
      .mockResolvedValueOnce({ 'relay-a': { status: 'failure', detail: 'auth-required: sign in' } })
      .mockResolvedValue({ 'relay-a': { status: 'success' } });
    await ctx.outbox.open();
    const authRequired = ctx.until(entry => entry.relays['relay-a']?.state === 'auth-required');
    await ctx.outbox.enqueue(event); await authRequired;
    expect(ctx.pool.publishEvent).toHaveBeenCalledTimes(1);
    ctx.reconnect('relay-a');
    expect(ctx.pool.publishEvent).toHaveBeenCalledTimes(1);
    const published = ctx.until(entry => entry.state === 'published');
    ctx.outbox.onAuth('relay-a'); await published;
    expect(ctx.pool.publishEvent).toHaveBeenCalledTimes(2);
    ctx.outbox.close();
  });
});
