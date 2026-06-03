import { createHash } from 'node:crypto';
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { finalizeEvent } from 'nostr-tools';

const TEST_SECRET_KEY = Uint8Array.from(Array.from({ length: 32 }, (_, index) => index + 1));

function eventId(event) {
  return createHash('sha256')
    .update(JSON.stringify([0, event.pubkey, event.created_at, event.kind, event.tags, event.content]))
    .digest('hex');
}

function validEvent(overrides = {}) {
  const unsigned = {
    created_at: Math.floor(Date.now() / 1000),
    kind: 30900,
    tags: [['d', 'svc-1']],
    content: '{}'
  };
  const signed = finalizeEvent({ ...unsigned, ...Object.fromEntries(Object.entries(overrides).filter(([key]) => !['id', 'sig', 'tags'].includes(key))) }, TEST_SECRET_KEY);
  return {
    ...signed,
    ...(Object.hasOwn(overrides, 'tags') ? { tags: overrides.tags } : {}),
    ...(Object.hasOwn(overrides, 'sig') ? { sig: overrides.sig } : {}),
    ...(Object.hasOwn(overrides, 'id') ? { id: overrides.id } : {})
  };
}


function createRelay(url, { connected = true } = {}) {
  const relay = {
    url,
    connected,
    subscriptions: [],
    subscribe: vi.fn((filters, params) => {
      const subscription = { filters, params, close: vi.fn() };
      relay.subscriptions.push(subscription);
      return subscription;
    })
  };
  return relay;
}

function createPool(relays = []) {
  const relayMap = new Map(relays.map((relay) => [relay.url, relay]));
  return {
    ensureRelay: vi.fn(async (url) => {
      if (!relayMap.has(url)) relayMap.set(url, createRelay(url));
      return relayMap.get(url);
    }),
    listConnectionStatus: vi.fn(() => new Map(Array.from(relayMap.entries()).map(([url, relay]) => [url, relay.connected]))),
    publish: vi.fn(() => []),
    close: vi.fn(),
    destroy: vi.fn()
  };
}

async function flushPromises() {
  await Promise.resolve();
  await Promise.resolve();
}

