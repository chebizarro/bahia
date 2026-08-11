import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest';

// Mock browser environment
global.window = global;

// Mock connection guard (shared relay connection utility)
vi.mock('../../src/lib/nostr/connection-guard.js', () => ({
  ensureRelayConnection: vi.fn(async () => {})
}));

// Mock the nostr client module
const nostrClientMockFactory = vi.hoisted(() => () => {
  const KINDS = {
    SOUL_TEMPLATE: 31950,
    AGENT_SOUL: 31951,
    SOUL_DRAFT: 31952,
    PROVISIONING_REQUEST: 5950,
    PROVISIONING_STATUS: 6950,
    PROVISIONING_RESULT: 7950,
    SOUL_ACTION: 1950,
    SOUL_ACTION_LEGACY_RESULT: 1951,
    RUNTIME_CAPABILITY: 30317
  };

  const mockNostr = {
    subscribe: vi.fn(),
    query: vi.fn(),
    publish: vi.fn()
  };

  const fetchSouls = vi.fn();
  const fetchTemplates = vi.fn();
  const fetchSoulDrafts = vi.fn();
  const fetchRuntimeCapabilities = vi.fn();
  const queryOrPartial = vi.fn();
  const readModelEvents = vi.fn((result) => Array.isArray(result) ? result : (result?.events || []));
  
  const parseSoulEvent = vi.fn((event) => ({
    id: event.id,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    agentId: event.tags.find(t => t[0] === 'd')?.[1] || '',
    name: event.tags.find(t => t[0] === 'name')?.[1] || '',
    status: event.tags.find(t => t[0] === 'status')?.[1] || 'active'
  }));

  const parseTemplateEvent = vi.fn((event) => ({
    id: event.id,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    identifier: event.tags.find(t => t[0] === 'd')?.[1] || '',
    name: event.tags.find(t => t[0] === 'name')?.[1] || '',
    tier: event.tags.find(t => t[0] === 'tier')?.[1] || 'standard'
  }));

  const parseSoulDraftEvent = vi.fn((event) => ({
    id: event.id,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    agentId: event.tags.find(t => t[0] === 'd')?.[1] || '',
    content: event.content ? JSON.parse(event.content) : {}
  }));

  const parseRuntimeCapabilityEvent = vi.fn((event) => ({
    id: event.id,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    runtime: event.tags.find(t => t[0] === 'runtime')?.[1] || 'openclaw',
    methods: ['soulfactory.provision'],
    compatible: true,
    event
  }));

  const getD = (event) => event.tags.find(t => t[0] === 'd')?.[1] || '';
  const keyFor = (event) => `${event.kind}:${event.pubkey}:${getD(event)}`;
  const upsertReplaceableEvent = vi.fn((map, event) => {
    const key = keyFor(event);
    const existing = map.get(key);
    if (existing && existing.id === event.id) return { accepted: false, key, deleted: false };
    if (existing && Number(existing.created_at || 0) > Number(event.created_at || 0)) return { accepted: false, key, deleted: false };
    map.set(key, event);
    return { accepted: true, key, deleted: false };
  });

  return {
    nostr: mockNostr,
    fetchSouls,
    fetchTemplates,
    fetchSoulDrafts,
    fetchRuntimeCapabilities,
    queryOrPartial,
    readModelEvents,
    parseSoulEvent,
    parseTemplateEvent,
    parseSoulDraftEvent,
    parseRuntimeCapabilityEvent,
    normalizeSoulDraftContent: vi.fn((content) => ({ ...content, identity: content.identity || {} })),
    upsertReplaceableEvent,
    isReplaceableTombstone: vi.fn((event) => event.content && JSON.parse(event.content).deleted === true),
    ensureRelayConnection: vi.fn(async () => {}),
    SOUL_LIFECYCLE_ACTIONS: { UPDATE: 'update' },
    SOUL_RUNTIME_METHODS: { PROVISION: 'soulfactory.provision', UPDATE: 'soulfactory.update' },
    KINDS
  };
});

vi.mock('../../src/lib/nostr/client.js', nostrClientMockFactory);
vi.mock('$lib/nostr/client.js', nostrClientMockFactory);

const authStoreMock = vi.hoisted(() => () => ({
  authState: { status: 'authenticated', pubkey: 'author-pubkey' },
  login: vi.fn(async () => {}),
  signWithAuth: vi.fn(async (event) => ({ ...event, id: 'signed-action-id' }))
}));

vi.mock('../../src/lib/stores/auth.js', authStoreMock);
vi.mock('$lib/stores/auth.js', authStoreMock);

