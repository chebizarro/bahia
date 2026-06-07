import { describe, it, expect, beforeEach, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const authMock = vi.hoisted(() => ({
  authState: { status: 'authenticated', pubkey: 'a'.repeat(64) },
  encryptWithAuth: vi.fn(),
  decryptWithAuth: vi.fn(),
  ensureEncryptedSignerReady: vi.fn(),
  signWithAuth: vi.fn()
}));

const canonicalDiscoveryFixture = JSON.parse(
  readFileSync(resolve(process.cwd(), '../test/fixtures/system_discovery_sidecar_first.json'), 'utf8')
);

const systemMock = vi.hoisted(() => ({
  currentSystemInfo: vi.fn(() => ({
    features: {
      encrypted_nostr_requests: true
    },
    nostr: {
      service_pubkey: 'b'.repeat(64),
      browser_relays: ['wss://relay.example']
    }
  }))
}));

vi.mock('$lib/stores/auth.js', () => authMock);
vi.mock('$lib/stores/system.svelte.js', () => systemMock);

function fakeClient() {
  return {
    getConnectedRelays: vi.fn(() => ['wss://relay.example']),
    connect: vi.fn().mockResolvedValue(),
    publish: vi.fn().mockResolvedValue([{ relay: 'wss://relay.example', sent: true, accepted: true, message: '' }]),
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
    authMock.ensureEncryptedSignerReady.mockResolvedValue(true);
    authMock.encryptWithAuth.mockImplementation(async (_pubkey, plaintext) => `cipher:${plaintext}`);
    authMock.decryptWithAuth.mockImplementation(async (_pubkey, ciphertext) => ciphertext.replace(/^cipher:/, ''));
    authMock.signWithAuth.mockImplementation(async (event) => ({ ...event, id: 'request-id', pubkey: authMock.authState.pubkey, sig: 'sig' }));
    systemMock.currentSystemInfo.mockReturnValue({
      features: {
        encrypted_nostr_requests: true
      },
      nostr: {
        service_pubkey: 'b'.repeat(64),
        browser_relays: ['wss://relay.example']
      }
    });
    client = fakeClient();
    module = await import('../../src/lib/nostr/encrypted-controlplane.js');
  });

  it('uses standard Bahia relays for ContextVM requests when the feature is enabled', () => {
    expect(module.encryptedRelayUrlsFromSystemInfo()).toEqual(['wss://relay.example']);
    expect(module.encryptedRequestsAvailable()).toBe(true);
  });

  it('prefers discovered ContextVM relays for encrypted ContextVM requests', () => {
    const info = {
      features: {
        encrypted_nostr_requests: true
      },
      nostr: {
        service_pubkey: 'b'.repeat(64),
        browser_relays: ['wss://public.example'],
        contextvm_relays: ['wss://contextvm.example']
      }
    };

    expect(module.encryptedRelayUrlsFromSystemInfo(info)).toEqual(['wss://contextvm.example']);
    expect(module.encryptedRequestsAvailable(info)).toBe(true);
  });

  it('returns configured Bahia browser relays for encrypted requests when ContextVM relays are absent', () => {
    expect(module.encryptedRelayUrlsFromSystemInfo({
      features: {
        encrypted_nostr_requests: true
      },
      nostr: {
        service_pubkey: 'b'.repeat(64),
        browser_relays: ['wss://my-relay.example']
      }
    })).toEqual(['wss://my-relay.example']);
    expect(module.encryptedRequestsAvailable({
      features: {
        encrypted_nostr_requests: true
      },
      nostr: {
        service_pubkey: 'b'.repeat(64),
        browser_relays: ['wss://my-relay.example']
      }
    })).toBe(true);
  });

  it('fails closed when encrypted capability is not explicitly advertised', () => {
    const publicOnly = {
      features: {
        encrypted_nostr_requests: false
      },
      nostr: {
        service_pubkey: 'b'.repeat(64),
        browser_relays: ['wss://relay.example']
      }
    };

    expect(module.encryptedRequestsAvailable(publicOnly)).toBe(false);
    expect(module.encryptedRelayUrlsFromSystemInfo(publicOnly)).toEqual(['wss://relay.example']);
  });

  it('requires service_pubkey for encrypted capability', () => {
    const noServicePubkey = {
      features: {
        encrypted_nostr_requests: true
      },
      nostr: {
        browser_relays: ['wss://relay.example']
      }
    };

    expect(module.encryptedRequestsAvailable(noServicePubkey)).toBe(false);
    expect(module.encryptedRelayUrlsFromSystemInfo(noServicePubkey)).toEqual(['wss://relay.example']);
    expect(module.encryptedRequestsAvailable({
      features: {
        encrypted_nostr_requests: true
      },
      nostr: {
        service_pubkey: 'b'.repeat(64),
        browser_relays: ['wss://relay.example']
      }
    })).toBe(true);
    expect(module.encryptedRelayUrlsFromSystemInfo(canonicalDiscoveryFixture)).toEqual(['wss://public.example']);
  });

  it('builds encrypted request events without targeting public browser relays', async () => {
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: 'b'.repeat(64) });

    const event = await transport.buildEncryptedRequestEvent({ operation: 'payments.history', payload: { limit: 10 }, requestId: 'ctxvm-req-1' });

    const plaintext = JSON.parse(authMock.encryptWithAuth.mock.calls[0][1]);
    expect(plaintext).toEqual({
      jsonrpc: '2.0',
      id: 'ctxvm-req-1',
      method: 'payments/history',
      params: { limit: 10, _meta: { progressToken: 'ctxvm-req-1' } }
    });
    expect(authMock.ensureEncryptedSignerReady).toHaveBeenCalledWith('b'.repeat(64));
    expect(authMock.signWithAuth).toHaveBeenCalledWith(expect.objectContaining({
      kind: module.CONTEXTVM_MESSAGE_KIND,
      tags: expect.arrayContaining([['p', 'b'.repeat(64)], [module.ENCRYPTED_REQUEST_ROUTING_TAG, module.ENCRYPTED_REQUEST_WIRE_VERSION], ['method', 'payments/history']]),
      content: expect.stringMatching(/^cipher:/)
    }));
    expect(event.id).toBe('request-id');
  });

  it('fails locally before publish when the signer lacks browser-visible NIP-44 support', async () => {
    authMock.ensureEncryptedSignerReady.mockRejectedValueOnce(new Error('Failed to encrypt with NIP-44: signer bridge unavailable'));
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: 'b'.repeat(64) });

    await expect(transport.requestEncryptedResult({ operation: 'notifications/channels-list', payload: {} }))
      .rejects.toThrow('signer bridge unavailable');

    expect(client.publish).not.toHaveBeenCalled();
    expect(client.subscribe).not.toHaveBeenCalled();
    expect(module.encryptedRelayUrlsFromSystemInfo({
      features: {
        encrypted_nostr_requests: true
      },
      nostr: {
        service_pubkey: 'b'.repeat(64),
        browser_relays: ['wss://relay.example'],
        contextvm_relays: ['wss://contextvm.example']
      }
    })).toEqual(['wss://contextvm.example']);
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

    const promise = transport.awaitEncryptedResult({ requestEventId: 'req-1', contextVMRequestId: 'ctxvm-req-1' });
    await handlers.onEvent({ id: 'other', pubkey: 'b'.repeat(64), tags: [['e', 'other'], ['p', 'a'.repeat(64)]], content: 'cipher:{}' });
    await handlers.onEvent({ id: 'spoofed', pubkey: 'c'.repeat(64), tags: [['e', 'req-1'], ['p', 'a'.repeat(64)]], content: 'cipher:{}' });
    handlers.onEose('wss://relay.example');
    await handlers.onEvent({ id: 'result-1', pubkey: 'b'.repeat(64), tags: [['e', 'req-1'], ['p', 'a'.repeat(64)]], content: 'cipher:{"jsonrpc":"2.0","id":"ctxvm-req-1","result":{"status":"ok","payload":{"count":1}}}' });
    await handlers.onEvent({ id: 'result-1', pubkey: 'b'.repeat(64), tags: [['e', 'req-1'], ['p', 'a'.repeat(64)]], content: 'cipher:{}' });

    await expect(promise).resolves.toMatchObject({ payload: { status: 'ok', payload: { count: 1 } } });
    expect(unsubscribe).toHaveBeenCalledTimes(1);
    expect(client.subscribe).toHaveBeenCalledWith(
      [{ kinds: [module.CONTEXTVM_MESSAGE_KIND], '#e': ['req-1'], '#p': ['a'.repeat(64)], authors: ['b'.repeat(64)] }],
      expect.objectContaining({ onEvent: expect.any(Function), onEose: expect.any(Function), onClosed: expect.any(Function) })
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

  it('requires decrypted encrypted result envelopes to include matching request correlation', async () => {
    let handlers;
    client.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
      return vi.fn();
    });
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: 'b'.repeat(64) });

    const missing = transport.awaitEncryptedResult({ requestEventId: 'req-1' });
    await handlers.onEvent({ id: 'result-missing-correlation', pubkey: 'b'.repeat(64), tags: [['e', 'req-1'], ['p', 'a'.repeat(64)]], content: 'cipher:{"jsonrpc":"2.0","id":"other","result":{}}' });
    await expect(missing).rejects.toThrow('ContextVM encrypted result payload did not correlate');
  });

  it('reports encrypted result AUTH and all-relay CLOSED failures explicitly', async () => {
    let handlers;
    client.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
      return vi.fn();
    });
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: 'b'.repeat(64) });

    const authFailure = transport.awaitEncryptedResult({ requestEventId: 'req-1' });
    handlers.onClosed('auth-required: sign in', 'wss://relay.example');
    await expect(authFailure).rejects.toThrow('ContextVM result subscription auth closure: wss://relay.example: auth-required: sign in');

    client.getConnectedRelays.mockReturnValueOnce(['wss://relay-1.example', 'wss://relay-2.example']);
    const closedFailure = transport.awaitEncryptedResult({ requestEventId: 'req-2' });
    handlers.onClosed('closed: shard restarting', 'wss://relay-1.example');
    handlers.onClosed('closed: subscription limit', 'wss://relay-2.example');
    await expect(closedFailure).rejects.toThrow('wss://relay-1.example (closed: shard restarting); wss://relay-2.example (closed: subscription limit)');
  });

  it('treats relay-less or unknown-relay CLOSED as terminal instead of waiting indefinitely', async () => {
    let handlers;
    client.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
      return vi.fn();
    });
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: 'b'.repeat(64) });

    client.getConnectedRelays.mockReturnValueOnce(null);
    const unknownRelays = transport.awaitEncryptedResult({ requestEventId: 'req-unknown-relays' });
    handlers.onClosed('closed: relay unavailable');
    await expect(unknownRelays).rejects.toThrow('ContextVM result subscription closed before result: closed: relay unavailable');

    client.getConnectedRelays.mockReturnValueOnce(['wss://relay.example']);
    const relaylessClosed = transport.awaitEncryptedResult({ requestEventId: 'req-relayless' });
    handlers.onClosed('closed: relay unavailable');
    await expect(relaylessClosed).rejects.toThrow('ContextVM result subscription closed before result: closed: relay unavailable');
  });

  it('does not publish encrypted ContextVM requests when operation cancellation is already aborted', async () => {
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: 'b'.repeat(64) });
    const controller = new AbortController();
    controller.abort(new Error('operator cancelled before publish'));

    await expect(transport.requestEncryptedResult({ operation: 'orgs.list', payload: {}, signal: controller.signal }))
      .rejects.toThrow('operator cancelled before publish');
    expect(client.connect).not.toHaveBeenCalled();
    expect(client.publish).not.toHaveBeenCalled();
    expect(client.subscribe).not.toHaveBeenCalled();
  });

  it('rejects result waiting only from operation cancellation when relays remain open without a result', async () => {
    const unsubscribe = vi.fn();
    client.subscribe.mockImplementation((_filters, _handlers) => unsubscribe);
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: 'b'.repeat(64) });
    const controller = new AbortController();

    const promise = transport.awaitEncryptedResult({ requestEventId: 'req-1', signal: controller.signal });
    controller.abort(new Error('operator cancelled ContextVM request'));

    await expect(promise).rejects.toThrow('operator cancelled ContextVM request');
    expect(unsubscribe).toHaveBeenCalledTimes(1);
  });
});
