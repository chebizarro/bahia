import { describe, it, expect, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';

// Mock browser environment
global.window = global;
global.WebSocket = vi.fn();

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
    close: vi.fn((urls = []) => {
      for (const url of urls) {
        const relay = relayMap.get(url);
        if (relay) relay.connected = false;
      }
    }),
    destroy: vi.fn()
  };
}

async function flushPromises() {
  await Promise.resolve();
  await Promise.resolve();
}

describe('Nostr Client - Parsing Functions', () => {
  let parseSoulEvent;
  let parseTemplateEvent;
  let KINDS;
  let isOpenInteropKind;
  let isReadableKind;
  let BAHIA_READ_MODEL_KINDS;
  let BAHIA_STATUS_KINDS;
  let BAHIA_STATE_SCHEMAS;
  let replaceableKey;
  let shouldAcceptReplaceableEvent;
  let isReplaceableTombstone;
  let dedupeReplaceableEvents;
  let parseRuntimeCapabilityEvent;
  let runtimeCapabilitySupports;
  let normalizeSoulDraftContent;
  let createNostrPoolClient;

  beforeEach(async () => {
    // Reset modules to avoid state leakage
    vi.resetModules();

    // Dynamically import the nostr client
    const module = await import('../../src/lib/nostr/client.js');
    parseSoulEvent = module.parseSoulEvent;
    parseTemplateEvent = module.parseTemplateEvent;
    KINDS = module.KINDS;
    BAHIA_READ_MODEL_KINDS = module.BAHIA_READ_MODEL_KINDS;
    BAHIA_STATUS_KINDS = module.BAHIA_STATUS_KINDS;
    BAHIA_STATE_SCHEMAS = module.BAHIA_STATE_SCHEMAS;
    replaceableKey = module.replaceableKey;
    shouldAcceptReplaceableEvent = module.shouldAcceptReplaceableEvent;
    isReplaceableTombstone = module.isReplaceableTombstone;
    isOpenInteropKind = module.isOpenInteropKind;
    isReadableKind = module.isReadableKind;
    dedupeReplaceableEvents = module.dedupeReplaceableEvents;
    parseRuntimeCapabilityEvent = module.parseRuntimeCapabilityEvent;
    runtimeCapabilitySupports = module.runtimeCapabilitySupports;
    normalizeSoulDraftContent = module.normalizeSoulDraftContent;
    createNostrPoolClient = module.createNostrPoolClient;
    global.WebSocket.OPEN = 1;
    global.WebSocket.CONNECTING = 0;
  });

  describe('KINDS constants', () => {
    it('should export correct Soul Factory event kinds', () => {
      expect(KINDS.SOUL_TEMPLATE).toBe(31950);
      expect(KINDS.AGENT_SOUL).toBe(31951);
      expect(KINDS.SOUL_DRAFT).toBe(31952);
      expect(KINDS.PROVISIONING_REQUEST).toBe(5950);
      expect(KINDS.PROVISIONING_STATUS).toBe(6950);
      expect(KINDS.PROVISIONING_RESULT).toBe(7950);
      expect(KINDS.SOUL_ACTION).toBe(1950);
      expect(KINDS.SOUL_ACTION_LEGACY_RESULT).toBe(1951);
      expect(KINDS.RUNTIME_CAPABILITY).toBe(30317);
      expect(KINDS.RUNTIME_CONTROL_REQUEST).toBe(38384);
      expect(KINDS.RUNTIME_CONTROL_RESULT).toBe(38386);
    });

    it('classifies Soul Factory kinds as sidecar-readable open interop', () => {
      for (const kind of [
        KINDS.SOUL_TEMPLATE,
        KINDS.AGENT_SOUL,
        KINDS.SOUL_DRAFT,
        KINDS.PROVISIONING_REQUEST,
        KINDS.PROVISIONING_STATUS,
        KINDS.PROVISIONING_RESULT,
        KINDS.SOUL_ACTION,
        KINDS.SOUL_ACTION_LEGACY_RESULT,
        KINDS.RUNTIME_CAPABILITY,
        KINDS.RUNTIME_CONTROL_REQUEST,
        KINDS.RUNTIME_CONTROL_RESULT
      ]) {
        expect(isOpenInteropKind(kind)).toBe(true);
        expect(isReadableKind(kind)).toBe(true);
      }
    });

    it('exports canonical Bahia controlplane kinds and schema identifiers, not old Bahia aliases', () => {
      expect(KINDS.CASCADIA_CONTROLPLANE_STATE).toBe(30900);
      expect(KINDS.CONTEXTVM_MESSAGE).toBe(25910);
      expect(KINDS.NIP38_STATUS).toBe(30315);
      expect(KINDS.NIP51_RELAY_SET).toBe(30002);
      expect(KINDS.LOOM_WORKER_AD).toBe(10100);
      expect(BAHIA_STATE_SCHEMAS.SERVICE_REGISTRY).toBe('bahia.registry.service.v1');
      expect(BAHIA_STATE_SCHEMAS.LLM_ROUTE_STATE).toBe('bahia.state.llm-route.v1');
      expect(BAHIA_STATE_SCHEMAS.WORKER_STATE).toBe('bahia.state.worker.v1');
      expect(KINDS.BAHIA_SERVICE_STATE).toBeUndefined();
      expect(KINDS.BAHIA_SERVICE_REGISTRY).toBeUndefined();
      expect(KINDS.BAHIA_REQUEST_LLM_DEPLOY).toBeUndefined();
      expect(KINDS.BAHIA_LLM_DEPLOYMENT_RESULT).toBeUndefined();
    });

    it('uses canonical ContextVM observables in Bahia subscription groups', () => {
      expect(BAHIA_READ_MODEL_KINDS).toEqual(expect.arrayContaining([30900, 11316, 11317, 11318, 11319, 11320, 30002]));
      expect(BAHIA_READ_MODEL_KINDS).not.toContain(31964);
      expect(BAHIA_READ_MODEL_KINDS).not.toContain(31965);
      expect(BAHIA_READ_MODEL_KINDS).not.toContain(32000);
      expect(BAHIA_STATUS_KINDS).toEqual([30315]);
    });
  });

  describe('replaceable event helpers', () => {
    it('should build replaceable keys using kind, pubkey, and d-tag', () => {
      const pubkey = 'a'.repeat(64);
      expect(replaceableKey({ kind: 30900, pubkey, tags: [['d', 'svc-1']] })).toBe(`30900:${pubkey}:svc-1`);
      expect(replaceableKey({ kind: 10100, pubkey, tags: [] })).toBe(`10100:${pubkey}`);
    });

    it('should accept latest replaceable events and reject stale duplicates', () => {
      const existing = { id: 'old', created_at: 100 };
      expect(shouldAcceptReplaceableEvent(existing, { id: 'new', created_at: 101 })).toBe(true);
      expect(shouldAcceptReplaceableEvent(existing, { id: 'stale', created_at: 99 })).toBe(false);
      expect(shouldAcceptReplaceableEvent(existing, { id: 'old', created_at: 100 })).toBe(false);
    });

    it('should detect replaceable tombstones from content or tags', () => {
      expect(isReplaceableTombstone({ content: JSON.stringify({ deleted: true }), tags: [] })).toBe(true);
      expect(isReplaceableTombstone({ content: '{}', tags: [['deleted', 'true']] })).toBe(true);
      expect(isReplaceableTombstone({ content: '{}', tags: [['deleted', 'false']] })).toBe(false);
    });

    it('should dedupe replaceable events by latest created_at and omit tombstones', () => {
      const pubkey = 'a'.repeat(64);
      const events = [
        { id: 'old', kind: 31951, pubkey, created_at: 100, tags: [['d', 'agent-1']], content: '{}' },
        { id: 'new', kind: 31951, pubkey, created_at: 200, tags: [['d', 'agent-1']], content: '{}' },
        { id: 'deleted', kind: 31952, pubkey, created_at: 300, tags: [['d', 'draft-1']], content: JSON.stringify({ deleted: true }) }
      ];

      expect(dedupeReplaceableEvents(events).map((event) => event.id)).toEqual(['new']);
    });
  });

  describe('soul draft parsing', () => {
    it('normalizes partial identity objects with legacy top-level fields', () => {
      expect(normalizeSoulDraftContent({
        identity: { name: 'Scout' },
        purpose: 'Observe',
        tier: 'heavy',
        nip05: 'scout@example.com'
      }).identity).toEqual({
        name: 'Scout',
        purpose: 'Observe',
        tier: 'heavy',
        nip05: 'scout@example.com'
      });
    });

    it('preserves v2 customization specs and derives legacy asset refs', () => {
      const normalized = normalizeSoulDraftContent({
        schema: 'soulfactory-draft/v2',
        identity: { name: 'Scout', theme: 'warm', emoji: '🔍' },
        avatar: {
          current: 'generated',
          generated_ref: 'blossom:avatar',
          generation: { prompt: 'owl researcher', style_preset: 'pixel-art' }
        },
        voice: { provider: 'openai', persona_id: 'scout-voice' },
        memory: { embedding_provider: 'voyage', search: { top_k: 10 } },
        persona: { traits: ['curious'], tone: 'friendly professional' }
      });

      expect(normalized.identity).toMatchObject({ name: 'Scout', theme: 'warm', emoji: '🔍' });
      expect(normalized.avatar.generated_ref).toBe('blossom:avatar');
      expect(normalized.voice.provider).toBe('openai');
      expect(normalized.memory.search.top_k).toBe(10);
      expect(normalized.persona.traits).toEqual(['curious']);
      expect(normalized.assets.avatar_ref).toBe('blossom:avatar');
    });
  });

  describe('runtime capability parsing', () => {
    it('parses compatible 30317 runtime capability announcements', () => {
      const event = {
        id: 'cap-openclaw',
        kind: 30317,
        pubkey: 'runtime-pubkey',
        created_at: 1715700000,
        tags: [
          ['d', 'openclaw-main'],
          ['runtime', 'openclaw'],
          ['schema', 'soulfactory-runtime-capability/v1'],
          ['control-schema', 'soulfactory-runtime-control/v1'],
          ['method', 'soulfactory.provision'],
          ['method', 'soulfactory.update'],
          ['controller', 'controller-pubkey'],
          ['relay', 'wss://control.example', 'control']
        ],
        content: JSON.stringify({
          schema: 'soulfactory-runtime-capability/v1',
          runtime: 'openclaw',
          methods: ['soulfactory.provision'],
          control_schema: 'soulfactory-runtime-control/v1',
          relay_hints: { read: ['wss://read.example'] }
        })
      };

      const capability = parseRuntimeCapabilityEvent(event);

      expect(capability).toMatchObject({
        id: 'cap-openclaw',
        runtime: 'openclaw',
        coordinate: '30317:runtime-pubkey:openclaw-main',
        compatible: true
      });
      expect(capability.methods).toEqual(['soulfactory.provision', 'soulfactory.update']);
      expect(capability.controllerPubkeys).toEqual(['controller-pubkey']);
      expect(capability.relayHints.control).toEqual(['wss://control.example']);
      expect(capability.relayHints.read).toEqual(['wss://read.example']);
      expect(runtimeCapabilitySupports(capability, { runtime: 'openclaw', method: 'soulfactory.update', controllerPubkey: 'controller-pubkey' })).toBe(true);
      expect(runtimeCapabilitySupports(capability, { runtime: 'metiq' })).toBe(false);
    });

    it('dedupes runtime capability announcements with replaceable latest-wins semantics', () => {
      const old = {
        id: 'old-cap',
        kind: 30317,
        pubkey: 'runtime-pubkey',
        created_at: 100,
        tags: [['d', 'openclaw'], ['runtime', 'openclaw']],
        content: JSON.stringify({ schema: 'soulfactory-runtime-capability/v1', runtime: 'openclaw', control_schema: 'soulfactory-runtime-control/v1', methods: ['soulfactory.provision'] })
      };
      const latest = {
        ...old,
        id: 'new-cap',
        created_at: 200,
        content: JSON.stringify({ schema: 'soulfactory-runtime-capability/v1', runtime: 'openclaw', control_schema: 'soulfactory-runtime-control/v1', methods: ['soulfactory.update'] })
      };

      const capabilities = dedupeReplaceableEvents([old, latest]).map(parseRuntimeCapabilityEvent).filter(Boolean);

      expect(capabilities.map((capability) => capability.id)).toEqual(['new-cap']);
      expect(capabilities.filter((capability) => runtimeCapabilitySupports(capability, { method: 'soulfactory.update' }))).toHaveLength(1);
    });
  });

  describe('pool subscriptions', () => {
    it('keeps subscriptions active until relay EOSE callbacks arrive', async () => {
      const relay = createRelay('ws://relay.example');
      const pool = createPool([relay]);
      const client = createNostrPoolClient({ relays: ['ws://relay.example'], pool });
      const onEose = vi.fn();

      client.subscribe([{ kinds: [30900] }], { onEose });
      await flushPromises();

      expect(pool.ensureRelay).toHaveBeenCalledWith('ws://relay.example');
      expect(relay.subscribe).toHaveBeenCalledWith([{ kinds: [30900] }], expect.objectContaining({ id: 'sub_1' }));
      expect(onEose).not.toHaveBeenCalled();

      relay.subscriptions[0].params.oneose();
      await flushPromises();
      expect(onEose).toHaveBeenCalledWith('ws://relay.example');
    });

    it('surfaces relay CLOSED before EOSE through onClosed metadata', async () => {
      const relay = createRelay('ws://relay.example');
      const pool = createPool([relay]);
      const client = createNostrPoolClient({ relays: ['ws://relay.example'], pool });
      const onClosed = vi.fn();

      client.subscribe([{ kinds: [30900] }], { onClosed });
      await flushPromises();
      relay.subscriptions[0].params.onclose('relay reconnect attempts exhausted before EOSE');
      await flushPromises();

      expect(onClosed).toHaveBeenCalledWith('relay reconnect attempts exhausted before EOSE', 'ws://relay.example', {
        terminal: true,
        source: 'closed',
        authRequired: false
      });
    });

    it('marks AUTH-required relays and lets remaining relays reach EOSE', async () => {
      const authRelay = createRelay('ws://auth.example');
      const openRelay = createRelay('ws://open.example');
      const pool = createPool([authRelay, openRelay]);
      const client = createNostrPoolClient({ relays: ['ws://auth.example', 'ws://open.example'], pool });
      const onClosed = vi.fn();
      const onEose = vi.fn();

      client.subscribe([{ kinds: [30900] }], { onClosed, onEose });
      await flushPromises();
      authRelay.subscriptions[0].params.onclose('auth-required: sign in first');
      openRelay.subscriptions[0].params.oneose();
      await flushPromises();

      expect(onClosed).toHaveBeenCalledWith('auth-required: sign in first', 'ws://auth.example', {
        terminal: true,
        source: 'auth',
        authRequired: true
      });
      expect(onEose).toHaveBeenCalledWith('ws://open.example');
      expect(get(client.connectionStatus)['ws://auth.example']).toBe('auth-required');
    });

    it('does not classify non-prefix auth-required text as NIP-42 AUTH', async () => {
      const relay = createRelay('ws://relay.example');
      const pool = createPool([relay]);
      const client = createNostrPoolClient({ relays: ['ws://relay.example'], pool });
      const onClosed = vi.fn();

      client.subscribe([{ kinds: [30900] }], { onClosed });
      await flushPromises();
      relay.subscriptions[0].params.onclose('closed: not auth-required; maintenance');
      await flushPromises();

      expect(onClosed).toHaveBeenCalledWith('closed: not auth-required; maintenance', 'ws://relay.example', {
        terminal: true,
        source: 'closed',
        authRequired: false
      });
      expect(get(client.connectionStatus)['ws://relay.example']).toBe('connected');
    });

    it('re-REQs after CLOSED with a bounded replay cursor and resumes events', async () => {
      vi.useFakeTimers();
      try {
        const relay = createRelay('ws://relay.example');
        const pool = createPool([relay]);
        const client = createNostrPoolClient({ relays: ['ws://relay.example'], pool, validateEvent: null });
        const onEvent = vi.fn();
        const onClosed = vi.fn();

        const unsubscribe = client.subscribeWithRecovery(
          [{ kinds: [30900] }],
          { onEvent, onClosed },
          { initialDelayMs: 100, maxDelayMs: 1000, jitterRatio: 0, disconnectAfterFailures: 3 }
        );
        await flushPromises();

        relay.subscriptions[0].params.onevent({ id: 'event-1', created_at: 100 });
        await flushPromises();
        relay.subscriptions[0].params.onclose('rate-limited');
        await flushPromises();

        expect(onClosed).toHaveBeenCalledWith('rate-limited', 'ws://relay.example', expect.objectContaining({
          terminal: false,
          recovering: true,
          disconnected: false,
          consecutiveFailures: 1,
          retryInMs: 100
        }));

        await vi.advanceTimersByTimeAsync(100);
        await flushPromises();
        expect(relay.subscriptions).toHaveLength(2);
        expect(relay.subscriptions[1].filters).toEqual([{ kinds: [30900], since: 99 }]);

        relay.subscriptions[1].params.onevent({ id: 'event-2', created_at: 101 });
        await flushPromises();
        expect(onEvent.mock.calls.map(([event]) => event.id)).toEqual(['event-1', 'event-2']);

        unsubscribe();
      } finally {
        vi.useRealTimers();
      }
    });

    it('resets recovery backoff after EOSE', async () => {
      vi.useFakeTimers();
      try {
        const relay = createRelay('ws://relay.example');
        const client = createNostrPoolClient({
          relays: ['ws://relay.example'],
          pool: createPool([relay]),
          validateEvent: null
        });
        const unsubscribe = client.subscribeWithRecovery(
          [{ kinds: [30900] }],
          {},
          { initialDelayMs: 100, maxDelayMs: 1000, jitterRatio: 0 }
        );
        await flushPromises();

        relay.subscriptions[0].params.onclose('first failure');
        await flushPromises();
        await vi.advanceTimersByTimeAsync(100);
        await flushPromises();

        relay.subscriptions[1].params.onclose('second failure');
        await flushPromises();
        await vi.advanceTimersByTimeAsync(199);
        expect(relay.subscriptions).toHaveLength(2);
        await vi.advanceTimersByTimeAsync(1);
        await flushPromises();
        expect(relay.subscriptions).toHaveLength(3);

        relay.subscriptions[2].params.oneose();
        await flushPromises();
        relay.subscriptions[2].params.onclose('failure after healthy EOSE');
        await flushPromises();
        await vi.advanceTimersByTimeAsync(99);
        expect(relay.subscriptions).toHaveLength(3);
        await vi.advanceTimersByTimeAsync(1);
        await flushPromises();
        expect(relay.subscriptions).toHaveLength(4);

        unsubscribe();
      } finally {
        vi.useRealTimers();
      }
    });
  });

  describe('publish', () => {
    it('resolves relay publish result using OK acceptance', async () => {
      const relay = createRelay('wss://relay.example');
      const pool = createPool([relay]);
      pool.publish.mockReturnValueOnce([Promise.resolve('accepted')]);
      const client = createNostrPoolClient({ relays: ['wss://relay.example'], pool });

      await expect(client.publish({ id: 'evt-1', kind: 5950, tags: [], content: '' })).resolves.toEqual([
        { relay: 'wss://relay.example', sent: true, accepted: true, message: 'accepted' }
      ]);
      expect(pool.publish).toHaveBeenCalledWith(['wss://relay.example'], { id: 'evt-1', kind: 5950, tags: [], content: '' }, {});
    });

    it('returns rejected result when relay rejects event', async () => {
      const relay = createRelay('wss://relay.example');
      const pool = createPool([relay]);
      pool.publish.mockReturnValueOnce([Promise.reject(new Error('auth required'))]);
      const client = createNostrPoolClient({ relays: ['wss://relay.example'], pool });

      await expect(client.publish({ id: 'evt-2', kind: 5950, tags: [], content: '' })).resolves.toEqual([
        { relay: 'wss://relay.example', sent: true, accepted: false, message: 'auth required' }
      ]);
    });

    it('returns send failure if relay closes before OK', async () => {
      const relay = createRelay('wss://relay.example');
      const pool = createPool([relay]);
      pool.publish.mockReturnValueOnce([Promise.reject(new Error('connection failure: relay connection closed'))]);
      const client = createNostrPoolClient({ relays: ['wss://relay.example'], pool });

      await expect(client.publish({ id: 'evt-3', kind: 5950, tags: [], content: '' })).resolves.toEqual([
        { relay: 'wss://relay.example', sent: false, accepted: false, message: 'connection failure: relay connection closed' }
      ]);
    });

    it('throws when publishing unsigned event without id', async () => {
      const client = createNostrPoolClient({ relays: [], pool: createPool() });
      await expect(client.publish({ kind: 5950, tags: [], content: '' })).rejects.toThrow(
        'Cannot publish event without id'
      );
    });
  });

  describe('subscribe lifecycle', () => {
    it('forwards relay AUTH challenges to the subscription onAuth handler', async () => {
      const relay = createRelay('wss://relay.example');
      relay.challenge = 'challenge-1';
      const client = createNostrPoolClient({ relays: ['wss://relay.example'], pool: createPool([relay]) });
      const authEvent = { id: 'auth-event' };
      const onAuth = vi.fn(async () => authEvent);

      client.subscribe([{ kinds: [30900] }], { onAuth });
      await flushPromises();

      await expect(relay.onauth({ kind: 22242, tags: [] })).resolves.toBe(authEvent);
      expect(onAuth).toHaveBeenCalledWith('challenge-1', 'wss://relay.example', { kind: 22242, tags: [] });
    });

    it('disconnect closes active subscriptions', async () => {
      const relay = createRelay('wss://relay.example');
      const pool = createPool([relay]);
      const client = createNostrPoolClient({ relays: ['wss://relay.example'], pool });

      client.subscribe([{ kinds: [30900] }], { onEvent: vi.fn() });
      await flushPromises();
      client.disconnect();
      await flushPromises();

      expect(relay.subscriptions[0].close).toHaveBeenCalledWith('closed by caller');
      expect(pool.destroy).toHaveBeenCalled();
    });
  });

  describe('connect', () => {
    it('reports relay connection progress and returns a summary', async () => {
      const relay = createRelay('wss://relay.example', { connected: false });
      const pool = createPool([relay]);
      let resolveConnection;
      pool.ensureRelay.mockImplementationOnce(() => new Promise((resolve) => {
        resolveConnection = () => {
          relay.connected = true;
          resolve(relay);
        };
      }));
      const client = createNostrPoolClient({ relays: ['wss://relay.example'], pool });

      const connectPromise = client.connect(['wss://relay.example']);
      expect(get(client.connectionStatus)).toEqual({
        'wss://relay.example': 'connecting'
      });

      resolveConnection();
      await expect(connectPromise).resolves.toEqual({
        total: 1,
        connected: 1,
        failed: 0,
        connecting: 0,
        relays: [
          { url: 'wss://relay.example', status: 'connected' }
        ]
      });
    });

    it('closes relay connections removed from configuration', async () => {
      const relay = createRelay('wss://relay.example');
      const pool = createPool([relay]);
      const client = createNostrPoolClient({ relays: ['wss://relay.example'], pool });

      await client.connect([]);

      expect(pool.close).toHaveBeenCalledWith(['wss://relay.example']);
      expect(get(client.connectionStatus)).toEqual({});
    });
  });


  describe('parseSoulEvent', () => {
    it('should parse minimal soul event with defaults', () => {
      const event = {
        id: 'event-id-1',
        pubkey: 'a'.repeat(64),
        created_at: 1714392000,
        content: 'Soul content',
        tags: [
          ['d', 'agent-id-alpha']
        ]
      };

      const soul = parseSoulEvent(event);

      expect(soul.id).toBe('event-id-1');
      expect(soul.pubkey).toBe('a'.repeat(64));
      expect(soul.createdAt).toBe(1714392000);
      expect(soul.content).toBe('Soul content');
      expect(soul.agentId).toBe('agent-id-alpha');
      expect(soul.name).toBe('');
      expect(soul.tier).toBe('standard');
      expect(soul.status).toBe('active');
      expect(soul.allowedKinds).toEqual([]);
      expect(soul.tools).toEqual([]);
    });

    it('should parse all standard soul tags', () => {
      const event = {
        id: 'event-id-2',
        pubkey: 'b'.repeat(64),
        created_at: 1714392100,
        content: 'Detailed soul',
        tags: [
          ['d', 'agent-beta'],
          ['name', 'Agent Beta'],
          ['purpose', 'Code review and analysis'],
          ['tier', 'heavy'],
          ['status', 'provisioning'],
          ['deploy-status', 'deploying'],
          ['npub', 'npub1abc...'],
          ['avatar', 'https://example.com/avatar.png'],
          ['nip05', 'agent@example.com'],
          ['workspace', 'workspace-id-123'],
          ['qdrant', 'qdrant-collection-456'],
          ['service', 'bahia-svc-789']
        ]
      };

      const soul = parseSoulEvent(event);

      expect(soul.agentId).toBe('agent-beta');
      expect(soul.name).toBe('Agent Beta');
      expect(soul.purpose).toBe('Code review and analysis');
      expect(soul.tier).toBe('heavy');
      expect(soul.status).toBe('provisioning');
      expect(soul.deployStatus).toBe('deploying');
      expect(soul.npub).toBe('npub1abc...');
      expect(soul.avatarUrl).toBe('https://example.com/avatar.png');
      expect(soul.nip05).toBe('agent@example.com');
      expect(soul.workspace).toBe('workspace-id-123');
      expect(soul.qdrant).toBe('qdrant-collection-456');
      expect(soul.bahiaServiceId).toBe('bahia-svc-789');
    });

    it('should parse agent pubkey from p tag with agent marker', () => {
      const event = {
        id: 'event-id-3',
        pubkey: 'c'.repeat(64),
        created_at: 1714392200,
        content: '',
        tags: [
          ['d', 'agent-gamma'],
          ['p', 'd'.repeat(64), 'agent'],
          ['p', 'e'.repeat(64), 'other']
        ]
      };

      const soul = parseSoulEvent(event);

      expect(soul.agentPubkey).toBe('d'.repeat(64));
    });

    it('should parse allowed kinds', () => {
      const event = {
        id: 'event-id-4',
        pubkey: 'e'.repeat(64),
        created_at: 1714392300,
        content: '',
        tags: [
          ['d', 'agent-delta'],
          ['allowed-kind', '1'],
          ['allowed-kind', '30023'],
          ['allowed-kind', '25910']
        ]
      };

      const soul = parseSoulEvent(event);

      expect(soul.allowedKinds).toEqual([1, 30023, 25910]);
    });

    it('should parse tool tags with scopes', () => {
      const event = {
        id: 'event-id-5',
        pubkey: 'f'.repeat(64),
        created_at: 1714392400,
        content: '',
        tags: [
          ['d', 'agent-epsilon'],
          ['tool', 'mcp-server-github', 'read', 'write'],
          ['tool', 'mcp-server-database', 'read']
        ]
      };

      const soul = parseSoulEvent(event);

      expect(soul.tools).toHaveLength(2);
      expect(soul.tools[0]).toEqual({
        server: 'mcp-server-github',
        scopes: ['read', 'write']
      });
      expect(soul.tools[1]).toEqual({
        server: 'mcp-server-database',
        scopes: ['read']
      });
    });

    it('should parse runtime-aware read model fields from content', () => {
      const event = {
        id: 'event-runtime-content',
        pubkey: 'f'.repeat(64),
        created_at: 1714392450,
        tags: [['d', 'agent-runtime'], ['draft', '31952:operator:agent-runtime'], ['spec-hash', 'sha256:spec']],
        content: JSON.stringify({
          runtime: { target: 'openclaw', runtime_pubkey: 'runtime-pubkey', capability_ref: '30317:runtime:openclaw', state: 'running' },
          permissions: { allowed_kinds: [1, 31952], tool_grants: [{ mcp_server: 'github', scopes: ['read'] }] },
          relay_policy: { control: ['wss://control.example'] },
          workspace: { repo: '30617:repo:bahia', branch: 'main' },
          assets: { avatar_ref: 'https://assets.example/avatar.png', voice_ref: 'blob:voice' }
        })
      };

      const soul = parseSoulEvent(event);

      expect(soul.runtime).toMatchObject({ target: 'openclaw', state: 'running' });
      expect(soul.capabilityRef).toBe('30317:runtime:openclaw');
      expect(soul.allowedKinds).toEqual([1, 31952]);
      expect(soul.tools).toEqual([{ server: 'github', scopes: ['read'] }]);
      expect(soul.relayPolicy.control).toEqual(['wss://control.example']);
      expect(soul.workspaceSpec.repo).toBe('30617:repo:bahia');
      expect(soul.avatarUrl).toBe('https://assets.example/avatar.png');
    });

    it('should handle soul with all possible statuses', () => {
      const statuses = ['active', 'provisioning', 'suspended', 'revoked', 'draft'];

      statuses.forEach((status, idx) => {
        const event = {
          id: `event-${idx}`,
          pubkey: 'g'.repeat(64),
          created_at: 1714392500 + idx,
          content: '',
          tags: [
            ['d', `agent-${idx}`],
            ['status', status]
          ]
        };

        const soul = parseSoulEvent(event);
        expect(soul.status).toBe(status);
      });
    });

    it('should handle event with no tags gracefully', () => {
      const event = {
        id: 'event-no-tags',
        pubkey: 'h'.repeat(64),
        created_at: 1714392600,
        content: 'Content only',
        tags: []
      };

      const soul = parseSoulEvent(event);

      expect(soul.agentId).toBe('');
      expect(soul.name).toBe('');
      expect(soul.status).toBe('active');
      expect(soul.tier).toBe('standard');
    });
  });

  describe('parseTemplateEvent', () => {
    it('should parse minimal template event with defaults', () => {
      const event = {
        id: 'template-id-1',
        pubkey: 'i'.repeat(64),
        created_at: 1714390000,
        content: 'Base prompt for agent',
        tags: [
          ['d', 'template-basic']
        ]
      };

      const template = parseTemplateEvent(event);

      expect(template.id).toBe('template-id-1');
      expect(template.pubkey).toBe('i'.repeat(64));
      expect(template.createdAt).toBe(1714390000);
      expect(template.identifier).toBe('template-basic');
      expect(template.basePrompt).toBe('Base prompt for agent');
      expect(template.name).toBe('');
      expect(template.description).toBe('');
      expect(template.tier).toBe('standard');
      expect(template.defaultKinds).toEqual([]);
      expect(template.defaultTools).toEqual([]);
      expect(template.tags).toEqual([]);
    });

    it('should parse all standard template tags', () => {
      const event = {
        id: 'template-id-2',
        pubkey: 'j'.repeat(64),
        created_at: 1714390100,
        content: 'You are a specialized code review agent...',
        tags: [
          ['d', 'template-code-review'],
          ['name', 'Code Review Agent'],
          ['description', 'An agent specialized in reviewing pull requests'],
          ['tier', 'heavy']
        ]
      };

      const template = parseTemplateEvent(event);

      expect(template.identifier).toBe('template-code-review');
      expect(template.name).toBe('Code Review Agent');
      expect(template.description).toBe('An agent specialized in reviewing pull requests');
      expect(template.tier).toBe('heavy');
      expect(template.basePrompt).toBe('You are a specialized code review agent...');
    });

    it('should parse template with tags', () => {
      const event = {
        id: 'template-id-3',
        pubkey: 'k'.repeat(64),
        created_at: 1714390200,
        content: 'Prompt text',
        tags: [
          ['d', 'template-tagged'],
          ['t', 'development'],
          ['t', 'automation'],
          ['t', 'code-quality']
        ]
      };

      const template = parseTemplateEvent(event);

      expect(template.tags).toEqual(['development', 'automation', 'code-quality']);
    });

    it('should parse template with default kinds', () => {
      const event = {
        id: 'template-id-4',
        pubkey: 'l'.repeat(64),
        created_at: 1714390300,
        content: 'Prompt',
        tags: [
          ['d', 'template-kinds'],
          ['default-kind', '1'],
          ['default-kind', '30023']
        ]
      };

      const template = parseTemplateEvent(event);

      expect(template.defaultKinds).toEqual([1, 30023]);
    });

    it('should handle template with all tier levels', () => {
      const tiers = ['lightweight', 'standard', 'heavy'];

      tiers.forEach((tier, idx) => {
        const event = {
          id: `template-tier-${idx}`,
          pubkey: 'm'.repeat(64),
          created_at: 1714390400 + idx,
          content: 'Tier test',
          tags: [
            ['d', `template-${tier}`],
            ['tier', tier]
          ]
        };

        const template = parseTemplateEvent(event);
        expect(template.tier).toBe(tier);
      });
    });

    it('should handle template with no identifier', () => {
      const event = {
        id: 'template-no-id',
        pubkey: 'n'.repeat(64),
        created_at: 1714390500,
        content: 'Prompt without identifier',
        tags: []
      };

      const template = parseTemplateEvent(event);

      expect(template.identifier).toBe('');
      expect(template.name).toBe('');
      expect(template.tier).toBe('standard');
    });

    it('should handle template with complex content', () => {
      const complexPrompt = `You are an AI agent with the following capabilities:

1. Code analysis
2. Documentation generation
3. Test creation

Always be helpful and precise.`;

      const event = {
        id: 'template-complex',
        pubkey: 'o'.repeat(64),
        created_at: 1714390600,
        content: complexPrompt,
        tags: [
          ['d', 'template-complex'],
          ['name', 'Complex Agent']
        ]
      };

      const template = parseTemplateEvent(event);

      expect(template.basePrompt).toBe(complexPrompt);
      expect(template.name).toBe('Complex Agent');
    });
  });

  describe('Parsing edge cases', () => {
    it('should handle soul event with duplicate tags', () => {
      const event = {
        id: 'event-duplicates',
        pubkey: 'p'.repeat(64),
        created_at: 1714392700,
        content: '',
        tags: [
          ['d', 'agent-first'],
          ['d', 'agent-duplicate'],
          ['name', 'First Name'],
          ['name', 'Second Name']
        ]
      };

      const soul = parseSoulEvent(event);

      // Should use last value
      expect(soul.agentId).toBe('agent-duplicate');
      expect(soul.name).toBe('Second Name');
    });

    it('should handle template event with duplicate tags', () => {
      const event = {
        id: 'template-duplicates',
        pubkey: 'q'.repeat(64),
        created_at: 1714390700,
        content: 'Prompt',
        tags: [
          ['d', 'first-id'],
          ['d', 'second-id'],
          ['tier', 'lightweight'],
          ['tier', 'heavy']
        ]
      };

      const template = parseTemplateEvent(event);

      expect(template.identifier).toBe('second-id');
      expect(template.tier).toBe('heavy');
    });

    it('should handle soul with malformed allowed-kind tags', () => {
      const event = {
        id: 'event-malformed-kinds',
        pubkey: 'r'.repeat(64),
        created_at: 1714392800,
        content: '',
        tags: [
          ['d', 'agent-malformed'],
          ['allowed-kind', 'not-a-number'],
          ['allowed-kind', '1'],
          ['allowed-kind', '']
        ]
      };

      const soul = parseSoulEvent(event);

      // parseInt will parse 'not-a-number' as NaN, '' as NaN
      expect(soul.allowedKinds).toContain(1);
      expect(soul.allowedKinds.length).toBe(3);
    });

    it('should handle tool tag with no scopes', () => {
      const event = {
        id: 'event-tool-no-scopes',
        pubkey: 's'.repeat(64),
        created_at: 1714392900,
        content: '',
        tags: [
          ['d', 'agent-tool-test'],
          ['tool', 'mcp-server-basic']
        ]
      };

      const soul = parseSoulEvent(event);

      expect(soul.tools).toHaveLength(1);
      expect(soul.tools[0]).toEqual({
        server: 'mcp-server-basic',
        scopes: []
      });
    });

    it('should preserve unknown soul tags gracefully', () => {
      const event = {
        id: 'event-unknown-tags',
        pubkey: 't'.repeat(64),
        created_at: 1714393000,
        content: '',
        tags: [
          ['d', 'agent-unknown'],
          ['unknown-tag', 'some-value'],
          ['future-feature', 'future-value']
        ]
      };

      // Should not throw
      expect(() => {
        const soul = parseSoulEvent(event);
        expect(soul.agentId).toBe('agent-unknown');
      }).not.toThrow();
    });

    it('should preserve unknown template tags gracefully', () => {
      const event = {
        id: 'template-unknown-tags',
        pubkey: 'u'.repeat(64),
        created_at: 1714390800,
        content: 'Prompt',
        tags: [
          ['d', 'template-unknown'],
          ['experimental', 'feature'],
          ['version', '2.0']
        ]
      };

      // Should not throw
      expect(() => {
        const template = parseTemplateEvent(event);
        expect(template.identifier).toBe('template-unknown');
      }).not.toThrow();
    });
  });
});