describe('browser Nostr trust boundary and query completion', () => {
  let createNostrPoolClient;
  let NostrIncompleteEOSEError;
  let validateInboundNostrEvent;
  let originalWebSocket;

  beforeEach(async () => {
    vi.resetModules();
    originalWebSocket = global.WebSocket;
    global.WebSocket = { OPEN: 1, CONNECTING: 0 };
    const module = await import('../../src/lib/nostr/client.js');
    createNostrPoolClient = module.createNostrPoolClient;
    NostrIncompleteEOSEError = module.NostrIncompleteEOSEError;
    validateInboundNostrEvent = module.validateInboundNostrEvent;
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    global.WebSocket = originalWebSocket;
  });

  it('validates NIP-01 event hashes before browser consumers receive relay EVENT frames', async () => {
    const relay = createRelay('wss://relay.example');
    const client = createNostrPoolClient({ relays: ['wss://relay.example'], pool: createPool([relay]) });
    let resolveReceived;
    const received = new Promise((resolve) => { resolveReceived = resolve; });
    const onEvent = vi.fn((...args) => resolveReceived(args));
    let resolveWarn;
    const warned = new Promise((resolve) => { resolveWarn = resolve; });
    const warn = vi.spyOn(console, 'warn').mockImplementation((...args) => resolveWarn(args));

    client.subscribe([{ kinds: [30900] }], { onEvent });
    await flushPromises();
    const good = validEvent();
    relay.subscriptions[0].params.onevent(good);
    await received;
    relay.subscriptions[0].params.onevent({ ...good, id: 'c'.repeat(64) });
    await warned;

    expect(onEvent).toHaveBeenCalledTimes(1);
    expect(onEvent).toHaveBeenCalledWith(good, 'wss://relay.example');
    expect(warn).toHaveBeenCalledWith(expect.stringContaining('Dropping invalid EVENT'), expect.stringContaining('event id'));
  });

  it('rejects malformed signatures, timestamps, and non-string tags at the inbound validator', async () => {
    const now = Math.floor(Date.now() / 1000);
    await expect(validateInboundNostrEvent(validEvent({ sig: 'not-hex', created_at: now }), { now }))
      .rejects.toThrow('signature');
    await expect(validateInboundNostrEvent(validEvent({ created_at: now + 3600 }), { now }))
      .rejects.toThrow('future');
    await expect(validateInboundNostrEvent(validEvent({ tags: [['d', 123]], created_at: now }), { now }))
      .rejects.toThrow('arrays of strings');
  });

  it('preserves relay frame order when EVENT validation is followed immediately by EOSE', async () => {
    const relay = createRelay('wss://relay.example');
    const client = createNostrPoolClient({ relays: ['wss://relay.example'], pool: createPool([relay]) });
    const event = validEvent();

    const query = client.queryUntilEose([{ kinds: [30900] }]);
    await flushPromises();
    relay.subscriptions[0].params.onevent(event);
    relay.subscriptions[0].params.oneose();
    await flushPromises();

    await expect(query).resolves.toEqual([event]);
  });

  it('resolves historical queries only after every connected relay sends EOSE', async () => {
    const relayA = createRelay('wss://relay-a.example');
    const relayB = createRelay('wss://relay-b.example');
    const client = createNostrPoolClient({ relays: ['wss://relay-a.example', 'wss://relay-b.example'], pool: createPool([relayA, relayB]) });
    const event = validEvent();

    const query = client.queryUntilEose([{ kinds: [30900] }]);
    await flushPromises();
    relayA.subscriptions[0].params.onevent(event);
    relayA.subscriptions[0].params.oneose();
    await flushPromises();

    let settled = false;
    query.then(() => { settled = true; }, () => { settled = true; });
    await flushPromises();
    expect(settled).toBe(false);

    relayB.subscriptions[0].params.oneose();
    await flushPromises();
    await expect(query).resolves.toEqual([event]);
  });

  it('treats query timeouts as incomplete history instead of successful partial results', async () => {
    vi.useFakeTimers();
    const relay = createRelay('wss://relay.example');
    const client = createNostrPoolClient({ relays: ['wss://relay.example'], pool: createPool([relay]) });

    const query = client.query([{ kinds: [30900] }], 1000);
    const assertion = expect(query).rejects.toMatchObject({
      name: 'NostrIncompleteEOSEError',
      reason: 'timeout',
      partialEvents: []
    });

    await vi.advanceTimersByTimeAsync(1000);
    await assertion;
  });

  it('treats aborts as incomplete history with typed reason metadata', async () => {
    const relay = createRelay('wss://relay.example');
    const client = createNostrPoolClient({ relays: ['wss://relay.example'], pool: createPool([relay]) });
    const controller = new AbortController();

    const query = client.queryUntilEose([{ kinds: [30900] }], { signal: controller.signal });
    const assertion = expect(query).rejects.toMatchObject({
      name: 'NostrIncompleteEOSEError',
      reason: 'aborted',
      relaySummary: [{ relay: 'wss://relay.example', status: 'pending', reason: '' }]
    });

    controller.abort(new Error('user cancelled'));
    await assertion;
  });

  it('keeps EOSE queries active across transient transport reconnect and resolves after EOSE', async () => {
    const relay = createRelay('wss://relay.example');
    const client = createNostrPoolClient({ relays: ['wss://relay.example'], pool: createPool([relay]) });
    const event = validEvent();

    const query = client.queryUntilEose([{ kinds: [30900] }]);
    await flushPromises();
    relay.subscriptions[0].params.onevent(event);

    let settled = false;
    query.then(() => { settled = true; }, () => { settled = true; });
    await flushPromises();
    expect(settled).toBe(false);

    relay.subscriptions[0].params.oneose();
    await flushPromises();
    await expect(query).resolves.toEqual([event]);
  });

  it('rejects relay CLOSED-incomplete queries and preserves partial events for callers', async () => {
    const relay = createRelay('wss://relay.example');
    const client = createNostrPoolClient({ relays: ['wss://relay.example'], pool: createPool([relay]) });
    const event = validEvent();

    const query = client.queryUntilEose([{ kinds: [30900] }]);
    await flushPromises();
    relay.subscriptions[0].params.onevent(event);
    relay.subscriptions[0].params.onclose('closed: relay shutdown');
    await flushPromises();

    await expect(query).rejects.toMatchObject({
      name: 'NostrIncompleteEOSEError',
      reason: 'all_relays_closed',
      partialEvents: [event],
      relaySummary: [{ relay: 'wss://relay.example', status: 'closed', reason: 'closed: relay shutdown' }]
    });
  });

  it('exposes a typed incomplete-query error for fail-closed callers', () => {
    const error = new NostrIncompleteEOSEError('aborted', {
      partialEvents: [{ id: 'partial' }],
      relaySummary: [{ relay: 'wss://relay.example', status: 'pending' }]
    });

    expect(error).toBeInstanceOf(Error);
    expect(error.reason).toBe('aborted');
    expect(error.partialEvents).toEqual([{ id: 'partial' }]);
  });
});
