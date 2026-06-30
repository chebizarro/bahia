import { createHash } from 'node:crypto';
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { get } from 'svelte/store';
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
  await Promise.resolve();
  await Promise.resolve();
}

describe('browser Nostr trust boundary and subscription completion', () => {
  let createNostrPoolClient;
  let validateInboundNostrEvent;
  let originalWebSocket;

  beforeEach(async () => {
    vi.resetModules();
    originalWebSocket = global.WebSocket;
    global.WebSocket = { OPEN: 1, CONNECTING: 0 };
    const module = await import('../../src/lib/nostr/client.js');
    createNostrPoolClient = module.createNostrPoolClient;
    validateInboundNostrEvent = module.validateInboundNostrEvent;
  });

  afterEach(() => {
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
    const frames = [];
    let resolveEose;
    const eoseSeen = new Promise((resolve) => { resolveEose = resolve; });

    client.subscribe([{ kinds: [30900] }], {
      onEvent: (evt, relayUrl) => frames.push(['event', relayUrl, evt.id]),
      onEose: (relayUrl) => {
        frames.push(['eose', relayUrl]);
        resolveEose();
      }
    });
    await flushPromises();
    relay.subscriptions[0].params.onevent(event);
    relay.subscriptions[0].params.oneose();
    await eoseSeen;

    expect(frames).toEqual([
      ['event', 'wss://relay.example', event.id],
      ['eose', 'wss://relay.example']
    ]);
  });

  it('reports EOSE per connected relay so callers can make catch-up authoritative', async () => {
    const relayA = createRelay('wss://relay-a.example');
    const relayB = createRelay('wss://relay-b.example');
    const client = createNostrPoolClient({ relays: ['wss://relay-a.example', 'wss://relay-b.example'], pool: createPool([relayA, relayB]) });
    const onEose = vi.fn();

    client.subscribe([{ kinds: [30900] }], { onEose });
    await flushPromises();
    relayA.subscriptions[0].params.oneose();
    await flushPromises();

    expect(onEose).toHaveBeenCalledTimes(1);
    expect(onEose).toHaveBeenCalledWith('wss://relay-a.example');

    relayB.subscriptions[0].params.oneose();
    await flushPromises();
    expect(onEose).toHaveBeenCalledTimes(2);
    expect(onEose).toHaveBeenLastCalledWith('wss://relay-b.example');
  });

  it('keeps subscriptions active across transient transport reconnect and delivers EOSE later', async () => {
    const relay = createRelay('wss://relay.example');
    const client = createNostrPoolClient({ relays: ['wss://relay.example'], pool: createPool([relay]) });
    const event = validEvent();
    let resolveEvent;
    let resolveEose;
    const eventSeen = new Promise((resolve) => { resolveEvent = resolve; });
    const eoseSeen = new Promise((resolve) => { resolveEose = resolve; });
    const onEvent = vi.fn((...args) => resolveEvent(args));
    const onEose = vi.fn((...args) => resolveEose(args));

    client.subscribe([{ kinds: [30900] }], { onEvent, onEose });
    await flushPromises();
    relay.subscriptions[0].params.onevent(event);
    await eventSeen;
    expect(onEose).not.toHaveBeenCalled();

    relay.subscriptions[0].params.oneose();
    await eoseSeen;
    expect(onEvent).toHaveBeenCalledWith(event, 'wss://relay.example');
    expect(onEose).toHaveBeenCalledWith('wss://relay.example');
  });

  it('surfaces relay CLOSED before EOSE with terminal metadata while preserving partial events already delivered', async () => {
    const relay = createRelay('wss://relay.example');
    const client = createNostrPoolClient({ relays: ['wss://relay.example'], pool: createPool([relay]) });
    const event = validEvent();
    let resolveEvent;
    let resolveClosed;
    const eventSeen = new Promise((resolve) => { resolveEvent = resolve; });
    const closedSeen = new Promise((resolve) => { resolveClosed = resolve; });
    const onEvent = vi.fn((...args) => resolveEvent(args));
    const onClosed = vi.fn((...args) => resolveClosed(args));

    client.subscribe([{ kinds: [30900] }], { onEvent, onClosed });
    await flushPromises();
    relay.subscriptions[0].params.onevent(event);
    await eventSeen;
    relay.subscriptions[0].params.onclose('closed: relay shutdown');
    await closedSeen;

    expect(onEvent).toHaveBeenCalledWith(event, 'wss://relay.example');
    expect(onClosed).toHaveBeenCalledWith('closed: relay shutdown', 'wss://relay.example', {
      terminal: true,
      source: 'closed',
      authRequired: false
    });
  });

  it('classifies NIP-42 AUTH-required closures and marks relay status', async () => {
    const relay = createRelay('wss://auth.example');
    const client = createNostrPoolClient({ relays: ['wss://auth.example'], pool: createPool([relay]) });
    const onClosed = vi.fn();

    client.subscribe([{ kinds: [30900] }], { onClosed });
    await flushPromises();
    relay.subscriptions[0].params.onclose('auth-required: sign in first');
    await flushPromises();

    expect(onClosed).toHaveBeenCalledWith('auth-required: sign in first', 'wss://auth.example', {
      terminal: true,
      source: 'auth',
      authRequired: true
    });
    expect(get(client.connectionStatus)['wss://auth.example']).toBe('auth-required');
  });

  it('does not classify non-prefix auth-required text as NIP-42 AUTH', async () => {
    const relay = createRelay('wss://relay.example');
    const client = createNostrPoolClient({ relays: ['wss://relay.example'], pool: createPool([relay]) });
    const onClosed = vi.fn();

    client.subscribe([{ kinds: [30900] }], { onClosed });
    await flushPromises();
    relay.subscriptions[0].params.onclose('closed: not auth-required; maintenance');
    await flushPromises();

    expect(onClosed).toHaveBeenCalledWith('closed: not auth-required; maintenance', 'wss://relay.example', {
      terminal: true,
      source: 'closed',
      authRequired: false
    });
    expect(get(client.connectionStatus)['wss://relay.example']).toBe('connected');
  });
});
