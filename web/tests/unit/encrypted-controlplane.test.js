import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const authMock = vi.hoisted(() => ({
  authState: { status: 'authenticated', pubkey: 'a'.repeat(64) },
  encryptWithAuth: vi.fn(),
  decryptWithAuth: vi.fn(),
  ensureEncryptedSignerReady: vi.fn(),
  signWithAuth: vi.fn()
}));

const nostrClientMock = vi.hoisted(() => ({
  activeClient: null,
  createNostrPoolClient: vi.fn(() => nostrClientMock.activeClient),
  getTagValues: (event, name) => (event?.tags || [])
    .filter((tag) => Array.isArray(tag) && tag[0] === name)
    .map((tag) => tag[1])
}));

const SERVICE_PUBKEY = 'a70a59980b1be3070959800f94f4221d54ef77a71d686ac85fedadfc586813a0';

const canonicalDiscoveryFixture = JSON.parse(
  readFileSync(resolve(process.cwd(), '../test/fixtures/system_discovery_sidecar_first.json'), 'utf8')
);

const systemMock = vi.hoisted(() => ({
  currentSystemInfo: vi.fn(() => ({
    features: {
      encrypted_nostr_requests: true
    },
    nostr: {
      service_pubkey: SERVICE_PUBKEY,
      browser_relays: ['wss://relay.example']
    }
  }))
}));

vi.mock('$lib/stores/auth.js', () => authMock);
vi.mock('$lib/stores/system.svelte.js', () => systemMock);
vi.mock('../../src/lib/nostr/client.js', () => nostrClientMock);

function progressAckDiscovery() {
  return {
    features: {
      encrypted_nostr_requests: true
    },
    nostr: {
      service_pubkey: SERVICE_PUBKEY,
      browser_relays: ['wss://relay.example']
    },
    control_plane: {
      wire_version: 'contextvm-jsonrpc-v2',
      capabilities: ['encrypted_controlplane.progress_ack']
    }
  };
}

function legacyDiscovery() {
  return {
    features: {
      encrypted_nostr_requests: true
    },
    nostr: {
      service_pubkey: SERVICE_PUBKEY,
      browser_relays: ['wss://relay.example']
    }
  };
}

async function flushAsync() {
  for (let i = 0; i < 8; i += 1) await Promise.resolve();
}

function resultEvent(module, requestEventId, payload) {
  const inner = {
    id: `inner-${Math.random()}`,
    kind: module.CONTEXTVM_MESSAGE_KIND,
    pubkey: SERVICE_PUBKEY,
    created_at: Math.floor(Date.now() / 1000),
    tags: [['e', requestEventId, '', 'reply'], ['p', authMock.authState.pubkey], [module.ENCRYPTED_REQUEST_ROUTING_TAG, module.ENCRYPTED_REQUEST_WIRE_VERSION]],
    content: JSON.stringify(payload),
    sig: '0'.repeat(128)
  };
  return {
    id: `result-${Math.random()}`,
    kind: module.CONTEXTVM_GIFT_WRAP_KIND,
    pubkey: SERVICE_PUBKEY,
    tags: [['e', requestEventId], ['p', authMock.authState.pubkey]],
    content: `cipher:${JSON.stringify(inner)}`
  };
}

