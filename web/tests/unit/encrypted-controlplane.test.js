import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { finalizeEvent, getPublicKey } from 'nostr-tools';

const authMock = vi.hoisted(() => ({
  authState: { status: 'authenticated', pubkey: 'a'.repeat(64) },
  encryptWithAuth: vi.fn(),
  decryptWithAuth: vi.fn(),
  ensureEncryptedSignerReady: vi.fn(),
  signWithAuth: vi.fn()
}));

const nostrClientMock = vi.hoisted(() => ({
  activeClient: null,
  nostr: {
    connect: (...args) => nostrClientMock.activeClient.connect(...args),
    getConnectedRelays: (...args) => nostrClientMock.activeClient.getConnectedRelays(...args),
    subscribeOnRelays: (_relays, filters, handlers) => nostrClientMock.activeClient.subscribe(filters, handlers),
    publish: (event) => nostrClientMock.activeClient.publish(event)
  }
}));

// ContextVM results are really signed with a throwaway service key; inbound
// validation has no test bypass.
const SERVICE_SECRET_KEY = Uint8Array.from({ length: 32 }, () => 0x5a);
const SERVICE_PUBKEY = getPublicKey(SERVICE_SECRET_KEY);
const FORGER_SECRET_KEY = Uint8Array.from({ length: 32 }, () => 0x6b);

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
vi.mock('../../src/lib/nostr/subscriptions.js', () => nostrClientMock);

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
  for (let i = 0; i < 24; i += 1) await Promise.resolve();
}

