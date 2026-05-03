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
      encrypted_browser_relays: ['wss://encrypted.example'],
      private_browser_relays: ['wss://deprecated.example'],
      browser_relays: ['wss://public.example']
    }
  }))
}));

vi.mock('$lib/stores/auth.js', () => authMock);
vi.mock('$lib/stores/system.svelte.js', () => systemMock);

function fakeClient() {
  return {
    sockets: new Map([['wss://encrypted.example', { readyState: 1 }]]),
    connect: vi.fn().mockResolvedValue(),
    publish: vi.fn().mockResolvedValue([{ relay: 'wss://encrypted.example', sent: true, accepted: true, message: '' }]),
    subscribe: vi.fn(() => vi.fn())
  };
}

describe('encrypted controlplane transport', () => {
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
        encrypted_browser_relays: ['wss://encrypted.example'],
        private_browser_relays: ['wss://deprecated.example'],
        browser_relays: ['wss://public.example']
      }
    });
    client = fakeClient();
    module = await import('../../src/lib/nostr/encrypted-controlplane.js');
  });

  it('prefers canonical encrypted browser relays and ignores public relays', () => {
    expect(module.encryptedRelayUrlsFromSystemInfo()).toEqual(['wss://encrypted.example']);
    expect(module.encryptedRequestsAvailable()).toBe(true);
  });

  it('falls back to deprecated private browser relay aliases for older system info', () => {
    expect(module.encryptedRelayUrlsFromSystemInfo({
      nostr: {
        service_pubkey: 'b'.repeat(64),
        private_browser_relays: ['wss://legacy.example'],
        browser_relays: ['wss://public.example']
      }
    })).toEqual(['wss://legacy.example']);
  });

  it('builds encrypted request events without targeting public browser relays', async () => {
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://encrypted.example'], servicePubkey: 'b'.repeat(64) });

    const event = await transport.buildEncryptedRequestEvent({ operation: 'payments.history', payload: { limit: 10 } });

    expect(authMock.encryptWithAuth).toHaveBeenCalledWith('b'.repeat(64), expect.stringContaining('payments.history'));
    expect(authMock.signWithAuth).toHaveBeenCalledWith(expect.objectContaining({
      kind: module.ENCRYPTED_REQUEST_KIND,
      tags: expect.arrayContaining([['p', 'b'.repeat(64)], ['private', module.ENCRYPTED_REQUEST_WIRE_VERSION]]),
      content: expect.stringMatching(/^cipher:/)
    }));
    expect(event.id).toBe('request-id');
  });

  it('publishes through the encrypted-request client and requires an accepted OK', async () => {
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://encrypted.example'], servicePubkey: 'b'.repeat(64) });
    const event = { id: 'request-id', kind: module.ENCRYPTED_REQUEST_KIND, tags: [], content: 'cipher' };

    await expect(transport.publishEncryptedRequest(event)).resolves.toMatchObject({ requestEventId: 'request-id' });
    expect(client.connect).toHaveBeenCalledWith(['wss://encrypted.example']);
    expect(client.publish).toHaveBeenCalledWith(event);

    client.publish.mockResolvedValueOnce([{ relay: 'wss://encrypted.example', sent: true, accepted: false, message: 'blocked: no' }]);
    await expect(transport.publishEncryptedRequest(event)).rejects.toThrow('blocked: no');
  });

  it('awaitEncryptedResult resolves only correlated encrypted results and unsubscribes', async () => {
    let handlers;
    const unsubscribe = vi.fn();
    client.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
      return unsubscribe;
    });
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://encrypted.example'], servicePubkey: 'b'.repeat(64) });

    const promise = transport.awaitEncryptedResult({ requestEventId: 'req-1' });
    await handlers.onEvent({ id: 'other', pubkey: 'b'.repeat(64), tags: [['e', 'other'], ['p', 'a'.repeat(64)]], content: 'cipher:{}' });
    await handlers.onEvent({ id: 'spoofed', pubkey: 'c'.repeat(64), tags: [['e', 'req-1'], ['p', 'a'.repeat(64)]], content: 'cipher:{}' });
    await handlers.onEvent({ id: 'result-1', pubkey: 'b'.repeat(64), tags: [['e', 'req-1'], ['p', 'a'.repeat(64)]], content: 'cipher:{"request_event_id":"req-1","status":"ok","payload":{"count":1}}' });
    await handlers.onEvent({ id: 'result-1', pubkey: 'b'.repeat(64), tags: [['e', 'req-1'], ['p', 'a'.repeat(64)]], content: 'cipher:{}' });

    await expect(promise).resolves.toMatchObject({ payload: { status: 'ok', payload: { count: 1 } } });
    expect(unsubscribe).toHaveBeenCalledTimes(1);
    expect(client.subscribe).toHaveBeenCalledWith(
      [{ kinds: [module.ENCRYPTED_RESULT_KIND], '#e': ['req-1'], '#p': ['a'.repeat(64)], authors: ['b'.repeat(64)] }],
      expect.objectContaining({ onEvent: expect.any(Function), onClosed: expect.any(Function) })
    );
  });

  it('cleans up result subscription when publish fails', async () => {
    const unsubscribe = vi.fn();
    client.subscribe.mockReturnValueOnce(unsubscribe);
    client.publish.mockResolvedValueOnce([{ relay: 'wss://encrypted.example', sent: true, accepted: false, message: 'blocked: no' }]);
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://encrypted.example'], servicePubkey: 'b'.repeat(64) });

    await expect(transport.requestEncryptedResult({ operation: 'payments.history', payload: { limit: 5 } })).rejects.toThrow('blocked: no');

    expect(unsubscribe).toHaveBeenCalledTimes(1);
  });

  it('rejects on decrypt failures for correlated result events', async () => {
    let handlers;
    client.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
      return vi.fn();
    });
    authMock.decryptWithAuth.mockRejectedValueOnce(new Error('bad ciphertext'));
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://encrypted.example'], servicePubkey: 'b'.repeat(64) });

    const promise = transport.awaitEncryptedResult({ requestEventId: 'req-1' });
    await handlers.onEvent({ id: 'result-1', pubkey: 'b'.repeat(64), tags: [['e', 'req-1'], ['p', 'a'.repeat(64)]], content: 'not-decryptable' });

    await expect(promise).rejects.toThrow('bad ciphertext');
  });
});
