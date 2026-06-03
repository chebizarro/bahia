import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

const nostrMock = vi.hoisted(() => ({
  publish: vi.fn(),
  subscribe: vi.fn(),
  getConnectedRelays: vi.fn()
}));

const authMock = vi.hoisted(() => ({
  authState: { status: 'authenticated', pubkey: 'a'.repeat(64) },
  signWithAuth: vi.fn()
}));

const systemMock = vi.hoisted(() => ({
  currentSystemInfo: vi.fn(() => ({ nostr: { service_pubkey: 'b'.repeat(64) } }))
}));

vi.mock('$lib/stores/auth.js', () => authMock);
vi.mock('$lib/stores/system.svelte.js', () => systemMock);

vi.mock('../../src/lib/nostr/client.js', async () => {
  const actual = await vi.importActual('../../src/lib/nostr/client.js');
  return {
    ...actual,
    nostr: nostrMock
  };
});

describe('controlplane request helpers', () => {
  let helper;
  let originalWebSocket;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    originalWebSocket = global.WebSocket;
    global.WebSocket = { OPEN: 1 };
    authMock.authState.status = 'authenticated';
    authMock.authState.pubkey = 'a'.repeat(64);
    authMock.signWithAuth.mockImplementation(async (event) => ({
      ...event,
      id: 'request-event-id',
      pubkey: authMock.authState.pubkey,
      sig: 'sig'
    }));
    nostrMock.publish.mockResolvedValue([
      { relay: 'ws://relay-1.test', sent: true, accepted: true, message: '' },
      { relay: 'ws://relay-2.test', sent: true, accepted: false, message: 'blocked: duplicate' }
    ]);
    nostrMock.subscribe.mockReturnValue(vi.fn());
    nostrMock.getConnectedRelays.mockReturnValue(null);
    systemMock.currentSystemInfo.mockReturnValue({ nostr: { service_pubkey: 'b'.repeat(64) } });
    helper = await import('../../src/lib/nostr/controlplane-requests.js');
  });

  afterEach(() => {
    global.WebSocket = originalWebSocket;
  });

  it('signs and publishes a request, requiring at least one OK accepted relay', async () => {
    const result = await helper.publishRequest({
      kind: 25910,
      tags: [['t', 'task-1'], ['p', 'b'.repeat(64)]],
      content: { service_id: 'svc-1' }
    });

    expect(authMock.signWithAuth).toHaveBeenCalledWith(expect.objectContaining({
      kind: 25910,
      tags: [['t', 'task-1'], ['p', 'b'.repeat(64)]],
      content: JSON.stringify({ service_id: 'svc-1' })
    }));
    expect(nostrMock.publish).toHaveBeenCalledWith(expect.objectContaining({ id: 'request-event-id' }));
    expect(result.requestEventId).toBe('request-event-id');
    expect(result.acceptedRelays).toHaveLength(1);
    expect(result.rejectedRelays).toHaveLength(1);
  });

  it('rejects publish when all relays return OK false or no accepted result', async () => {
    nostrMock.publish.mockResolvedValueOnce([
      { relay: 'ws://relay.test', sent: true, accepted: false, message: 'auth-required: sign in' }
    ]);

    await expect(helper.publishRequest({ kind: 25910 })).rejects.toThrow('auth-required');
  });

  it('requestResult subscribes for terminal results before publishing and preserves OK metadata', async () => {
    const order = [];
    let handlers;
    const unsubscribe = vi.fn();
    nostrMock.subscribe.mockImplementation((_filters, nextHandlers) => {
      order.push('subscribe');
      handlers = nextHandlers;
      return unsubscribe;
    });
    nostrMock.publish.mockImplementation(async (event) => {
      order.push('publish');
      handlers.onEvent({
        id: 'terminal-1',
        kind: 25910,
        pubkey: 'b'.repeat(64),
        tags: [['e', event.id]],
        content: '{"status":"ok"}'
      });
      return [{ relay: 'ws://relay.test', sent: true, accepted: true, message: 'duplicate: already have this event' }];
    });

    await expect(helper.requestResult({ kind: 25910, content: { service_id: 'svc-1' }, resultKinds: [25910] }))
      .resolves.toMatchObject({
        requestEventId: 'request-event-id',
        resultEvent: { id: 'terminal-1', kind: 25910 },
        acceptedRelays: [{ relay: 'ws://relay.test', sent: true, accepted: true, message: 'duplicate: already have this event' }]
      });
    expect(order).toEqual(['subscribe', 'publish']);
    expect(unsubscribe).toHaveBeenCalledTimes(1);
  });

  it('awaitResult rejects explicit aborts without timeout-based completion', async () => {
    const unsubscribe = vi.fn();
    nostrMock.subscribe.mockReturnValueOnce(unsubscribe);
    const controller = new AbortController();

    const promise = helper.awaitResult({ requestEventId: 'req-1', resultKinds: [25910], signal: controller.signal });
    controller.abort(new Error('operator cancelled result wait'));

    await expect(promise).rejects.toThrow('operator cancelled result wait');
    expect(unsubscribe).toHaveBeenCalledTimes(1);
  });

  it('awaitResult resolves from a correlated event and unsubscribes deterministically', async () => {
    let handlers;
    const unsubscribe = vi.fn();
    nostrMock.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
      return unsubscribe;
    });

    const promise = helper.awaitResult({ requestEventId: 'req-1', resultKinds: [25910] });
    handlers.onEvent({ id: 'other', kind: 25910, pubkey: 'b'.repeat(64), tags: [['e', 'other-req']], content: '{}' });
    handlers.onEvent({ id: 'spoofed', kind: 25910, pubkey: 'c'.repeat(64), tags: [['e', 'req-1']], content: '{}' });
    handlers.onEvent({ id: 'result-1', kind: 25910, pubkey: 'b'.repeat(64), tags: [['e', 'req-1']], content: '{"ok":true}' });
    handlers.onEvent({ id: 'result-1', kind: 25910, pubkey: 'b'.repeat(64), tags: [['e', 'req-1']], content: '{"ok":true}' });

    await expect(promise).resolves.toMatchObject({ id: 'result-1' });
    expect(unsubscribe).toHaveBeenCalledTimes(1);
    expect(nostrMock.subscribe).toHaveBeenCalledWith(
      [{ kinds: [25910], '#e': ['req-1'], authors: ['b'.repeat(64)] }],
      expect.objectContaining({ onEvent: expect.any(Function), onClosed: expect.any(Function) })
    );
  });

  it('awaitResult reports auth-related subscription closures distinctly', async () => {
    let handlers;
    const unsubscribe = vi.fn();
    nostrMock.getConnectedRelays.mockReturnValue(['ws://relay-auth.test']);
    nostrMock.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
      return unsubscribe;
    });

    const promise = helper.awaitResult({ requestEventId: 'req-1', resultKinds: [25910] });
    handlers.onClosed('auth-required: sign in', 'ws://relay-auth.test');

    await expect(promise).rejects.toThrow('Nostr result subscription auth closure: ws://relay-auth.test: auth-required: sign in');
    expect(unsubscribe).toHaveBeenCalledTimes(1);
  });

  it('awaitResult aggregates relay close reasons when all result subscriptions close incomplete', async () => {
    let handlers;
    nostrMock.getConnectedRelays.mockReturnValue(['ws://relay-1.test', 'ws://relay-2.test']);
    nostrMock.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
      return vi.fn();
    });

    const promise = helper.awaitResult({ requestEventId: 'req-1', resultKinds: [25910] });
    handlers.onClosed('closed: shard restarting', 'ws://relay-1.test');
    handlers.onClosed('closed: subscription limit', 'ws://relay-2.test');

    await expect(promise).rejects.toThrow('ws://relay-1.test (closed: shard restarting); ws://relay-2.test (closed: subscription limit)');
  });

  it('subscribeStatus streams only matching status events and dedupes duplicates', () => {
    let handlers;
    nostrMock.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
      return vi.fn();
    });
    const onStatus = vi.fn();

    helper.subscribeStatus({ requestEventId: 'req-1', statusKinds: [30315], onStatus });
    const status = { id: 'status-1', kind: 30315, pubkey: 'b'.repeat(64), tags: [['e', 'req-1']], content: '{}' };
    handlers.onEvent({ id: 'status-other', kind: 30315, pubkey: 'b'.repeat(64), tags: [['e', 'req-2']], content: '{}' }, 'relay');
    handlers.onEvent({ id: 'status-spoofed', kind: 30315, pubkey: 'c'.repeat(64), tags: [['e', 'req-1']], content: '{}' }, 'relay');
    handlers.onEvent(status, 'relay');
    handlers.onEvent(status, 'relay');

    expect(onStatus).toHaveBeenCalledTimes(1);
    expect(onStatus).toHaveBeenCalledWith(status, 'relay');
  });

  it('exposes current authenticated request pubkey', () => {
    expect(helper.currentRequesterPubkey()).toBe('a'.repeat(64));
    authMock.authState.status = 'unauthenticated';
    expect(helper.currentRequesterPubkey()).toBeNull();
  });
});
