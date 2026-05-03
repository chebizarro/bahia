import { describe, it, expect, beforeEach, vi } from 'vitest';

const authMock = vi.hoisted(() => ({
  authState: { status: 'authenticated', pubkey: 'a'.repeat(64) },
  encryptWithAuth: vi.fn(),
  decryptWithAuth: vi.fn(),
  signWithAuth: vi.fn()
}));

const systemMock = vi.hoisted(() => ({
  currentSystemInfo: vi.fn(() => ({
    nostr: {
      service_pubkey: 'b'.repeat(64),
      private_browser_relays: ['wss://private.example'],
      browser_relays: ['wss://public.example']
    }
  }))
}));

vi.mock('$lib/stores/auth.js', () => authMock);
vi.mock('$lib/stores/system.svelte.js', () => systemMock);

function fakeClient() {
  return {
    sockets: new Map([['wss://private.example', { readyState: 1 }]]),
    connect: vi.fn().mockResolvedValue(),
    publish: vi.fn().mockResolvedValue([{ relay: 'wss://private.example', sent: true, accepted: true, message: '' }]),
    subscribe: vi.fn(() => vi.fn())
  };
}

describe('private controlplane transport', () => {
  let module;
  let client;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    authMock.authState.status = 'authenticated';
    authMock.authState.pubkey = 'a'.repeat(64);
    authMock.encryptWithAuth.mockImplementation(async (_pubkey, plaintext) => `cipher:${plaintext}`);
    authMock.decryptWithAuth.mockImplementation(async (_pubkey, ciphertext) => ciphertext.replace(/^cipher:/, ''));
    authMock.signWithAuth.mockImplementation(async (event) => ({ ...event, id: 'request-id', pubkey: authMock.authState.pubkey, sig: 'sig' }));
    systemMock.currentSystemInfo.mockReturnValue({
      nostr: {
        service_pubkey: 'b'.repeat(64),
        private_browser_relays: ['wss://private.example'],
        browser_relays: ['wss://public.example']
      }
    });
    client = fakeClient();
    module = await import('../../src/lib/nostr/private-controlplane.js');
  });

  it('discovers only explicit private browser relays', () => {
    expect(module.privateRelayUrlsFromSystemInfo()).toEqual(['wss://private.example']);
    expect(module.privateTransportAvailable()).toBe(true);
  });

  it('builds encrypted request events without targeting public browser relays', async () => {
    const transport = new module.PrivateControlplaneTransport({ client, relays: ['wss://private.example'], servicePubkey: 'b'.repeat(64) });

    const event = await transport.buildPrivateRequestEvent({ operation: 'payments.history', payload: { limit: 10 } });

    expect(authMock.encryptWithAuth).toHaveBeenCalledWith('b'.repeat(64), expect.stringContaining('payments.history'));
    expect(authMock.signWithAuth).toHaveBeenCalledWith(expect.objectContaining({
      kind: module.PRIVATE_REQUEST_KIND,
      tags: expect.arrayContaining([['p', 'b'.repeat(64)], ['private', module.PRIVATE_TRANSPORT_VERSION]]),
      content: expect.stringMatching(/^cipher:/)
    }));
    expect(event.id).toBe('request-id');
  });

  it('publishes only through the private client and requires an accepted OK', async () => {
    const transport = new module.PrivateControlplaneTransport({ client, relays: ['wss://private.example'], servicePubkey: 'b'.repeat(64) });
    const event = { id: 'request-id', kind: module.PRIVATE_REQUEST_KIND, tags: [], content: 'cipher' };

    await expect(transport.publishPrivateRequest(event)).resolves.toMatchObject({ requestEventId: 'request-id' });
    expect(client.connect).toHaveBeenCalledWith(['wss://private.example']);
    expect(client.publish).toHaveBeenCalledWith(event);

    client.publish.mockResolvedValueOnce([{ relay: 'wss://private.example', sent: true, accepted: false, message: 'blocked: no' }]);
    await expect(transport.publishPrivateRequest(event)).rejects.toThrow('blocked: no');
  });

  it('awaitPrivateResult resolves only correlated encrypted results and unsubscribes', async () => {
    let handlers;
    const unsubscribe = vi.fn();
    client.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
      return unsubscribe;
    });
    const transport = new module.PrivateControlplaneTransport({ client, relays: ['wss://private.example'], servicePubkey: 'b'.repeat(64) });

    const promise = transport.awaitPrivateResult({ requestEventId: 'req-1' });
    await handlers.onEvent({ id: 'other', pubkey: 'b'.repeat(64), tags: [['e', 'other'], ['p', 'a'.repeat(64)]], content: 'cipher:{}' });
    await handlers.onEvent({ id: 'spoofed', pubkey: 'c'.repeat(64), tags: [['e', 'req-1'], ['p', 'a'.repeat(64)]], content: 'cipher:{}' });
    await handlers.onEvent({ id: 'result-1', pubkey: 'b'.repeat(64), tags: [['e', 'req-1'], ['p', 'a'.repeat(64)]], content: 'cipher:{"request_event_id":"req-1","status":"ok","payload":{"count":1}}' });
    await handlers.onEvent({ id: 'result-1', pubkey: 'b'.repeat(64), tags: [['e', 'req-1'], ['p', 'a'.repeat(64)]], content: 'cipher:{}' });

    await expect(promise).resolves.toMatchObject({ payload: { status: 'ok', payload: { count: 1 } } });
    expect(unsubscribe).toHaveBeenCalledTimes(1);
    expect(client.subscribe).toHaveBeenCalledWith(
      [{ kinds: [module.PRIVATE_RESULT_KIND], '#e': ['req-1'], '#p': ['a'.repeat(64)], authors: ['b'.repeat(64)] }],
      expect.objectContaining({ onEvent: expect.any(Function), onClosed: expect.any(Function) })
    );
  });

  it('cleans up result subscription when publish fails', async () => {
    const unsubscribe = vi.fn();
    client.subscribe.mockReturnValueOnce(unsubscribe);
    client.publish.mockResolvedValueOnce([{ relay: 'wss://private.example', sent: true, accepted: false, message: 'blocked: no' }]);
    const transport = new module.PrivateControlplaneTransport({ client, relays: ['wss://private.example'], servicePubkey: 'b'.repeat(64) });

    await expect(transport.requestPrivateResult({ operation: 'payments.history', payload: { limit: 5 } })).rejects.toThrow('blocked: no');

    expect(unsubscribe).toHaveBeenCalledTimes(1);
  });

  it('rejects on decrypt failures for correlated result events', async () => {
    let handlers;
    client.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
      return vi.fn();
    });
    authMock.decryptWithAuth.mockRejectedValueOnce(new Error('bad ciphertext'));
    const transport = new module.PrivateControlplaneTransport({ client, relays: ['wss://private.example'], servicePubkey: 'b'.repeat(64) });

    const promise = transport.awaitPrivateResult({ requestEventId: 'req-1' });
    await handlers.onEvent({ id: 'result-1', pubkey: 'b'.repeat(64), tags: [['e', 'req-1'], ['p', 'a'.repeat(64)]], content: 'not-decryptable' });

    await expect(promise).rejects.toThrow('bad ciphertext');
  });
});