function resultEvent(module, requestEventId, payload) {
  const inner = finalizeEvent({
    kind: module.CONTEXTVM_MESSAGE_KIND,
    created_at: Math.floor(Date.now() / 1000),
    tags: [['e', requestEventId, '', 'reply'], ['p', authMock.authState.pubkey], [module.ENCRYPTED_REQUEST_ROUTING_TAG, module.ENCRYPTED_REQUEST_WIRE_VERSION]],
    content: JSON.stringify(payload)
  }, SERVICE_SECRET_KEY);
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
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ limitation: { max_message_length: 512000, max_content_length: 65535 } }) }));
    authMock.authState.status = 'authenticated';
    authMock.authState.pubkey = 'a'.repeat(64);
    authMock.ensureEncryptedSignerReady.mockResolvedValue(true);
    authMock.encryptWithAuth.mockImplementation(async (_pubkey, plaintext) => `cipher:${plaintext}`);
    authMock.decryptWithAuth.mockImplementation(async (_pubkey, ciphertext) => ciphertext.replace(/^cipher:/, ''));
    authMock.signWithAuth.mockImplementation(async (event) => ({ ...event, id: 'request-id', pubkey: authMock.authState.pubkey, sig: 'sig' }));
    systemMock.currentSystemInfo.mockReturnValue(legacyDiscovery());
    client = fakeClient();
    nostrClientMock.activeClient = client;
    module = await import('../../src/lib/nostr/encrypted-controlplane.js');
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
    module?.disconnectEncryptedControlplane?.();
    nostrClientMock.activeClient = null;
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

  it('sends a request too large for the relay to store as an ephemeral 21059 wrap', async () => {
    const constants = await import('../../src/lib/nostr/encrypted-controlplane-constants.js');
    const { nip44 } = await import('nostr-tools');
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: SERVICE_PUBKEY });

    const small = await transport.buildEncryptedRequestEvent({ operation: 'secrets.set', payload: { value: 'x'.repeat(1_000) }, requestId: 'small' });
    expect(small.kind).toBe(constants.CONTEXTVM_GIFT_WRAP_KIND);
    expect(small.content.length).toBeLessThanOrEqual(constants.STORED_EVENT_MAX_CONTENT_BYTES);

    // 45 KB of plaintext pads to 49,152 bytes, whose base64 exceeds 65,535.
    const large = await transport.buildEncryptedRequestEvent({ operation: 'secrets.set', payload: { value: 'x'.repeat(45_000) }, requestId: 'large' });
    expect(large.kind).toBe(constants.CONTEXTVM_EPHEMERAL_GIFT_WRAP_KIND);
    expect(large.content.length).toBeGreaterThan(constants.STORED_EVENT_MAX_CONTENT_BYTES);
    expect(large.tags).toEqual([['p', SERVICE_PUBKEY]]);
    const inner = JSON.parse(nip44.v2.decrypt(large.content, nip44.v2.utils.getConversationKey(SERVICE_SECRET_KEY, large.pubkey)));
    expect(JSON.parse(inner.content).params.value).toHaveLength(45_000);

    await expect(transport.publishEncryptedRequest(large)).resolves.toMatchObject({ requestEventId: large.id });
    expect(client.publish).toHaveBeenCalledWith(large);
  });

  it('refuses a request over the NIP-44 limit before publishing, pointing at Blossom', async () => {
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: SERVICE_PUBKEY });

    await expect(transport.requestEncryptedResult({ operation: 'secrets.set', payload: { value: 'x'.repeat(70_000) } }))
      .rejects.toThrow(/over the 65535-byte NIP-44 encryption limit; send large documents by reference \(upload them to Blossom/);
    expect(client.publish).not.toHaveBeenCalled();
  });

  it('refuses an event over the relay message limit before publishing instead of losing the connection', async () => {
    const constants = await import('../../src/lib/nostr/encrypted-controlplane-constants.js');
    const { relayMessageBytes } = await import('../../src/lib/nostr/encrypted-controlplane-utils.js');
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: SERVICE_PUBKEY });
    const base = { id: 'e'.repeat(64), pubkey: 'a'.repeat(64), sig: 'f'.repeat(128), kind: module.CONTEXTVM_MESSAGE_KIND, created_at: 1, tags: [], content: '' };
    const overhead = relayMessageBytes(base);
    const atLimit = { ...base, content: 'x'.repeat(constants.CONTEXTVM_MAX_RELAY_MESSAGE_BYTES - overhead) };
    const overLimit = { ...base, content: 'x'.repeat(constants.CONTEXTVM_MAX_RELAY_MESSAGE_BYTES - overhead + 1) };
    expect(relayMessageBytes(atLimit)).toBe(constants.CONTEXTVM_MAX_RELAY_MESSAGE_BYTES);

    await expect(transport.publishEncryptedRequest(overLimit)).rejects.toThrow(/over the 512000-byte relay message limit; send large documents by reference \(upload them to Blossom/);
    expect(client.publish).not.toHaveBeenCalled();
    await expect(transport.publishEncryptedRequest(atLimit)).resolves.toMatchObject({ requestEventId: atLimit.id });
    expect(client.publish).toHaveBeenCalledWith(atLimit);
  });

  it('uses connected relay NIP-11 message and stored-content limits', async () => {
    global.fetch.mockResolvedValue({ ok: true, json: async () => ({ limitation: { max_message_length: 1000, max_content_length: 100 } }) });
    client.getConnectedRelays.mockReturnValue(['wss://limited.example']);
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://limited.example'], servicePubkey: SERVICE_PUBKEY });
    await transport.connect();
    const { relayLimits } = await import('../../src/lib/nostr/relay-nip11.js');
    await relayLimits.resolve(['wss://limited.example']);
    const event = await transport.buildEncryptedRequestEvent({ operation: 'secrets.set', payload: { value: 'abc' } });
    expect(event.kind).toBe(module.CONTEXTVM_EPHEMERAL_GIFT_WRAP_KIND);
    await expect(transport.publishEncryptedRequest(event)).rejects.toThrow('1000-byte relay message limit');
    expect(client.publish).not.toHaveBeenCalled();
  });

  it('connects without waiting for a stalled NIP-11 fetch', async () => {
    global.fetch.mockImplementation(() => new Promise(() => {}));
    client.getConnectedRelays.mockReturnValue(['wss://stalled.example']);
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://stalled.example'], servicePubkey: SERVICE_PUBKEY });
    await expect(transport.connect()).resolves.toBe(transport);
    expect(transport.connected).toBe(true);
    expect(global.fetch).toHaveBeenCalledOnce();
  });

  it('publishes an sbom/import at the inline limit as one relay message', async () => {
    // MAX_CONTEXTVM_INLINE_SBOM_BYTES; public-controlplane.test.js pins the value.
    const MAX_CONTEXTVM_INLINE_SBOM_BYTES = 360 * 1024;
    const { relayMessageBytes } = await import('../../src/lib/nostr/encrypted-controlplane-utils.js');
    const constants = await import('../../src/lib/nostr/encrypted-controlplane-constants.js');
    const requester = Uint8Array.from({ length: 32 }, () => 0x7c);
    authMock.signWithAuth.mockImplementation(async (event) => finalizeEvent(event, requester));
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: SERVICE_PUBKEY });
    const payloadBase64 = Buffer.alloc(MAX_CONTEXTVM_INLINE_SBOM_BYTES).toString('base64');
    const digest = `sha256:${'ab'.repeat(32)}`;

    const event = await transport.buildEncryptedRequestEvent({
      operation: 'sbom/import',
      kind: module.CONTEXTVM_MESSAGE_KIND,
      tags: [['domain', 'sbom'], ['operation', 'sbom/import'], ['subject_type', 'artifact'], ['artifact', 'artifact-1'], ['subject', digest], ['format', 'spdx'], ['generator', 'web-import']],
      payload: {
        idempotencyKey: `web.sbom.import:artifact:artifact-1:${digest}:spdx:inline:${MAX_CONTEXTVM_INLINE_SBOM_BYTES}:${payloadBase64.slice(0, 24)}:${payloadBase64.slice(-24)}:web-import`,
        subject: { type: 'artifact', id: 'artifact-1', display_name: 'registry.example.com/acme/some-service-with-a-long-name', digest },
        format: 'spdx',
        payloadBase64,
        storage: 'blossom',
        generator: { id: 'web-import' }
      }
    });

    expect(event.kind).toBe(module.CONTEXTVM_MESSAGE_KIND);
    expect(relayMessageBytes(event)).toBeLessThanOrEqual(constants.CONTEXTVM_MAX_RELAY_MESSAGE_BYTES);
    await expect(transport.publishEncryptedRequest(event)).resolves.toMatchObject({ requestEventId: event.id });
  });

  it('publishes through the encrypted-request client and requires an accepted OK', async () => {
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: SERVICE_PUBKEY });
    const event = { id: 'request-id', kind: module.ENCRYPTED_REQUEST_KIND, tags: [], content: 'cipher' };

    await expect(transport.publishEncryptedRequest(event)).resolves.toMatchObject({ requestEventId: 'request-id' });
    expect(client.connect).toHaveBeenCalledWith(['wss://requests.example']);
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

    expect(nostrClientMock.activeClient).toBe(client);
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
      content: `cipher:${JSON.stringify(finalizeEvent({
        kind: module.CONTEXTVM_MESSAGE_KIND,
        created_at: Math.floor(Date.now() / 1000),
        tags: [['e', 'req-spoofed'], ['p', 'a'.repeat(64)]],
        content: '{"jsonrpc":"2.0","id":"req-spoofed","result":{"ok":true}}'
      }, FORGER_SECRET_KEY))}`
    });

    await expect(promise).rejects.toThrow('inner event was not signed by the expected service pubkey');
  });

  it('rejects service-attributed inner results whose signature does not verify', async () => {
    let handlers;
    client.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
      return vi.fn();
    });
    const transport = new module.EncryptedControlplaneTransport({ client, relays: ['wss://requests.example'], servicePubkey: SERVICE_PUBKEY });
    const signed = finalizeEvent({
      kind: module.CONTEXTVM_MESSAGE_KIND,
      created_at: Math.floor(Date.now() / 1000),
      tags: [['e', 'req-forged-sig'], ['p', 'a'.repeat(64)]],
      content: '{"jsonrpc":"2.0","id":"req-forged-sig","result":{"ok":true}}'
    }, SERVICE_SECRET_KEY);

    for (const [requestEventId, inner] of [
      ['req-forged-sig', { ...signed, sig: '0'.repeat(128) }],
      ['req-forged-sig', { ...signed, sig: `${signed.sig.slice(0, -2)}${signed.sig.endsWith('00') ? '11' : '00'}` }]
    ]) {
      const promise = transport.awaitEncryptedResult({ requestEventId });
      await handlers.onEvent({
        id: `result-${requestEventId}`,
        kind: module.CONTEXTVM_GIFT_WRAP_KIND,
        pubkey: SERVICE_PUBKEY,
        tags: [['e', requestEventId], ['p', 'a'.repeat(64)]],
        content: `cipher:${JSON.stringify(inner)}`
      });
      await expect(promise).rejects.toThrow('event signature is invalid');
    }
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

  it('keeps waiting for the terminal result when the advertised progress ack is missing', async () => {
    vi.useFakeTimers();
    systemMock.currentSystemInfo.mockReturnValue(progressAckDiscovery());
    let handlers;
    client.subscribe.mockImplementation((_filters, nextHandlers) => {
      handlers = nextHandlers;
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
    let settled = false;
    promise.then(() => { settled = true; }, () => { settled = true; });

    await flushAsync();
    expect(client.publish).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(11);
    expect(settled).toBe(false);

    const requestEventId = client.publish.mock.calls[0][0].id;
    await handlers.onEvent(resultEvent(module, requestEventId, {
      jsonrpc: '2.0',
      id: 'ctx-silent',
      result: { ok: true }
    }));

    await expect(promise).resolves.toMatchObject({ result: { ok: true } });
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
