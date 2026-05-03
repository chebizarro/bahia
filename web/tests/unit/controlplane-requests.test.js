import { describe, it, expect, beforeEach, vi } from 'vitest';

const nostrMock = vi.hoisted(() => ({
  publish: vi.fn(),
  subscribe: vi.fn()
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

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
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
    systemMock.currentSystemInfo.mockReturnValue({ nostr: { service_pubkey: 'b'.repeat(64) } });
    helper = await import('../../src/lib/nostr/controlplane-requests.js');
  });

  it('signs and publishes a request, requiring at least one OK accepted relay', async () => {
    const result = await helper.publishRequest({
      kind: 5964,
      tags: [['t', 'task-1'], ['p', 'b'.repeat(64)]],
      content: { service_id: 'svc-1' }
    });

    expect(authMock.signWithAuth).toHaveBeenCalledWith(expect.objectContaining({
      kind: 5964,
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

    await expect(helper.publishRequest({ kind: 5964 })).rejects.toThrow('auth-required');
  });

  it('awaitResult resolves from a correlated event and unsubscribes deterministically', async () => {
    let handlers;
    const unsubscribe = vi.fn();
    nostrMock.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
      return unsubscribe;
    });

    const promise = helper.awaitResult({ requestEventId: 'req-1', resultKinds: [7963] });
    handlers.onEvent({ id: 'other', kind: 7963, pubkey: 'b'.repeat(64), tags: [['e', 'other-req']], content: '{}' });
    handlers.onEvent({ id: 'spoofed', kind: 7963, pubkey: 'c'.repeat(64), tags: [['e', 'req-1']], content: '{}' });
    handlers.onEvent({ id: 'result-1', kind: 7963, pubkey: 'b'.repeat(64), tags: [['e', 'req-1']], content: '{"ok":true}' });
    handlers.onEvent({ id: 'result-1', kind: 7963, pubkey: 'b'.repeat(64), tags: [['e', 'req-1']], content: '{"ok":true}' });

    await expect(promise).resolves.toMatchObject({ id: 'result-1' });
    expect(unsubscribe).toHaveBeenCalledTimes(1);
    expect(nostrMock.subscribe).toHaveBeenCalledWith(
      [{ kinds: [7963], '#e': ['req-1'], authors: ['b'.repeat(64)] }],
      expect.objectContaining({ onEvent: expect.any(Function), onClosed: expect.any(Function) })
    );
  });

  it('subscribeStatus streams only matching status events and dedupes duplicates', () => {
    let handlers;
    nostrMock.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
      return vi.fn();
    });
    const onStatus = vi.fn();

    helper.subscribeStatus({ requestEventId: 'req-1', statusKinds: [6962], onStatus });
    const status = { id: 'status-1', kind: 6962, pubkey: 'b'.repeat(64), tags: [['e', 'req-1']], content: '{}' };
    handlers.onEvent({ id: 'status-other', kind: 6962, pubkey: 'b'.repeat(64), tags: [['e', 'req-2']], content: '{}' }, 'relay');
    handlers.onEvent({ id: 'status-spoofed', kind: 6962, pubkey: 'c'.repeat(64), tags: [['e', 'req-1']], content: '{}' }, 'relay');
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