describe('Souls Store', () => {
  let soulsModule;
  let mockNostr;
  let fetchSouls;
  let fetchTemplates;
  let fetchSoulDrafts;
  let fetchRuntimeCapabilities;
  let parseSoulEvent;
  let queryOrPartial;
  let parseTemplateEvent;
  let authModule;

  beforeEach(async () => {
    // Reset modules to get fresh store state
    vi.resetModules();
    vi.clearAllMocks();

    // Import mocked nostr client
    const nostrModule = await import('$lib/nostr/client.js');
    mockNostr = nostrModule.nostr;
    fetchSouls = nostrModule.fetchSouls;
    fetchTemplates = nostrModule.fetchTemplates;
    fetchSoulDrafts = nostrModule.fetchSoulDrafts;
    fetchRuntimeCapabilities = nostrModule.fetchRuntimeCapabilities;
    parseSoulEvent = nostrModule.parseSoulEvent;
    queryOrPartial = nostrModule.queryOrPartial;
    parseTemplateEvent = nostrModule.parseTemplateEvent;
    authModule = await import('$lib/stores/auth.js');

    // Set default mock implementations
    fetchSouls.mockResolvedValue([
      {
        id: 'soul-1',
        pubkey: 'pubkey-1',
        created_at: 1714392000,
        tags: [
          ['d', 'agent-id-1'],
          ['name', 'Agent Alpha'],
          ['status', 'active']
        ]
      },
      {
        id: 'soul-2',
        pubkey: 'pubkey-1',
        created_at: 1714392100,
        tags: [
          ['d', 'agent-id-2'],
          ['name', 'Agent Beta'],
          ['status', 'provisioning']
        ]
      }
    ]);

    fetchTemplates.mockResolvedValue([
      {
        id: 'template-1',
        pubkey: 'pubkey-1',
        created_at: 1714390000,
        tags: [
          ['d', 'template-standard'],
          ['name', 'Standard Agent'],
          ['tier', 'standard']
        ]
      }
    ]);

    fetchSoulDrafts.mockResolvedValue([]);
    fetchRuntimeCapabilities.mockResolvedValue([]);

    queryOrPartial.mockResolvedValue([]);
    mockNostr.subscribe.mockReturnValue(() => {});

    // Dynamically import souls module
    soulsModule = await import('../../src/lib/stores/souls.js');
  });

  afterEach(() => {
    // Clean up subscriptions
    if (soulsModule.unsubscribeFromSoulUpdates) {
      soulsModule.unsubscribeFromSoulUpdates();
    }
  });

  async function startSoulFactorySubscription(authorPubkey = null, entrypoint = 'subscribeToSoulUpdates') {
    const cleanup = vi.fn();
    mockNostr.subscribe.mockReturnValue(cleanup);

    await soulsModule[entrypoint](authorPubkey);

    expect(mockNostr.subscribe).toHaveBeenCalledTimes(1);
    const [filters, handlers] = mockNostr.subscribe.mock.calls.at(-1);
    return { filters, handlers, cleanup };
  }

  function applyBackfill(handlers, events) {
    for (const event of events) {
      handlers.onEvent(event);
    }
    handlers.onEose('wss://relay.example');
  }

  describe('soul factory subscription read models', () => {
    it('subscribes once to current soul factory read-model kinds without an author filter', async () => {
      const { filters, handlers } = await startSoulFactorySubscription();

      expect(filters).toEqual([
        { kinds: [31951, 31950, 31952] },
        { kinds: [30317] }
      ]);
      expect(handlers).toEqual(expect.objectContaining({
        onEvent: expect.any(Function),
        onEose: expect.any(Function),
        onClosed: expect.any(Function)
      }));
      expect(soulsModule.loading).toMatchObject({
        souls: true,
        templates: true,
        drafts: true,
        capabilities: true
      });

      handlers.onEose('wss://relay.example');

      expect(soulsModule.readModelMeta.souls).toMatchObject({
        complete: true,
        degraded: null,
        relaySummary: [expect.objectContaining({ relay: 'wss://relay.example', status: 'eose' })]
      });
      expect(soulsModule.readModelMeta.templates.complete).toBe(true);
      expect(soulsModule.readModelMeta.drafts.complete).toBe(true);
      expect(soulsModule.readModelMeta.capabilities.complete).toBe(true);
      expect(soulsModule.loading).toMatchObject({
        souls: false,
        templates: false,
        drafts: false,
        capabilities: false
      });
    });

    it('applies the author filter to replaceable soul/template/draft events only', async () => {
      const authorPubkey = 'author-all-789';

      const { filters } = await startSoulFactorySubscription(authorPubkey, 'loadAll');

      expect(filters).toEqual([
        { kinds: [31951, 31950, 31952], authors: [authorPubkey] },
        { kinds: [30317] }
      ]);
    });

    it('applies stored EVENT callbacks, dedupes replaceable records, sorts read models, and marks ready on EOSE', async () => {
      const { handlers } = await startSoulFactorySubscription();

      applyBackfill(handlers, [
        {
          id: 'old-soul',
          kind: 31951,
          pubkey: 'factory',
          created_at: 100,
          tags: [['d', 'scout'], ['name', 'Old Scout'], ['status', 'active']]
        },
        {
          id: 'new-soul',
          kind: 31951,
          pubkey: 'factory',
          created_at: 200,
          tags: [['d', 'scout'], ['name', 'New Scout'], ['status', 'suspended']]
        },
        {
          id: 'agent-beta',
          kind: 31951,
          pubkey: 'factory',
          created_at: 300,
          tags: [['d', 'agent-beta'], ['name', 'Agent Beta'], ['status', 'provisioning']]
        },
        {
          id: 'template-z',
          kind: 31950,
          pubkey: 'factory',
          created_at: 50,
          tags: [['d', 'z-template'], ['name', 'Zebra']]
        },
        {
          id: 'template-a',
          kind: 31950,
          pubkey: 'factory',
          created_at: 60,
          tags: [['d', 'a-template'], ['name', 'Alpha']]
        },
        {
          id: 'draft-1',
          kind: 31952,
          pubkey: 'author-pubkey',
          created_at: 120,
          tags: [['d', 'scout']],
          content: JSON.stringify({ identity: { name: 'Scout' } })
        },
        {
          id: 'cap-1',
          kind: 30317,
          pubkey: 'runtime',
          created_at: 140,
          tags: [['runtime', 'openclaw']],
          content: '{}'
        }
      ]);

      expect(parseSoulEvent).toHaveBeenCalled();
      expect(parseTemplateEvent).toHaveBeenCalled();
      expect(soulsModule.souls.map((soul) => soul.agentId)).toEqual(['agent-beta', 'scout']);
      expect(soulsModule.souls.find((soul) => soul.agentId === 'scout')).toMatchObject({
        id: 'new-soul',
        name: 'New Scout',
        status: 'suspended'
      });
      expect(soulsModule.templates.map((template) => template.name)).toEqual(['Alpha', 'Zebra']);
      expect(soulsModule.drafts).toHaveLength(1);
      expect(soulsModule.drafts[0]).toMatchObject({ agentId: 'scout' });
      expect(soulsModule.runtimeCapabilities).toHaveLength(1);
      // Unknown server policy fails closed.
      expect(soulsModule.supportedRuntimeTargets()).toEqual([]);
      soulsModule.serverAgentRuntimes.push('openclaw');
      soulsModule.serverPolicy.known = true;
      expect(soulsModule.supportedRuntimeTargets()).toEqual(['openclaw']);
      expect(soulsModule.loading.souls).toBe(false);
    });

    it('keeps the persistent subscription open and applies live updates after EOSE', async () => {
      const { handlers } = await startSoulFactorySubscription();

      applyBackfill(handlers, [
        {
          id: 'soul-1',
          kind: 31951,
          pubkey: 'factory',
          created_at: 100,
          tags: [['d', 'agent-id-1'], ['name', 'Agent Alpha'], ['status', 'active']]
        }
      ]);

      handlers.onEvent({
        id: 'soul-1-updated',
        kind: 31951,
        pubkey: 'factory',
        created_at: 220,
        tags: [['d', 'agent-id-1'], ['name', 'Agent Alpha Updated'], ['status', 'suspended']]
      });
      handlers.onEvent({
        id: 'soul-2',
        kind: 31951,
        pubkey: 'factory',
        created_at: 230,
        tags: [['d', 'agent-id-2'], ['name', 'Agent Beta'], ['status', 'active']]
      });

      expect(soulsModule.souls.map((soul) => soul.agentId)).toEqual(['agent-id-2', 'agent-id-1']);
      expect(soulsModule.souls.find((soul) => soul.agentId === 'agent-id-1')).toMatchObject({
        name: 'Agent Alpha Updated',
        status: 'suspended'
      });
    });

    it('records degraded read-model metadata when CLOSED occurs before EOSE after partial events', async () => {
      const consoleWarn = vi.spyOn(console, 'warn').mockImplementation(() => {});
      const { handlers } = await startSoulFactorySubscription();

      handlers.onEvent({
        id: 'partial-soul',
        kind: 31951,
        pubkey: 'factory',
        created_at: 100,
        tags: [['d', 'partial'], ['name', 'Partial Soul'], ['status', 'active']]
      }, 'wss://relay.example');
      handlers.onClosed('relay closed before EOSE', 'wss://relay.example', { terminal: true, source: 'closed' });

      expect(soulsModule.loading.souls).toBe(false);
      expect(soulsModule.readModelMeta.souls).toMatchObject({
        complete: false,
        degraded: {
          incomplete: true,
          reason: 'closed',
          partialEventCount: 1,
          relaySummary: [expect.objectContaining({ relay: 'wss://relay.example', status: 'closed' })]
        }
      });
      expect(soulsModule.souls.map((soul) => soul.agentId)).toContain('partial');
      consoleWarn.mockRestore();
    });

    it('records AUTH-required closure metadata for soul factory read models', async () => {
      const consoleWarn = vi.spyOn(console, 'warn').mockImplementation(() => {});
      const { handlers } = await startSoulFactorySubscription();

      handlers.onClosed('auth-required: sign in', 'wss://auth.example', { terminal: true, source: 'auth', authRequired: true });

      expect(soulsModule.loading.souls).toBe(false);
      expect(soulsModule.readModelMeta.souls).toMatchObject({
        complete: false,
        degraded: {
          incomplete: true,
          reason: 'auth-required',
          authRequired: true,
          partialEventCount: 0,
          relaySummary: [expect.objectContaining({ relay: 'wss://auth.example', status: 'auth-required' })]
        }
      });
      consoleWarn.mockRestore();
    });

    it('records relay connection failures without opening a subscription', async () => {
      const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
      const nostrModule = await import('$lib/nostr/client.js');
      nostrModule.ensureRelayConnection.mockRejectedValueOnce(new Error('Relay connection failed'));

      await soulsModule.loadSouls();

      expect(consoleError).toHaveBeenCalledWith('[souls] Failed to connect:', expect.any(Error));
      expect(mockNostr.subscribe).not.toHaveBeenCalled();
      expect(soulsModule.error.value).toBe('Relay connection failed');
      expect(soulsModule.loading).toMatchObject({
        souls: false,
        templates: false,
        drafts: false,
        capabilities: false
      });

      consoleError.mockRestore();
    });

    it('clears a previous connection error on a successful subscription', async () => {
      const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
      const nostrModule = await import('$lib/nostr/client.js');
      nostrModule.ensureRelayConnection.mockRejectedValueOnce(new Error('Network error'));

      await soulsModule.loadSouls();
      expect(soulsModule.error.value).toBe('Network error');

      await soulsModule.loadSouls();

      expect(soulsModule.error.value).toBeNull();
      expect(mockNostr.subscribe).toHaveBeenCalledTimes(1);

      consoleError.mockRestore();
    });

    it('does not create duplicate subscriptions while the persistent reader is live', async () => {
      const cleanup = vi.fn();
      mockNostr.subscribe.mockReturnValue(cleanup);

      await soulsModule.subscribeToSoulUpdates();
      await soulsModule.subscribeToSoulUpdates();

      expect(mockNostr.subscribe).toHaveBeenCalledTimes(1);
      expect(cleanup).not.toHaveBeenCalled();
    });
  });

  describe('unsubscribeFromSoulUpdates', () => {
    it('should call cleanup function when unsubscribing', async () => {
      const cleanup = vi.fn();
      mockNostr.subscribe.mockReturnValue(cleanup);

      await soulsModule.subscribeToSoulUpdates();
      soulsModule.unsubscribeFromSoulUpdates();

      expect(cleanup).toHaveBeenCalledTimes(1);
    });

    it('should be safe to call when not subscribed', () => {
      expect(() => {
        soulsModule.unsubscribeFromSoulUpdates();
      }).not.toThrow();
    });
  });

  describe('trackProvisioningRun', () => {
    it('should initialize provisioning run in store', () => {
      const requestEventId = 'req-event-123';

      soulsModule.trackProvisioningRun(requestEventId, {});

      const runs = soulsModule.provisioningRuns;
      expect(runs.has(requestEventId)).toBe(true);
      
      const run = runs.get(requestEventId);
      expect(run.id).toBe(requestEventId);
      expect(run.status).toBe('pending');
      expect(run.progress.current).toBe(0);
      expect(run.progress.total).toBe(8);
    });

    it('should subscribe to status and result events', () => {
      const requestEventId = 'req-event-456';
      const { KINDS } = require('../../src/lib/nostr/client.js');

      soulsModule.trackProvisioningRun(requestEventId, {});

      expect(mockNostr.subscribe).toHaveBeenCalledWith(
        [
          { kinds: [KINDS.PROVISIONING_STATUS], '#e': [requestEventId] },
          { kinds: [KINDS.PROVISIONING_RESULT, KINDS.SOUL_ACTION_LEGACY_RESULT], '#e': [requestEventId] }
        ],
        expect.objectContaining({
          onEvent: expect.any(Function),
          onEose: expect.any(Function),
          onClosed: expect.any(Function)
        })
      );
    });

    it('should update run status on status event', () => {
      const requestEventId = 'req-event-789';
      
      let onEventCallback = null;
      mockNostr.subscribe.mockImplementation((filters, handlers) => {
        onEventCallback = handlers.onEvent;
        return () => {};
      });

      const onProgress = vi.fn();
      soulsModule.trackProvisioningRun(requestEventId, { onProgress });

      const { KINDS } = require('../../src/lib/nostr/client.js');
      
      const statusEvent = {
        id: 'status-1',
        kind: KINDS.PROVISIONING_STATUS,
        content: 'Creating Qdrant collection',
        tags: [
          ['step', 'qdrant'],
          ['progress', '3', '8']
        ]
      };

      onEventCallback(statusEvent);

      const runs = soulsModule.provisioningRuns;
      const run = runs.get(requestEventId);
      
      expect(run.status).toBe('running');
      expect(run.step).toBe('qdrant');
      expect(run.progress.current).toBe(3);
      expect(run.progress.total).toBe(8);
      expect(run.message).toBe('Creating Qdrant collection');

      expect(onProgress).toHaveBeenCalledWith({
        step: 'qdrant',
        progress: { current: 3, total: 8 },
        message: 'Creating Qdrant collection'
      });
    });

    it('should handle successful result event and call onComplete', () => {
      const requestEventId = 'req-event-success';
      
      let onEventCallback = null;
      const unsub = vi.fn();
      mockNostr.subscribe.mockImplementation((filters, handlers) => {
        onEventCallback = handlers.onEvent;
        return unsub;
      });

      const onComplete = vi.fn();
      soulsModule.trackProvisioningRun(requestEventId, { onComplete });

      const { KINDS } = require('../../src/lib/nostr/client.js');
      
      const resultEvent = {
        id: 'result-success-1',
        kind: KINDS.PROVISIONING_RESULT,
        content: '{"agentId":"agent-new","npub":"npub123"}',
        tags: [
          ['status', 'success'],
          ['soul', 'soul-event-id']
        ]
      };

      onEventCallback(resultEvent);

      const runs = soulsModule.provisioningRuns;
      const run = runs.get(requestEventId);
      
      expect(run.status).toBe('completed');
      expect(run.result.success).toBe(true);
      expect(run.result.data).toEqual({ agentId: 'agent-new', npub: 'npub123' });

      expect(onComplete).toHaveBeenCalledWith({ agentId: 'agent-new', npub: 'npub123' });
      expect(unsub).toHaveBeenCalledTimes(1);
    });

    it('should handle failed result event and call onError', () => {
      const requestEventId = 'req-event-fail';
      
      let onEventCallback = null;
      const unsub = vi.fn();
      mockNostr.subscribe.mockImplementation((filters, handlers) => {
        onEventCallback = handlers.onEvent;
        return unsub;
      });

      const onError = vi.fn();
      soulsModule.trackProvisioningRun(requestEventId, { onError });

      const { KINDS } = require('../../src/lib/nostr/client.js');
      
      const resultEvent = {
        id: 'result-error-1',
        kind: KINDS.PROVISIONING_RESULT,
        content: 'Qdrant collection creation failed',
        tags: [
          ['status', 'error']
        ]
      };

      onEventCallback(resultEvent);

      const runs = soulsModule.provisioningRuns;
      const run = runs.get(requestEventId);
      
      expect(run.status).toBe('failed');
      expect(run.result.success).toBe(false);
      expect(run.result.error).toBe('Qdrant collection creation failed');

      expect(onError).toHaveBeenCalledWith('Qdrant collection creation failed');
      expect(unsub).toHaveBeenCalledTimes(1);
    });

    it('should update pending message after EOSE while waiting for live updates', () => {
      const requestEventId = 'req-event-eose';

      let handlers = null;
      mockNostr.subscribe.mockImplementation((filters, incomingHandlers) => {
        handlers = incomingHandlers;
        return () => {};
      });

      soulsModule.trackProvisioningRun(requestEventId, {});
      handlers.onEose('wss://relay.example');

      const run = soulsModule.provisioningRuns.get(requestEventId);
      expect(run.message).toBe('Request published. Waiting for live provisioning updates…');
    });

    it('should keep run non-terminal when relay closes subscription', () => {
      const requestEventId = 'req-event-closed';

      let handlers = null;
      const unsub = vi.fn();
      mockNostr.subscribe.mockImplementation((filters, incomingHandlers) => {
        handlers = incomingHandlers;
        return unsub;
      });

      const onError = vi.fn();
      soulsModule.trackProvisioningRun(requestEventId, { onError });
      handlers.onClosed('auth required', 'wss://relay.example');

      const run = soulsModule.provisioningRuns.get(requestEventId);
      expect(run.status).toBe('pending');
      expect(run.result).toBeNull();
      expect(run.message).toBe('Relay closed this subscription: auth required. Waiting for an explicit provisioning result…');
      expect(onError).not.toHaveBeenCalled();
      expect(unsub).not.toHaveBeenCalled();
    });

    it('should not fail run solely because local time passes without relay updates', () => {
      vi.useFakeTimers();

      const requestEventId = 'req-event-timeout';
      const onError = vi.fn();
      soulsModule.trackProvisioningRun(requestEventId, { onError });

      vi.advanceTimersByTime(121000);

      const run = soulsModule.provisioningRuns.get(requestEventId);
      expect(run.status).toBe('pending');
      expect(run.result).toBeNull();
      expect(onError).not.toHaveBeenCalled();

      vi.useRealTimers();
    });

    it('should return cleanup function that removes run from store', () => {
      const requestEventId = 'req-event-cleanup';

      const cleanup = soulsModule.trackProvisioningRun(requestEventId, {});

      let runs = soulsModule.provisioningRuns;
      expect(runs.has(requestEventId)).toBe(true);

      cleanup();

      runs = soulsModule.provisioningRuns;
      expect(runs.has(requestEventId)).toBe(false);
    });
  });

  describe('drafts and capabilities', () => {
    it('subscription read model exposes compatible runtime targets from capability events', async () => {
      const { handlers } = await startSoulFactorySubscription();

      applyBackfill(handlers, [
        {
          id: 'cap-1',
          kind: 30317,
          pubkey: 'runtime',
          created_at: 100,
          tags: [['runtime', 'openclaw']],
          content: '{}'
        }
      ]);

      expect(soulsModule.runtimeCapabilities).toHaveLength(1);
      soulsModule.serverAgentRuntimes.push('openclaw');
      soulsModule.serverPolicy.known = true;
      expect(soulsModule.supportedRuntimeTargets({ method: 'soulfactory.provision' })).toEqual(['openclaw']);
    });

    it('supportedRuntimeMethods gates controls by advertised methods per runtime', async () => {
      const nostrModule = await import('$lib/nostr/client.js');
      nostrModule.parseRuntimeCapabilityEvent.mockImplementation((event) => ({
        id: event.id,
        pubkey: event.pubkey,
        createdAt: event.created_at,
        runtime: event.tags.find((t) => t[0] === 'runtime')?.[1] || 'unknown',
        methods: event.tags.filter((t) => t[0] === 'method').map((t) => t[1]),
        compatible: true,
        event
      }));
      const { handlers } = await startSoulFactorySubscription();

      applyBackfill(handlers, [
        {
          id: 'cap-oc',
          kind: 30317,
          pubkey: 'runtime-oc',
          created_at: 100,
          tags: [['runtime', 'openclaw'], ['method', 'soulfactory.provision'], ['method', 'soulfactory.config.reload']],
          content: '{}'
        },
        {
          id: 'cap-mq',
          kind: 30317,
          pubkey: 'runtime-mq',
          created_at: 100,
          tags: [['runtime', 'metiq'], ['method', 'soulfactory.provision']],
          content: '{}'
        }
      ]);

      soulsModule.serverAgentRuntimes.push('openclaw', 'metiq');
      soulsModule.serverPolicy.known = true;
      expect(soulsModule.supportedRuntimeMethods({ runtime: 'openclaw' })).toEqual(
        expect.arrayContaining(['soulfactory.provision', 'soulfactory.config.reload'])
      );
      expect(soulsModule.supportedRuntimeMethods({ runtime: 'metiq' })).toEqual(['soulfactory.provision']);
      expect(soulsModule.supportedRuntimeMethods({ runtime: 'unregistered-rt' })).toBeNull();
      expect(soulsModule.supportedRuntimeMethods({ runtime: 'openclaw', runtimePubkey: 'runtime-oc' })).toContain('soulfactory.config.reload');
      expect(soulsModule.supportedRuntimeMethods({ runtime: 'openclaw', runtimePubkey: 'other-pubkey' })).toBeNull();
    });

    it('intersects discovered targets with the server-enabled runtime policy', async () => {
      const nostrModule = await import('$lib/nostr/client.js');
      nostrModule.parseRuntimeCapabilityEvent.mockImplementation((event) => ({
        id: event.id,
        pubkey: event.pubkey,
        createdAt: event.created_at,
        runtime: event.tags.find((t) => t[0] === 'runtime')?.[1] || 'unknown',
        methods: event.tags.filter((t) => t[0] === 'method').map((t) => t[1]),
        compatible: true,
        event
      }));
      const { handlers } = await startSoulFactorySubscription();

      applyBackfill(handlers, [
        { id: 'cap-oc', kind: 30317, pubkey: 'runtime-oc', created_at: 100, tags: [['runtime', 'openclaw'], ['method', 'soulfactory.provision']], content: '{}' },
        { id: 'cap-mq', kind: 30317, pubkey: 'runtime-mq', created_at: 100, tags: [['runtime', 'metiq'], ['method', 'soulfactory.provision']], content: '{}' }
      ]);

      // Unknown policy fails closed even with live compatible capabilities.
      expect(soulsModule.supportedRuntimeTargets({ method: 'soulfactory.provision' })).toEqual([]);
      expect(soulsModule.supportedRuntimeMethods({ runtime: 'metiq' })).toBeNull();

      soulsModule.serverAgentRuntimes.push('metiq');
      soulsModule.serverPolicy.known = true;
      expect(soulsModule.supportedRuntimeTargets({ method: 'soulfactory.provision' })).toEqual(['metiq']);
      expect(soulsModule.supportedRuntimeMethods({ runtime: 'openclaw' })).toBeNull();
      expect(soulsModule.supportedRuntimeMethods({ runtime: 'metiq' })).toEqual(['soulfactory.provision']);
    });

    it('publishSoulDraft signs, publishes, and stores a 31952 draft', async () => {
      mockNostr.publish.mockResolvedValue([{ relay: 'wss://relay', accepted: true, message: '' }]);

      const result = await soulsModule.publishSoulDraft({
        agentId: 'scout',
        content: {
          identity: { name: 'Scout', tier: 'standard' },
          runtime: { target: 'openclaw', capability_ref: 'cap-1' }
        },
        templateRef: '31950:factory:default',
        specHash: 'sha256:spec'
      });

      const signedCall = authModule.signWithAuth.mock.calls.at(-1)?.[0];
      expect(signedCall).toMatchObject({ kind: 31952, pubkey: 'author-pubkey' });
      expect(signedCall.tags).toEqual(expect.arrayContaining([
        ['d', 'scout'],
        ['name', 'Scout'],
        ['tier', 'standard'],
        ['template', '31950:factory:default'],
        ['runtime', 'openclaw'],
        ['capability', 'cap-1'],
        ['spec-hash', 'sha256:spec']
      ]));
      expect(result.publishResults[0].accepted).toBe(true);
    });

    it('publishProvisioningRequest signs 5950 with exact draft and capability refs', async () => {
      mockNostr.publish.mockResolvedValue([{ relay: 'wss://relay', accepted: true, message: '' }]);
      const beforePublish = vi.fn();

      await soulsModule.publishProvisioningRequest({
        agentId: 'scout',
        name: 'Scout',
        tier: 'standard',
        draftRef: '31952:author-pubkey:scout',
        draftEvent: { id: 'draft-event-id', pubkey: 'author-pubkey' },
        draftContent: {
          brief: 'Observe deploys',
          runtime: { target: 'openclaw', runtime_pubkey: 'runtime-pubkey', capability_ref: '30317:runtime:openclaw' }
        },
        templateRef: '31950:factory:default',
        specHash: 'sha256:spec',
        beforePublish
      });

      const signedCall = authModule.signWithAuth.mock.calls.at(-1)?.[0];
      expect(signedCall).toMatchObject({ kind: 5950, pubkey: 'author-pubkey' });
      expect(signedCall.tags).toEqual(expect.arrayContaining([
        ['agent-id', 'scout'],
        ['draft', '31952:author-pubkey:scout'],
        ['draft-event', 'draft-event-id'],
        ['e', 'draft-event-id', 'draft'],
        ['spec-hash', 'sha256:spec'],
        ['runtime', 'openclaw'],
        ['runtime-pubkey', 'runtime-pubkey'],
        ['capability', '30317:runtime:openclaw'],
        ['method', 'soulfactory.provision']
      ]));
      expect(JSON.parse(signedCall.content)).toMatchObject({
        agent_id: 'scout',
        draft_ref: '31952:author-pubkey:scout',
        draft_event_id: 'draft-event-id',
        spec_hash: 'sha256:spec',
        brief: 'Observe deploys'
      });
      expect(beforePublish).toHaveBeenCalledWith(expect.objectContaining({ id: 'signed-action-id' }));
    });
  });

  describe('soul management actions', () => {
    it('buildSoulRef returns a NIP-33 coordinate', () => {
      const ref = soulsModule.buildSoulRef({ agentId: 'scout', pubkey: 'factory-pubkey' });
      expect(ref).toBe('31951:factory-pubkey:scout');
    });

    it('publishSoulAction signs and publishes kind:1950 action event', async () => {
      mockNostr.publish.mockResolvedValue([{ relay: 'wss://relay', accepted: true, message: '' }]);

      const result = await soulsModule.publishSoulAction({
        soul: { agentId: 'scout', pubkey: 'factory-pubkey' },
        action: 'suspend',
        reason: 'Maintenance window'
      });

      expect(authModule.signWithAuth).toHaveBeenCalledWith(expect.objectContaining({
        kind: 1950,
        pubkey: 'author-pubkey',
        content: ''
      }));
      expect(mockNostr.publish).toHaveBeenCalledWith(expect.objectContaining({ id: 'signed-action-id' }));
      expect(result.publishResults[0].accepted).toBe(true);
    });

    it('updateSoulDetails publishes a structured update action with JSON payload', async () => {
      mockNostr.publish.mockResolvedValue([{ relay: 'wss://relay', accepted: true, message: '' }]);

      await soulsModule.updateSoulDetails(
        { agentId: 'scout', pubkey: 'factory-pubkey', tier: 'standard', name: 'Scout', purpose: 'Observe', specHash: 'sha256:old', previousSpecHash: 'sha256:older' },
        { name: 'Scout v2', purpose: 'Observe and report', tier: 'heavy', brief: 'Updated brief', reason: 'ops update', newSpecHash: 'sha256:new' }
      );

      const signedCall = authModule.signWithAuth.mock.calls.at(-1)?.[0];
      expect(signedCall.tags).toEqual(expect.arrayContaining([
        ['soul', '31951:factory-pubkey:scout'],
        ['action', 'update'],
        ['reason', 'ops update'],
        ['method', 'soulfactory.update'],
        ['previous-spec-hash', 'sha256:old'],
        ['spec-hash', 'sha256:new']
      ]));
      expect(JSON.parse(signedCall.content)).toMatchObject({
        schema: 'soulfactory-action/v1',
        action: 'update',
        method: 'soulfactory.update',
        params: {
          update_mode: 'merge',
          previous_spec_hash: 'sha256:old',
          new_spec_hash: 'sha256:new',
          patch: {
            identity: {
              name: 'Scout v2',
              purpose: 'Observe and report',
              tier: 'heavy'
            }
          }
        }
      });
    });

    it('fetchSoulHistory includes soul updates and actions sorted newest-first', async () => {
      let handlers = null;
      mockNostr.subscribe.mockImplementationOnce((filters, incomingHandlers) => {
        handlers = incomingHandlers;
        return () => {};
      });

      const historyPromise = soulsModule.fetchSoulHistory({ agentId: 'scout', pubkey: 'factory' }, { limit: 10 });

      expect(mockNostr.subscribe).toHaveBeenCalledWith([
        { kinds: [31951], '#d': ['scout'], limit: 10 },
        { kinds: [1950], limit: 10 },
        { kinds: [6950, 7950, 1951], limit: 10 }
      ], expect.objectContaining({
        onEvent: expect.any(Function),
        onEose: expect.any(Function),
        onClosed: expect.any(Function)
      }));

      handlers.onEvent({
        id: 'evt-action',
        kind: 1950,
        created_at: 200,
        pubkey: 'author2',
        tags: [['soul', '31951:factory:scout'], ['action', 'suspend'], ['reason', 'maintenance']]
      });
      handlers.onEvent({
        id: 'evt-other-soul',
        kind: 1950,
        created_at: 300,
        pubkey: 'author2',
        tags: [['soul', '31951:factory:other'], ['action', 'suspend']]
      });
      handlers.onEvent({
        id: 'evt-soul',
        kind: 31951,
        created_at: 100,
        pubkey: 'factory',
        tags: [['d', 'scout'], ['status', 'active']]
      });
      handlers.onEose('wss://relay.example');

      const history = await historyPromise;

      expect(history.map((item) => item.id)).toEqual(['evt-action', 'evt-soul']);
      expect(history[0].summary).toBe('suspend: maintenance');
      expect(history.complete).toBe(true);
      expect(history.degraded).toBeNull();
      expect(soulsModule.readModelMeta.history).toMatchObject({
        complete: true,
        degraded: null,
        relaySummary: [expect.objectContaining({ relay: 'wss://relay.example', status: 'eose' })]
      });
    });

    it('fetchSoulHistory returns partial activity with degraded metadata on CLOSED-before-EOSE', async () => {
      let handlers = null;
      mockNostr.subscribe.mockImplementationOnce((_filters, incomingHandlers) => {
        handlers = incomingHandlers;
        return () => {};
      });

      const historyPromise = soulsModule.fetchSoulHistory({ agentId: 'scout', pubkey: 'factory' }, { limit: 10 });

      handlers.onEvent({
        id: 'evt-action',
        kind: 1950,
        created_at: 200,
        pubkey: 'author2',
        tags: [['soul', '31951:factory:scout'], ['action', 'suspend']]
      }, 'wss://relay.example');
      handlers.onClosed('relay closed before EOSE', 'wss://relay.example', { terminal: true, source: 'closed' });

      const history = await historyPromise;

      expect(history.map((item) => item.id)).toEqual(['evt-action']);
      expect(history.complete).toBe(false);
      expect(history.degraded).toMatchObject({
        incomplete: true,
        reason: 'closed',
        partialEventCount: 1,
        relaySummary: [expect.objectContaining({ relay: 'wss://relay.example', status: 'closed' })]
      });
      expect(soulsModule.readModelMeta.history).toMatchObject({
        complete: false,
        degraded: expect.objectContaining({ incomplete: true, reason: 'closed' })
      });
    });
  });
});