function fakeClient() {
  return {
    getConnectedRelays: vi.fn(() => ['wss://relay.example']),
    connect: vi.fn().mockResolvedValue(),
    publish: vi.fn().mockResolvedValue([{ relay: 'wss://relay.example', sent: true, accepted: true, message: '' }]),
    subscribe: vi.fn(() => vi.fn()),
    disconnect: vi.fn()
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
    systemMock.currentSystemInfo.mockReturnValue(legacyDiscovery());
    client = fakeClient();
    nostrClientMock.activeClient = client;
    globalThis.__BAHIA_E2E_TRUST_MOCK_RELAY_EVENTS = true;
    module = await import('../../src/lib/nostr/encrypted-controlplane.js');
  });

  afterEach(() => {
    vi.useRealTimers();
    module?.disconnectEncryptedControlplane?.();
    nostrClientMock.activeClient = null;
    delete globalThis.__BAHIA_E2E_TRUST_MOCK_RELAY_EVENTS;
    delete globalThis.location;
  });

  it('uses standard Bahia relays for ContextVM requests when the feature is enabled', () => {
    expect(module.encryptedRelayUrlsFromSystemInfo()).toEqual(['wss://relay.example']);
    expect(module.encryptedRequestsAvailable()).toBe(true);
  });

  it('uses all discovered ContextVM and browser relays for encrypted ContextVM requests', () => {
    const info = {
      features: {
        encrypted_nostr_requests: true
      },
      nostr: {
        service_pubkey: SERVICE_PUBKEY,
        browser_relays: ['wss://public.example'],
        contextvm_relays: ['wss://contextvm.example']
      }
    };

    expect(module.encryptedRelayUrlsFromSystemInfo(info)).toEqual(['wss://contextvm.example', 'wss://public.example']);
    expect(module.encryptedRequestsAvailable(info)).toBe(true);
  });

  it('filters insecure LAN ContextVM relays for HTTPS pages while preserving multiple secure relays', () => {
    Object.defineProperty(globalThis, 'location', {
      configurable: true,
      value: { protocol: 'https:' }
    });

    const info = {
      features: {
        encrypted_nostr_requests: true
      },
      nostr: {
        service_pubkey: SERVICE_PUBKEY,
        browser_relays: ['wss://bahia.sharegap.net/relay'],
        contextvm_relays: ['ws://192.168.40.104:3337/', 'wss://relay.sharegap.net/']
      }
    };

    expect(module.encryptedRelayUrlsFromSystemInfo(info)).toEqual([
      'wss://relay.sharegap.net/',
      'wss://bahia.sharegap.net/relay'
    ]);
    expect(module.encryptedRequestsAvailable(info)).toBe(true);
  });

  it('returns configured Bahia browser relays for encrypted requests when ContextVM relays are absent', () => {
    expect(module.encryptedRelayUrlsFromSystemInfo({
      features: {
        encrypted_nostr_requests: true
      },
      nostr: {
        service_pubkey: SERVICE_PUBKEY,
        browser_relays: ['wss://my-relay.example']
      }
    })).toEqual(['wss://my-relay.example']);
    expect(module.encryptedRequestsAvailable({
      features: {
        encrypted_nostr_requests: true
      },
      nostr: {
        service_pubkey: SERVICE_PUBKEY,
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
        service_pubkey: SERVICE_PUBKEY,
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
        service_pubkey: SERVICE_PUBKEY,
        browser_relays: ['wss://relay.example']
      }
    })).toBe(true);
    expect(module.encryptedRelayUrlsFromSystemInfo(canonicalDiscoveryFixture)).toEqual(['wss://public.example']);
  });

  it('builds encrypted request events without targeting public browser relays', async () => {
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: SERVICE_PUBKEY });

    const event = await transport.buildEncryptedRequestEvent({ operation: 'payments.history', payload: { limit: 10 }, requestId: 'ctxvm-req-1' });

    expect(authMock.signWithAuth).toHaveBeenCalledWith(expect.objectContaining({
      kind: module.CONTEXTVM_MESSAGE_KIND,
      tags: expect.arrayContaining([['p', SERVICE_PUBKEY], [module.ENCRYPTED_REQUEST_ROUTING_TAG, module.ENCRYPTED_REQUEST_WIRE_VERSION], ['method', 'payments/history']])
    }));
    expect(JSON.parse(authMock.signWithAuth.mock.calls[0][0].content)).toEqual({
      jsonrpc: '2.0',
      id: 'ctxvm-req-1',
      method: 'payments/history',
      params: { limit: 10, _meta: { progressToken: 'ctxvm-req-1' } }
    });
    expect(authMock.ensureEncryptedSignerReady).toHaveBeenCalledWith(SERVICE_PUBKEY);
    expect(event.kind).toBe(module.ENCRYPTED_REQUEST_KIND);
    expect(event.pubkey).not.toBe(authMock.authState.pubkey);
    expect(event.tags).toEqual([['p', SERVICE_PUBKEY]]);
    expect(event.content).toEqual(expect.any(String));
  });

  it('advertises v2 progress ack support while preserving the v1 Nostr routing tag', async () => {
    systemMock.currentSystemInfo.mockReturnValue(progressAckDiscovery());
    expect(module.contextVMProgressAckSupported()).toBe(true);

    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: SERVICE_PUBKEY });
    await transport.buildEncryptedRequestEvent({ operation: 'payments.history', payload: { limit: 10 }, requestId: 'ctx-v2-routing' });

    expect(module.ENCRYPTED_REQUEST_WIRE_VERSION).toBe('contextvm-jsonrpc-v1');
    expect(authMock.signWithAuth).toHaveBeenCalledWith(expect.objectContaining({
      tags: expect.arrayContaining([[module.ENCRYPTED_REQUEST_ROUTING_TAG, 'contextvm-jsonrpc-v1']])
    }));
  });

  it('fails locally before publish when the signer lacks browser-visible NIP-44 support', async () => {
    authMock.ensureEncryptedSignerReady.mockRejectedValueOnce(new Error('Failed to encrypt with NIP-44: signer bridge unavailable'));
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: SERVICE_PUBKEY });

    await expect(transport.requestEncryptedResult({ operation: 'notifications.channels.list', payload: {} }))
      .rejects.toThrow('signer bridge unavailable');

    expect(client.publish).not.toHaveBeenCalled();
    expect(client.subscribe).not.toHaveBeenCalled();
    expect(module.encryptedRelayUrlsFromSystemInfo({
      features: {
        encrypted_nostr_requests: true
      },
      nostr: {
        service_pubkey: SERVICE_PUBKEY,
        browser_relays: ['wss://relay.example'],
        contextvm_relays: ['wss://contextvm.example']
      }
    })).toEqual(['wss://contextvm.example', 'wss://relay.example']);
  });

  it('publishes through the encrypted-request client and requires an accepted OK', async () => {
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: SERVICE_PUBKEY });
    const event = { id: 'request-id', kind: module.ENCRYPTED_REQUEST_KIND, tags: [], content: 'cipher' };

    await expect(transport.publishEncryptedRequest(event)).resolves.toMatchObject({ requestEventId: 'request-id' });
    expect(client.connect).not.toHaveBeenCalled();
    expect(client.publish).toHaveBeenCalledWith(event);

    client.publish.mockResolvedValueOnce([{ relay: 'wss://requests.example', sent: true, accepted: false, message: 'blocked: no' }]);
    await expect(transport.publishEncryptedRequest(event)).rejects.toThrow('blocked: no');
  });

  it('establishes the shared result subscription before publishing a request event', async () => {
    const order = [];
    const event = { id: 'request-id', kind: module.ENCRYPTED_REQUEST_KIND, tags: [], content: 'cipher' };
    client.subscribe.mockImplementation(() => {
      order.push('subscribe');
      return vi.fn();
    });
    client.publish.mockImplementation(async () => {
      order.push('publish');
      return [{ relay: 'wss://relay.example', sent: true, accepted: true, message: '' }];
    });

    await expect(module.publishEncryptedRequest({ event })).resolves.toMatchObject({ requestEventId: 'request-id' });

    expect(order).toEqual(['subscribe', 'publish']);
  });

  it('reuses one shared transport while concurrent startup requests are still connecting', async () => {
    let releaseConnect;
    const connectPromise = new Promise((resolve) => {
      releaseConnect = () => resolve({ connected: 1 });
    });
    client.connect.mockReturnValue(connectPromise);

    const first = module.requestEncryptedResult({ operation: 'payments.history', payload: {}, workTimeoutMs: 10 });
    const second = module.requestEncryptedResult({ operation: 'orgs.list', payload: {}, workTimeoutMs: 10 });
    await flushAsync();

    expect(nostrClientMock.createNostrPoolClient).toHaveBeenCalledTimes(1);
    expect(client.connect).toHaveBeenCalledTimes(2);

    releaseConnect();
    await Promise.allSettled([first, second]);
  });

  it('preserves prebuilt event publishing when the requester is unauthenticated', async () => {
    authMock.authState.status = 'unauthenticated';
    authMock.authState.pubkey = null;
    const event = { id: 'request-id', kind: module.ENCRYPTED_REQUEST_KIND, tags: [], content: 'cipher' };

    await expect(module.publishEncryptedRequest({ event })).resolves.toMatchObject({ requestEventId: 'request-id' });

    expect(client.subscribe).not.toHaveBeenCalled();
    expect(client.publish).toHaveBeenCalledWith(event);
  });

  it('awaitEncryptedResult resolves only correlated encrypted results and unsubscribes', async () => {
    let handlers;
    const unsubscribe = vi.fn();
    client.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
      return unsubscribe;
    });
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: SERVICE_PUBKEY });

    const promise = transport.awaitEncryptedResult({ requestEventId: 'req-1', contextVMRequestId: 'ctxvm-req-1' });
    await handlers.onEvent({ id: 'other', kind: module.ENCRYPTED_RESULT_KIND, pubkey: 'c'.repeat(64), tags: [['e', 'other'], ['p', 'a'.repeat(64)]], content: 'cipher:{}' });
    await handlers.onEvent({ id: 'spoofed', kind: module.CONTEXTVM_MESSAGE_KIND, pubkey: 'c'.repeat(64), tags: [['e', 'req-1'], ['p', 'a'.repeat(64)]], content: 'cipher:{}' });
    handlers.onEose('wss://relay.example');
    await handlers.onEvent({ id: 'result-1', kind: module.ENCRYPTED_RESULT_KIND, pubkey: 'd'.repeat(64), tags: [['e', 'req-1'], ['p', 'a'.repeat(64)]], content: 'cipher:{"jsonrpc":"2.0","id":"ctxvm-req-1","result":{"status":"ok","payload":{"count":1}}}' });
    await handlers.onEvent({ id: 'result-1', kind: module.ENCRYPTED_RESULT_KIND, pubkey: 'd'.repeat(64), tags: [['e', 'req-1'], ['p', 'a'.repeat(64)]], content: 'cipher:{}' });

    await expect(promise).resolves.toMatchObject({ payload: { status: 'ok', payload: { count: 1 } } });
    expect(unsubscribe).toHaveBeenCalledTimes(1);
    expect(client.subscribe).toHaveBeenCalledWith(
      [{ kinds: [module.ENCRYPTED_RESULT_KIND], '#e': ['req-1'], '#p': ['a'.repeat(64)] }],
      expect.objectContaining({ onEvent: expect.any(Function), onEose: expect.any(Function), onClosed: expect.any(Function) })
    );
  });

  it('cleans up result subscription when publish fails', async () => {
    const unsubscribe = vi.fn();
    client.subscribe.mockReturnValueOnce(unsubscribe);
    client.publish.mockResolvedValueOnce([{ relay: 'wss://requests.example', sent: true, accepted: false, message: 'blocked: no' }]);
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: SERVICE_PUBKEY });

    await expect(transport.requestEncryptedResult({ operation: 'payments.history', payload: { limit: 5 } })).rejects.toThrow('blocked: no');

    expect(unsubscribe).toHaveBeenCalledTimes(1);
  });

  it('rejects gift-wrapped ContextVM results whose inner event is not service-authored', async () => {
    let handlers;
    client.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
      return vi.fn();
    });
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: SERVICE_PUBKEY });

    const promise = transport.awaitEncryptedResult({ requestEventId: 'req-spoofed' });
    await handlers.onEvent({
      id: 'result-spoofed-inner',
      kind: module.CONTEXTVM_GIFT_WRAP_KIND,
      pubkey: 'c'.repeat(64),
      tags: [['e', 'req-spoofed'], ['p', 'a'.repeat(64)]],
      content: `cipher:${JSON.stringify({
        id: 'inner-spoofed',
        kind: module.CONTEXTVM_MESSAGE_KIND,
        pubkey: 'b'.repeat(64),
        created_at: Math.floor(Date.now() / 1000),
        tags: [['e', 'req-spoofed'], ['p', 'a'.repeat(64)]],
        content: '{"jsonrpc":"2.0","id":"req-spoofed","result":{"ok":true}}',
        sig: '0'.repeat(128)
      })}`
    });

    await expect(promise).rejects.toThrow('inner event was not signed by the expected service pubkey');
  });

  it('rejects on decrypt failures for correlated result events', async () => {
    let handlers;
    client.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
      return vi.fn();
    });
    authMock.decryptWithAuth.mockRejectedValueOnce(new Error('bad ciphertext'));
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: SERVICE_PUBKEY });

    const promise = transport.awaitEncryptedResult({ requestEventId: 'req-1' });
    await handlers.onEvent({ id: 'result-1', kind: module.ENCRYPTED_RESULT_KIND, pubkey: 'c'.repeat(64), tags: [['e', 'req-1'], ['p', 'a'.repeat(64)]], content: 'not-decryptable' });

    await expect(promise).rejects.toThrow('bad ciphertext');
  });

  it('requires decrypted encrypted result envelopes to include matching request correlation', async () => {
    let handlers;
    client.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
      return vi.fn();
    });
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: SERVICE_PUBKEY });

    const missing = transport.awaitEncryptedResult({ requestEventId: 'req-1' });
    await handlers.onEvent({ id: 'result-missing-correlation', kind: module.ENCRYPTED_RESULT_KIND, pubkey: 'c'.repeat(64), tags: [['e', 'req-1'], ['p', 'a'.repeat(64)]], content: 'cipher:{"jsonrpc":"2.0","id":"other","result":{}}' });
    await expect(missing).rejects.toThrow('ContextVM encrypted result payload did not correlate');
  });

  it('reports encrypted result AUTH and all-relay CLOSED failures explicitly', async () => {
    let handlers;
    client.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
      return vi.fn();
    });
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: SERVICE_PUBKEY });

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
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: SERVICE_PUBKEY });

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
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: SERVICE_PUBKEY });
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
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: SERVICE_PUBKEY });
    const controller = new AbortController();

    const promise = transport.awaitEncryptedResult({ requestEventId: 'req-1', signal: controller.signal });
    controller.abort(new Error('operator cancelled ContextVM request'));

    await expect(promise).rejects.toThrow('operator cancelled ContextVM request');
    expect(unsubscribe).toHaveBeenCalledTimes(1);
  });

  it('treats progress notifications as ack-only and keeps waiting for the terminal result', async () => {
    vi.useFakeTimers();
    systemMock.currentSystemInfo.mockReturnValue(progressAckDiscovery());
    let handlers;
    client.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
      return vi.fn();
    });
    const promise = module.requestEncryptedResult({
      operation: 'payments.history',
      payload: { limit: 1 },
      requestId: 'ctx-progress',
      ackTimeoutMs: 10,
      workTimeoutMs: 100
    });
    let settled = false;
    promise.then(() => { settled = true; }, () => { settled = true; });

    await flushAsync();
    expect(client.publish).toHaveBeenCalledTimes(1);
    const requestEventId = client.publish.mock.calls[0][0].id;
    await handlers.onEvent(resultEvent(module, requestEventId, {
      jsonrpc: '2.0',
      method: 'notifications/progress',
      params: { requestId: requestEventId, status: 'processing' }
    }));
    await vi.advanceTimersByTimeAsync(25);

    expect(settled).toBe(false);

    await handlers.onEvent(resultEvent(module, requestEventId, {
      jsonrpc: '2.0',
      id: 'ctx-progress',
      result: { ok: true }
    }));

    await expect(promise).resolves.toMatchObject({ result: { ok: true } });
  });

  it('fires the short ack timeout on silence when progress ack is advertised', async () => {
    vi.useFakeTimers();
    systemMock.currentSystemInfo.mockReturnValue(progressAckDiscovery());
    client.subscribe.mockImplementation((_filters, nextHandlers) => {
      expect(nextHandlers.onEvent).toEqual(expect.any(Function));
      return vi.fn();
    });

    const promise = module.requestEncryptedResult({
      operation: 'payments.history',
      payload: { limit: 1 },
      requestId: 'ctx-silent',
      ackTimeoutMs: 10,
      workTimeoutMs: 100
    });

    const rejection = expect(promise).rejects.toThrow('no service acknowledged within 10ms — check service-pubkey discovery / relay auth');
    await flushAsync();
    expect(client.publish).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(11);

    await rejection;
  });

  it('keeps the backward-compatible single work timeout when progress ack is not advertised', async () => {
    vi.useFakeTimers();
    systemMock.currentSystemInfo.mockReturnValue(legacyDiscovery());
    client.subscribe.mockImplementation(() => vi.fn());

    const promise = module.requestEncryptedResult({
      operation: 'payments.history',
      payload: { limit: 1 },
      requestId: 'ctx-legacy',
      ackTimeoutMs: 10,
      workTimeoutMs: 30
    });
    let settled = false;
    promise.then(() => { settled = true; }, () => { settled = true; });

    await flushAsync();
    expect(client.publish).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(11);
    expect(settled).toBe(false);

    await vi.advanceTimersByTimeAsync(20);
    await expect(promise).rejects.toThrow('timed out after 30ms waiting for result');
  });

  it('fails before publishing when encrypted preconditions are unavailable or relay connectivity is absent', async () => {
    systemMock.currentSystemInfo.mockReturnValue({
      features: { encrypted_nostr_requests: false },
      nostr: { service_pubkey: SERVICE_PUBKEY, browser_relays: ['wss://relay.example'] }
    });

    await expect(module.requestEncryptedResult({ operation: 'payments.history', payload: {} }))
      .rejects.toThrow('features.encrypted_nostr_requests');
    expect(client.publish).not.toHaveBeenCalled();
    expect(client.subscribe).not.toHaveBeenCalled();

    systemMock.currentSystemInfo.mockReturnValue({
      features: { encrypted_nostr_requests: true },
      nostr: { service_pubkey: 'not-a-pubkey', browser_relays: ['wss://relay.example'] }
    });

    await expect(module.requestEncryptedResult({ operation: 'payments.history', payload: {} }))
      .rejects.toThrow('missing a valid service-pubkey');
    expect(client.publish).not.toHaveBeenCalled();
    expect(client.subscribe).not.toHaveBeenCalled();

    systemMock.currentSystemInfo.mockReturnValue(legacyDiscovery());
    client.getConnectedRelays.mockReturnValue([]);

    await expect(module.requestEncryptedResult({ operation: 'payments.history', payload: {} }))
      .rejects.toThrow('No Bahia relay is connected');
    expect(client.publish).not.toHaveBeenCalled();
  });
});
