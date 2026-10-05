import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest';

// Mock browser environment
global.window = global;

const bootMock = vi.hoisted(() => ({
  ensureRelayConnection: vi.fn(async () => {}), boot: vi.fn(async () => {}),
  store: null, pool: null, refreshCallbacks: new Set(),
  getEventStore: () => bootMock.store, getPool: () => bootMock.pool,
  getServicePubkey: () => 'a'.repeat(64), getServicePubkeys: () => ['a'.repeat(64)],
  getRelayUrls: () => ['wss://relay.example'],
  onStoreRefresh: (cb) => { bootMock.refreshCallbacks.add(cb); return () => bootMock.refreshCallbacks.delete(cb); }
}));
vi.mock('../../src/lib/nostr/boot.js', () => bootMock);
vi.mock('$lib/nostr/boot.js', () => bootMock);

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
    RUNTIME_CAPABILITY: 30317,
    BAHIA_SYSTEM_DISCOVERY: 11316
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
    status: event.tags.find(t => t[0] === 'status')?.[1] || 'active',
    runtime: { runtime_pubkey: event.tags.find(t => t[0] === 'runtime-pubkey')?.[1] || '' }
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
const KINDS = nostrClientMockFactory().KINDS;

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

    bootMock.refreshCallbacks.clear();
    const cached = [];
    bootMock.store = {
      events: cached,
      query: vi.fn((filter) => cached.filter((event) =>
        filter.kinds?.includes(event.kind) &&
        (!filter.authors || filter.authors.includes(event.pubkey)) &&
        Object.entries(filter).filter(([key]) => key.startsWith('#')).every(([key, values]) =>
          event.tags?.some((tag) => tag[0] === key.slice(1) && values.includes(tag[1]))))),
      getCursor: vi.fn(() => null), setCursor: vi.fn()
    };
    bootMock.pool = { subscribe: vi.fn(() => ({ unsubscribe: vi.fn() })) };
    // Dynamically import souls module
    soulsModule = await import('../../src/lib/stores/souls.js');
  });

  afterEach(() => {
    // Clean up subscriptions
    soulsModule.teardownSoulFactoryStoreBinding?.();
  });

  it('removes a stale rerank model when reranking is disabled', () => {
    const memory = soulsModule.normalizeProvisioningMemorySpec({
      search: { rerank: false, rerank_model: 'local-reranker' }
    });

    expect(memory.search.rerank).toBe(false);
    expect(memory.search).not.toHaveProperty('rerank_model');
  });

  it('maps an unsupported enabled reranker to the backend default', () => {
    const memory = soulsModule.normalizeProvisioningMemorySpec({
      search: { rerank: true, rerank_model: 'local-reranker' }
    });

    expect(memory.search.rerank_model).toBe('rerank-v3.5');
  });

  describe('store-first soul read models', () => {
    const service = 'a'.repeat(64);
    const stranger = 'b'.repeat(64);
    const soul = (id, pubkey = service) => ({
      id, kind: KINDS.AGENT_SOUL, pubkey, created_at: 100,
      tags: [['d', id], ['name', id], ['status', 'active']], content: '{}'
    });

    it('renders cached souls without a loading gate or direct Nostr subscription', async () => {
      bootMock.store.events.push(soul('cached'));
      await soulsModule.subscribeToSoulFactoryUpdates();
      expect(soulsModule.souls.map((row) => row.agentId)).toEqual(['cached']);
      expect(soulsModule.loading.souls).toBe(false);
      expect(mockNostr.subscribe).not.toHaveBeenCalled();
      expect(bootMock.pool.subscribe).toHaveBeenCalled();
    });

    it('drops untrusted cached and live soul authors', async () => {
      bootMock.store.events.push(soul('trusted'), soul('forged', stranger));
      await soulsModule.subscribeToSoulFactoryUpdates();
      expect(soulsModule.souls.map((row) => row.agentId)).toEqual(['trusted']);
      bootMock.store.events.push(soul('forged-live', stranger));
      for (const cb of bootMock.refreshCallbacks) cb();
      expect(soulsModule.souls.map((row) => row.agentId)).toEqual(['trusted']);
    });

    it('keeps app-level subscriptions across page consumer cleanup', async () => {
      await soulsModule.subscribeToSoulFactoryUpdates();
      const count = bootMock.pool.subscribe.mock.calls.length;
      soulsModule.unsubscribeFromSoulUpdates();
      await soulsModule.subscribeToSoulFactoryUpdates();
      expect(bootMock.pool.subscribe).toHaveBeenCalledTimes(count);
      soulsModule.teardownSoulFactoryStoreBinding();
      expect(bootMock.refreshCallbacks.size).toBe(0);
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

      
      const statusEvent = {
        id: 'status-1',
        kind: KINDS.PROVISIONING_STATUS,
        content: 'Creating Qdrant collection',
        tags: [
		  ['e', requestEventId],
		  ['run-id', 'run-1'],
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

      
      const resultEvent = {
        id: 'result-success-1',
		created_at: 100,
        kind: KINDS.PROVISIONING_RESULT,
        content: '{"agentId":"agent-new","npub":"npub123"}',
        tags: [
		  ['e', requestEventId],
		  ['run-id', 'run-1'],
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
	  expect(unsub).not.toHaveBeenCalled();
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

      
      const resultEvent = {
        id: 'result-error-1',
		created_at: 100,
        kind: KINDS.PROVISIONING_RESULT,
        content: 'Qdrant collection creation failed',
        tags: [
		  ['e', requestEventId],
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
	  expect(unsub).not.toHaveBeenCalled();
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

	it('deduplicates snapshot/live overlap and rejects wrong request, run, and author correlation', () => {
	  const requestEventId = 'req-correlated';
	  let handlers;
	  const onProgress = vi.fn();
	  mockNostr.subscribe.mockImplementation((_filters, incoming) => { handlers = incoming; return vi.fn(); });
	  soulsModule.trackProvisioningRun(requestEventId, { expectedAuthor: 'factory', onProgress });

	  const valid = { id: 'status-valid', pubkey: 'factory', kind: KINDS.PROVISIONING_STATUS, content: 'Checking signer', tags: [['e', requestEventId], ['run-id', 'run-a'], ['step', 'nip46_signer'], ['progress', '3', '14']] };
	  handlers.onEvent(valid);
	  handlers.onEvent(valid);
	  handlers.onEvent({ ...valid, id: 'wrong-request', tags: [['e', 'abandoned'], ['run-id', 'run-a'], ['progress', '4', '14']] });
	  handlers.onEvent({ ...valid, id: 'wrong-run', tags: [['e', requestEventId], ['run-id', 'run-b'], ['progress', '4', '14']] });
	  handlers.onEvent({ ...valid, id: 'wrong-author', pubkey: 'attacker' });

	  const run = soulsModule.provisioningRuns.get(requestEventId);
	  expect(run.runId).toBe('run-a');
	  expect(run.statusEvents).toHaveLength(1);
	  expect(onProgress).toHaveBeenCalledTimes(1);
	});

	it('replaces progress immediately with retained terminal and lets a newer terminal supersede stale failure', () => {
	  const requestEventId = 'req-terminal-order';
	  let handlers;
	  mockNostr.subscribe.mockImplementation((_filters, incoming) => { handlers = incoming; return vi.fn(); });
	  soulsModule.trackProvisioningRun(requestEventId, {});
	  handlers.onEvent({ id: 'progress', created_at: 90, kind: KINDS.PROVISIONING_STATUS, content: 'DM probe', tags: [['e', requestEventId], ['run-id', 'run-a'], ['progress', '13', '14']] });
	  handlers.onEvent({ id: 'failure', created_at: 100, kind: KINDS.PROVISIONING_RESULT, content: 'relay timeout', tags: [['e', requestEventId], ['run-id', 'run-a'], ['status', 'error']] });
	  expect(soulsModule.provisioningRuns.get(requestEventId).status).toBe('failed');
	  handlers.onEvent({ id: 'success', created_at: 101, kind: KINDS.PROVISIONING_RESULT, content: '{"agentId":"scout"}', tags: [['e', requestEventId], ['run-id', 'run-a'], ['status', 'success']] });
	  expect(soulsModule.provisioningRuns.get(requestEventId).status).toBe('completed');
	  handlers.onEvent({ id: 'late-progress', created_at: 102, kind: KINDS.PROVISIONING_STATUS, content: 'stale', tags: [['e', requestEventId], ['run-id', 'run-a'], ['progress', '14', '14']] });
	  expect(soulsModule.provisioningRuns.get(requestEventId).status).toBe('completed');
	});

	it('bounds 100 percent without terminal in an actionable reconciliation error state', () => {
	  vi.useFakeTimers();
	  const requestEventId = 'req-missing-terminal';
	  let handlers;
	  mockNostr.subscribe.mockImplementation((_filters, incoming) => { handlers = incoming; return vi.fn(); });
	  soulsModule.trackProvisioningRun(requestEventId, { reconciliationTimeoutMs: 5000 });
	  handlers.onEvent({ id: 'status-100', kind: KINDS.PROVISIONING_STATUS, content: 'All gates complete', tags: [['e', requestEventId], ['run-id', 'run-a'], ['progress', '14', '14']] });
	  expect(soulsModule.provisioningRuns.get(requestEventId).status).toBe('reconciling');
	  vi.advanceTimersByTime(5000);
	  const run = soulsModule.provisioningRuns.get(requestEventId);
	  expect(run.status).toBe('reconciliation_error');
	  expect(run.retryState).toBe('terminal_missing');
	  expect(run.message).toContain('do not assume the soul is running');
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
    it('trusts runtime capability only from a factory-signed soul runtime key', async () => {
      const service = 'a'.repeat(64);
      const runtime = 'c'.repeat(64);
      bootMock.store.events.push({ id: 'soul', kind: KINDS.AGENT_SOUL, pubkey: service, created_at: 1,
        tags: [['d', 'scout'], ['runtime-pubkey', runtime]], content: '{}' });
      bootMock.store.events.push({ id: 'cap', kind: KINDS.RUNTIME_CAPABILITY, pubkey: runtime, created_at: 2,
        tags: [['runtime', 'openclaw']], content: '{}' });
      bootMock.store.events.push({ id: 'forged-cap', kind: KINDS.RUNTIME_CAPABILITY, pubkey: 'd'.repeat(64), created_at: 3,
        tags: [['runtime', 'metiq']], content: '{}' });
      await soulsModule.subscribeToSoulFactoryUpdates();
      expect(soulsModule.runtimeCapabilities.map((cap) => cap.id)).toEqual(['cap']);
      soulsModule.serverAgentRuntimes.push('openclaw');
      soulsModule.serverPolicy.known = true;
      expect(soulsModule.supportedRuntimeTargets({ method: 'soulfactory.provision' })).toEqual(['openclaw']);
    });

    it('keeps runtime method controls scoped to capabilities from trusted Soul runtime keys', async () => {
      const nostrModule = await import('$lib/nostr/client.js');
      nostrModule.parseRuntimeCapabilityEvent.mockImplementation((event) => ({
        id: event.id, pubkey: event.pubkey, createdAt: event.created_at,
        runtime: event.tags.find((tag) => tag[0] === 'runtime')?.[1] || 'unknown',
        methods: event.tags.filter((tag) => tag[0] === 'method').map((tag) => tag[1]),
        compatible: true, event
      }));
      const service = 'a'.repeat(64);
      const openclaw = 'c'.repeat(64);
      const metiq = 'd'.repeat(64);
      bootMock.store.events.push(
        { id: 'soul-oc', kind: KINDS.AGENT_SOUL, pubkey: service, created_at: 1, tags: [['d', 'oc'], ['runtime-pubkey', openclaw]], content: '{}' },
        { id: 'soul-mq', kind: KINDS.AGENT_SOUL, pubkey: service, created_at: 1, tags: [['d', 'mq'], ['runtime-pubkey', metiq]], content: '{}' },
        { id: 'cap-oc', kind: KINDS.RUNTIME_CAPABILITY, pubkey: openclaw, created_at: 2, tags: [['runtime', 'openclaw'], ['method', 'soulfactory.provision'], ['method', 'soulfactory.config.reload']], content: '{}' },
        { id: 'cap-mq', kind: KINDS.RUNTIME_CAPABILITY, pubkey: metiq, created_at: 2, tags: [['runtime', 'metiq'], ['method', 'soulfactory.provision']], content: '{}' }
      );
      await soulsModule.subscribeToSoulFactoryUpdates();
      await Promise.resolve();
      soulsModule.serverAgentRuntimes.push('openclaw', 'metiq');
      soulsModule.serverPolicy.known = true;
      expect(soulsModule.supportedRuntimeMethods({ runtime: 'openclaw', runtimePubkey: openclaw }))
        .toEqual(expect.arrayContaining(['soulfactory.provision', 'soulfactory.config.reload']));
      expect(soulsModule.supportedRuntimeMethods({ runtime: 'openclaw', runtimePubkey: metiq })).toBeNull();
      expect(soulsModule.supportedRuntimeMethods({ runtime: 'unregistered' })).toBeNull();
    });

    it('intersects trusted runtime capabilities with service-authored runtime policy', async () => {
      const service = 'a'.repeat(64);
      const openclaw = 'c'.repeat(64);
      const metiq = 'd'.repeat(64);
      bootMock.store.events.push(
        { id: 'soul-oc', kind: KINDS.AGENT_SOUL, pubkey: service, created_at: 1, tags: [['d', 'oc'], ['runtime-pubkey', openclaw]], content: '{}' },
        { id: 'soul-mq', kind: KINDS.AGENT_SOUL, pubkey: service, created_at: 1, tags: [['d', 'mq'], ['runtime-pubkey', metiq]], content: '{}' },
        { id: 'cap-oc', kind: KINDS.RUNTIME_CAPABILITY, pubkey: openclaw, created_at: 2, tags: [['runtime', 'openclaw'], ['method', 'soulfactory.provision']], content: '{}' },
        { id: 'cap-mq', kind: KINDS.RUNTIME_CAPABILITY, pubkey: metiq, created_at: 2, tags: [['runtime', 'metiq'], ['method', 'soulfactory.provision']], content: '{}' }
      );
      await soulsModule.subscribeToSoulFactoryUpdates();
      await Promise.resolve();
      expect(soulsModule.supportedRuntimeTargets({ method: 'soulfactory.provision' })).toEqual([]);
      soulsModule.serverAgentRuntimes.push('metiq');
      soulsModule.serverPolicy.known = true;
      expect(soulsModule.supportedRuntimeTargets({ method: 'soulfactory.provision' })).toEqual(['metiq']);
      expect(soulsModule.supportedRuntimeMethods({ runtime: 'openclaw' })).toBeNull();
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

    it('reads soul history from the local store without opening a REQ', async () => {
      const service = 'a'.repeat(64);
      authModule.authState.pubkey = 'c'.repeat(64);
      bootMock.store.events.push({ id: 'evt-soul', kind: KINDS.AGENT_SOUL, pubkey: service,
        created_at: 100, tags: [['d', 'scout'], ['status', 'active']], content: '{}' });
      bootMock.store.events.push({ id: 'evt-action', kind: KINDS.SOUL_ACTION, pubkey: authModule.authState.pubkey,
        created_at: 200, tags: [['soul', `31951:${service}:scout`], ['action', 'suspend'], ['reason', 'maintenance']], content: '' });
      const history = await soulsModule.fetchSoulHistory({ agentId: 'scout', pubkey: service }, { limit: 10 });
      expect(history.map((item) => item.id)).toEqual(['evt-action', 'evt-soul']);
      expect(mockNostr.subscribe).not.toHaveBeenCalled();
      expect(bootMock.pool.subscribe).not.toHaveBeenCalled();
      expect(history.complete).toBe(false);
      expect(history.degraded).toMatchObject({ incomplete: true, reason: 'catching-up' });
    });

  });
});
