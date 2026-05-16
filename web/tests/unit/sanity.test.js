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
    kind: 31962,
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

function openSocket() {
  return { readyState: WebSocket.OPEN, send: vi.fn() };
}

describe('browser Nostr trust boundary and query completion', () => {
  let NostrClient;
  let NostrIncompleteEOSEError;
  let validateInboundNostrEvent;
  let originalWebSocket;

  beforeEach(async () => {
    vi.resetModules();
    originalWebSocket = global.WebSocket;
    global.WebSocket = { OPEN: 1, CONNECTING: 0 };
    const module = await import('../../src/lib/nostr/client.js');
    NostrClient = module.NostrClient;
    NostrIncompleteEOSEError = module.NostrIncompleteEOSEError;
    validateInboundNostrEvent = module.validateInboundNostrEvent;
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    global.WebSocket = originalWebSocket;
  });

  it('validates NIP-01 event hashes before browser consumers receive relay EVENT frames', async () => {
    const client = new NostrClient({ relays: [] });
    const socket = openSocket();
    client.sockets.set('wss://relay.example', socket);
    const onEvent = vi.fn();
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});

    client.subscribe([{ kinds: [31962] }], { onEvent });
    const good = validEvent();
    await client.handleMessage('wss://relay.example', JSON.stringify(['EVENT', 'sub_1', good]));
    await client.handleMessage('wss://relay.example', JSON.stringify(['EVENT', 'sub_1', { ...good, id: 'c'.repeat(64) }]));

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
    const client = new NostrClient({ relays: [] });
    client.sockets.set('wss://relay.example', openSocket());
    const event = validEvent();

    const query = client.queryUntilEose([{ kinds: [31962] }]);
    const eventFrame = client.handleMessage('wss://relay.example', JSON.stringify(['EVENT', 'sub_1', event]));
    const eoseFrame = client.handleMessage('wss://relay.example', JSON.stringify(['EOSE', 'sub_1']));

    await Promise.all([eventFrame, eoseFrame]);
    await expect(query).resolves.toEqual([event]);
  });

  it('resolves historical queries only after every connected relay sends EOSE', async () => {
    const client = new NostrClient({ relays: [] });
    client.sockets.set('wss://relay-a.example', openSocket());
    client.sockets.set('wss://relay-b.example', openSocket());
    const event = validEvent();

    const query = client.queryUntilEose([{ kinds: [31962] }]);
    await client.handleMessage('wss://relay-a.example', JSON.stringify(['EVENT', 'sub_1', event]));
    await client.handleMessage('wss://relay-a.example', JSON.stringify(['EOSE', 'sub_1']));

    let settled = false;
    query.then(() => { settled = true; }, () => { settled = true; });
    await Promise.resolve();
    expect(settled).toBe(false);

    await client.handleMessage('wss://relay-b.example', JSON.stringify(['EOSE', 'sub_1']));
    await expect(query).resolves.toEqual([event]);
  });

  it('treats query timeouts as incomplete history instead of successful partial results', async () => {
    vi.useFakeTimers();
    const client = new NostrClient({ relays: [] });
    client.sockets.set('wss://relay.example', openSocket());

    const query = client.query([{ kinds: [31962] }], 1000);
    const assertion = expect(query).rejects.toMatchObject({
      name: 'NostrIncompleteEOSEError',
      reason: 'timeout',
      partialEvents: []
    });

    await vi.advanceTimersByTimeAsync(1000);
    await assertion;
  });

  it('treats aborts as incomplete history with typed reason metadata', async () => {
    const client = new NostrClient({ relays: [] });
    client.sockets.set('wss://relay.example', openSocket());
    const controller = new AbortController();

    const query = client.queryUntilEose([{ kinds: [31962] }], { signal: controller.signal });
    const assertion = expect(query).rejects.toMatchObject({
      name: 'NostrIncompleteEOSEError',
      reason: 'aborted',
      relaySummary: [{ relay: 'wss://relay.example', status: 'pending', reason: '' }]
    });

    controller.abort(new Error('user cancelled'));
    await assertion;
  });

  it('rejects closed-incomplete queries and preserves partial events for callers', async () => {
    const client = new NostrClient({ relays: [] });
    client.sockets.set('wss://relay.example', openSocket());
    const event = validEvent();

    const query = client.queryUntilEose([{ kinds: [31962] }]);
    await client.handleMessage('wss://relay.example', JSON.stringify(['EVENT', 'sub_1', event]));
    client.notifyRelayClosed('wss://relay.example', 'closed: relay shutdown');

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
