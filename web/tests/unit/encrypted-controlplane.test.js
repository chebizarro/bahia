import { describe, it, expect, beforeEach, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const authMock = vi.hoisted(() => ({
  authState: { status: 'authenticated', pubkey: 'a'.repeat(64) },
  encryptWithAuth: vi.fn(),
  decryptWithAuth: vi.fn(),
  signWithAuth: vi.fn()
}));

const canonicalDiscoveryFixture = JSON.parse(
  readFileSync(resolve(process.cwd(), '../test/fixtures/system_discovery_sidecar_first.json'), 'utf8')
);

const systemMock = vi.hoisted(() => ({
  currentSystemInfo: vi.fn(() => ({
    nostr: {
      service_pubkey: 'b'.repeat(64),
      browser_encrypted_request_relays: ['wss://requests.example'],
      browser_relays: ['wss://public.example']
    }
  }))
}));

vi.mock('$lib/stores/auth.js', () => authMock);
vi.mock('$lib/stores/system.svelte.js', () => systemMock);

function fakeClient() {
  return {
    getConnectedRelays: vi.fn(() => ['wss://requests.example']),
    connect: vi.fn().mockResolvedValue(),
    publish: vi.fn().mockResolvedValue([{ relay: 'wss://requests.example', sent: true, accepted: true, message: '' }]),
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
        browser_encrypted_request_relays: ['wss://requests.example'],
        browser_relays: ['wss://public.example']
      }
    });
    client = fakeClient();
    module = await import('../../src/lib/nostr/encrypted-controlplane.js');
  });

  it('prefers canonical relay URLs for encrypted request events and ignores public relays', () => {
    expect(module.encryptedRelayUrlsFromSystemInfo()).toEqual(['wss://requests.example']);
    expect(module.encryptedRequestsAvailable()).toBe(true);
  });

  it('does not treat public browser relays as encrypted request relays', () => {
    expect(module.encryptedRelayUrlsFromSystemInfo({
      nostr: {
        service_pubkey: 'b'.repeat(64),
        browser_relays: ['wss://public.example']
      }
    })).toEqual([]);
    expect(module.encryptedRequestsAvailable({
      nostr: {
        service_pubkey: 'b'.repeat(64),
        browser_relays: ['wss://public.example']
      }
    })).toBe(false);
  });

  it('requires explicit encrypted capability indicators rather than public bootstrap fields alone', () => {
    const publicOnly = {
      nostr: {
        browser_relays: canonicalDiscoveryFixture.nostr.browser_relays,
        sidecar_url: canonicalDiscoveryFixture.nostr.sidecar_url
      },
      features: {
        relay_sidecar: true,
        relay_read_models: true
      }
    };

    expect(module.encryptedRequestsAvailable(publicOnly)).toBe(false);
    expect(module.encryptedRelayUrlsFromSystemInfo(publicOnly)).toEqual([]);
    expect(module.encryptedRequestsAvailable(canonicalDiscoveryFixture)).toBe(true);
    expect(module.encryptedRelayUrlsFromSystemInfo(canonicalDiscoveryFixture)).toEqual(['wss://requests.example']);
  });

  it('builds encrypted request events without targeting public browser relays', async () => {
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: 'b'.repeat(64) });

    const event = await transport.buildEncryptedRequestEvent({ operation: 'payments.history', payload: { limit: 10 } });

    expect(authMock.encryptWithAuth).toHaveBeenCalledWith('b'.repeat(64), expect.stringContaining('payments.history'));
    expect(authMock.signWithAuth).toHaveBeenCalledWith(expect.objectContaining({
      kind: module.ENCRYPTED_REQUEST_KIND,
      tags: expect.arrayContaining([['p', 'b'.repeat(64)], [module.ENCRYPTED_REQUEST_ROUTING_TAG, module.ENCRYPTED_REQUEST_WIRE_VERSION]]),
      content: expect.stringMatching(/^cipher:/)
    }));
    expect(event.id).toBe('request-id');
  });

  it('fails locally before publish when the signer lacks browser-visible NIP-44 support', async () => {
    authMock.encryptWithAuth.mockRejectedValueOnce(new Error('Event encryption failed: Active signer does not expose NIP-44 encryption'));
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: 'b'.repeat(64) });

    await expect(transport.requestEncryptedResult({ operation: 'notifications.channels.list', payload: {} }))
      .rejects.toThrow('Active signer does not expose NIP-44 encryption');

    expect(client.publish).not.toHaveBeenCalled();
    expect(client.subscribe).not.toHaveBeenCalled();
    expect(module.encryptedRelayUrlsFromSystemInfo({
      nostr: {
        service_pubkey: 'b'.repeat(64),
        browser_relays: ['wss://public.example']
      }
    })).toEqual([]);
  });

  it('publishes through the encrypted-request client and requires an accepted OK', async () => {
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: 'b'.repeat(64) });
    const event = { id: 'request-id', kind: module.ENCRYPTED_REQUEST_KIND, tags: [], content: 'cipher' };

    await expect(transport.publishEncryptedRequest(event)).resolves.toMatchObject({ requestEventId: 'request-id' });
    expect(client.connect).not.toHaveBeenCalled();
    expect(client.publish).toHaveBeenCalledWith(event);

    client.publish.mockResolvedValueOnce([{ relay: 'wss://requests.example', sent: true, accepted: false, message: 'blocked: no' }]);
    await expect(transport.publishEncryptedRequest(event)).rejects.toThrow('blocked: no');
  });

  it('awaitEncryptedResult resolves only correlated encrypted results and unsubscribes', async () => {
    let handlers;
    const unsubscribe = vi.fn();
    client.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
      return unsubscribe;
    });
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: 'b'.repeat(64) });

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
    client.publish.mockResolvedValueOnce([{ relay: 'wss://requests.example', sent: true, accepted: false, message: 'blocked: no' }]);
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: 'b'.repeat(64) });

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
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: 'b'.repeat(64) });

    const promise = transport.awaitEncryptedResult({ requestEventId: 'req-1' });
    await handlers.onEvent({ id: 'result-1', pubkey: 'b'.repeat(64), tags: [['e', 'req-1'], ['p', 'a'.repeat(64)]], content: 'not-decryptable' });

    await expect(promise).rejects.toThrow('bad ciphertext');
  });
});
