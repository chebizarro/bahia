import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest';

// Mock browser environment
global.window = global;

// Mock the nostr client module
vi.mock('../../src/lib/nostr/client.js', () => {
  const KINDS = {
    SOUL_TEMPLATE: 31950,
    AGENT_SOUL: 31951,
    SOUL_DRAFT: 31952,
    PROVISIONING_REQUEST: 5950,
    PROVISIONING_STATUS: 6950,
    PROVISIONING_RESULT: 7950,
    SOUL_ACTION: 1950
  };

  const mockNostr = {
    subscribe: vi.fn(),
    query: vi.fn(),
    publish: vi.fn()
  };

  const fetchSouls = vi.fn();
  const fetchTemplates = vi.fn();
  
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

  return {
    nostr: mockNostr,
    fetchSouls,
    fetchTemplates,
    parseSoulEvent,
    parseTemplateEvent,
    KINDS
  };
});

vi.mock('../../src/lib/stores/auth.js', () => ({
  authState: { status: 'authenticated', pubkey: 'author-pubkey' },
  login: vi.fn(async () => {}),
  signWithAuth: vi.fn(async (event) => ({ ...event, id: 'signed-action-id' }))
}));

describe('Souls Store', () => {
  let soulsModule;
  let mockNostr;
  let fetchSouls;
  let fetchTemplates;
  let parseSoulEvent;
  let parseTemplateEvent;
  let authModule;

  beforeEach(async () => {
    // Reset modules to get fresh store state
    vi.resetModules();
    vi.clearAllMocks();

    // Import mocked nostr client
    const nostrModule = await import('../../src/lib/nostr/client.js');
    mockNostr = nostrModule.nostr;
    fetchSouls = nostrModule.fetchSouls;
    fetchTemplates = nostrModule.fetchTemplates;
    parseSoulEvent = nostrModule.parseSoulEvent;
    parseTemplateEvent = nostrModule.parseTemplateEvent;
    authModule = await import('../../src/lib/stores/auth.js');

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

  describe('loadSouls', () => {
    it('should load souls and update store', async () => {
      await soulsModule.loadSouls();

      expect(fetchSouls).toHaveBeenCalledWith(null);
      expect(parseSoulEvent).toHaveBeenCalledTimes(2);

      const souls = soulsModule.souls;
      expect(souls).toHaveLength(2);
      expect(souls[0].agentId).toBe('agent-id-2'); // Sorted newest first
      expect(souls[1].agentId).toBe('agent-id-1');
    });

    it('should filter by author pubkey when provided', async () => {
      const authorPubkey = 'author-pubkey-123';

      await soulsModule.loadSouls(authorPubkey);

      expect(fetchSouls).toHaveBeenCalledWith(authorPubkey);
    });

    it('should set loading state during load', async () => {
      let loadingDuringFetch = null;

      fetchSouls.mockImplementation(async () => {
        loadingDuringFetch = { ...soulsModule.loading };
        return [{
          id: 'soul-1',
          pubkey: 'pk-1',
          created_at: 1714392000,
          tags: [['d', 'agent-1'], ['name', 'Agent']]
        }];
      });

      await soulsModule.loadSouls();

      expect(loadingDuringFetch.souls).toBe(true);

      const loadingAfter = soulsModule.loading;
      expect(loadingAfter.souls).toBe(false);
    });

    it('should handle load error and set error state', async () => {
      const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
      
      fetchSouls.mockRejectedValue(new Error('Relay connection failed'));

      await soulsModule.loadSouls();

      expect(consoleError).toHaveBeenCalledWith(
        '[souls] Failed to load souls:',
        expect.any(Error)
      );

      const error = soulsModule.error.value;
      expect(error).toBe('Relay connection failed');

      const loading = soulsModule.loading;
      expect(loading.souls).toBe(false);

      consoleError.mockRestore();
    });

    it('should clear error state on successful load', async () => {
      const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
      
      // First load fails
      fetchSouls.mockRejectedValueOnce(new Error('Network error'));
      await soulsModule.loadSouls();

      let error = soulsModule.error.value;
      expect(error).toBe('Network error');

      // Second load succeeds
      fetchSouls.mockResolvedValueOnce([{
        id: 'soul-1',
        pubkey: 'pk-1',
        created_at: 1714392000,
        tags: [['d', 'agent-1']]
      }]);
      await soulsModule.loadSouls();

      error = soulsModule.error.value;
      expect(error).toBeNull();

      consoleError.mockRestore();
    });
  });

  describe('loadTemplates', () => {
    it('should load templates and update store', async () => {
      await soulsModule.loadTemplates();

      expect(fetchTemplates).toHaveBeenCalledWith(null);
      expect(parseTemplateEvent).toHaveBeenCalledTimes(1);

      const templates = soulsModule.templates;
      expect(templates).toHaveLength(1);
      expect(templates[0].identifier).toBe('template-standard');
    });

    it('should filter by author pubkey when provided', async () => {
      const authorPubkey = 'template-author-456';

      await soulsModule.loadTemplates(authorPubkey);

      expect(fetchTemplates).toHaveBeenCalledWith(authorPubkey);
    });

    it('should sort templates by name', async () => {
      fetchTemplates.mockResolvedValue([
        {
          id: 't1',
          pubkey: 'pk',
          created_at: 1714390000,
          tags: [['d', 'z-template'], ['name', 'Zebra']]
        },
        {
          id: 't2',
          pubkey: 'pk',
          created_at: 1714390100,
          tags: [['d', 'a-template'], ['name', 'Alpha']]
        },
        {
          id: 't3',
          pubkey: 'pk',
          created_at: 1714390200,
          tags: [['d', 'm-template'], ['name', 'Middle']]
        }
      ]);

      await soulsModule.loadTemplates();

      const templates = soulsModule.templates;
      expect(templates).toHaveLength(3);
      expect(templates[0].name).toBe('Alpha');
      expect(templates[1].name).toBe('Middle');
      expect(templates[2].name).toBe('Zebra');
    });

    it('should set loading state during load', async () => {
      let loadingDuringFetch = null;

      fetchTemplates.mockImplementation(async () => {
        loadingDuringFetch = { ...soulsModule.loading };
        return [{
          id: 't1',
          pubkey: 'pk',
          created_at: 1714390000,
          tags: [['d', 'template-1']]
        }];
      });

      await soulsModule.loadTemplates();

      expect(loadingDuringFetch.templates).toBe(true);

      const loadingAfter = soulsModule.loading;
      expect(loadingAfter.templates).toBe(false);
    });

    it('should handle load error and set error state', async () => {
      const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
      
      fetchTemplates.mockRejectedValue(new Error('Templates fetch error'));

      await soulsModule.loadTemplates();

      expect(consoleError).toHaveBeenCalledWith(
        '[souls] Failed to load templates:',
        expect.any(Error)
      );

      const error = soulsModule.error.value;
      expect(error).toBe('Templates fetch error');

      consoleError.mockRestore();
    });
  });

  describe('loadAll', () => {
    it('should load both souls and templates in parallel', async () => {
      await soulsModule.loadAll();

      expect(fetchSouls).toHaveBeenCalledWith(null);
      expect(fetchTemplates).toHaveBeenCalledWith(null);

      const souls = soulsModule.souls;
      const templates = soulsModule.templates;

      expect(souls).toHaveLength(2);
      expect(templates).toHaveLength(1);
    });

    it('should pass author pubkey to both loaders', async () => {
      const authorPubkey = 'author-all-789';

      await soulsModule.loadAll(authorPubkey);

      expect(fetchSouls).toHaveBeenCalledWith(authorPubkey);
      expect(fetchTemplates).toHaveBeenCalledWith(authorPubkey);
    });

    it('should complete even if one loader fails', async () => {
      const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
      
      fetchSouls.mockRejectedValue(new Error('Souls error'));
      fetchTemplates.mockResolvedValue([{
        id: 't1',
        pubkey: 'pk',
        created_at: 1714390000,
        tags: [['d', 'template-1'], ['name', 'Template']]
      }]);

      await soulsModule.loadAll();

      expect(fetchSouls).toHaveBeenCalled();
      expect(fetchTemplates).toHaveBeenCalled();

      const templates = soulsModule.templates;
      expect(templates).toHaveLength(1);

      consoleError.mockRestore();
    });
  });

  describe('subscribeToSoulUpdates', () => {
    it('should subscribe to soul events without author filter', () => {
      const { KINDS } = require('../../src/lib/nostr/client.js');

      soulsModule.subscribeToSoulUpdates();

      expect(mockNostr.subscribe).toHaveBeenCalledWith(
        [expect.objectContaining({
          kinds: [KINDS.AGENT_SOUL]
        })],
        expect.objectContaining({
          onEvent: expect.any(Function)
        })
      );

      const callArgs = mockNostr.subscribe.mock.calls[0];
      const filter = callArgs[0][0];
      expect(filter.authors).toBeUndefined();
      expect(filter.since).toBeGreaterThan(0);
    });

    it('should subscribe to soul events with author filter', () => {
      const authorPubkey = 'author-subscribe-abc';
      const { KINDS } = require('../../src/lib/nostr/client.js');

      soulsModule.subscribeToSoulUpdates(authorPubkey);

      expect(mockNostr.subscribe).toHaveBeenCalledWith(
        [expect.objectContaining({
          kinds: [KINDS.AGENT_SOUL],
          authors: [authorPubkey]
        })],
        expect.any(Object)
      );
    });

    it('should update existing soul when event is received', async () => {
      // First load initial souls
      await soulsModule.loadSouls();

      let onEventCallback = null;
      mockNostr.subscribe.mockImplementation((filters, handlers) => {
        onEventCallback = handlers.onEvent;
        return () => {};
      });

      soulsModule.subscribeToSoulUpdates();

      // Receive update for existing soul
      const updatedEvent = {
        id: 'soul-1-updated',
        pubkey: 'pubkey-1',
        created_at: 1714392200,
        tags: [
          ['d', 'agent-id-1'],
          ['name', 'Agent Alpha Updated'],
          ['status', 'suspended']
        ]
      };

      onEventCallback(updatedEvent);

      const souls = soulsModule.souls;
      expect(souls).toHaveLength(2);
      
      const updatedSoul = souls.find(s => s.agentId === 'agent-id-1');
      expect(updatedSoul.name).toBe('Agent Alpha Updated');
      expect(updatedSoul.status).toBe('suspended');
    });

    it('should add new soul when event is received', async () => {
      await soulsModule.loadSouls();

      let onEventCallback = null;
      mockNostr.subscribe.mockImplementation((filters, handlers) => {
        onEventCallback = handlers.onEvent;
        return () => {};
      });

      soulsModule.subscribeToSoulUpdates();

      // Receive new soul
      const newEvent = {
        id: 'soul-3',
        pubkey: 'pubkey-2',
        created_at: 1714392300,
        tags: [
          ['d', 'agent-id-3'],
          ['name', 'Agent Gamma'],
          ['status', 'active']
        ]
      };

      onEventCallback(newEvent);

      const souls = soulsModule.souls;
      expect(souls).toHaveLength(3);
      expect(souls[0].agentId).toBe('agent-id-3'); // Added at front
    });

    it('should close previous subscription when called again', () => {
      const cleanup1 = vi.fn();
      const cleanup2 = vi.fn();

      mockNostr.subscribe.mockReturnValueOnce(cleanup1).mockReturnValueOnce(cleanup2);

      soulsModule.subscribeToSoulUpdates();
      soulsModule.subscribeToSoulUpdates();

      expect(cleanup1).toHaveBeenCalledTimes(1);
      expect(mockNostr.subscribe).toHaveBeenCalledTimes(2);
    });
  });

  describe('unsubscribeFromSoulUpdates', () => {
    it('should call cleanup function when unsubscribing', () => {
      const cleanup = vi.fn();
      mockNostr.subscribe.mockReturnValue(cleanup);

      soulsModule.subscribeToSoulUpdates();
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
          { kinds: [KINDS.PROVISIONING_RESULT], '#e': [requestEventId] }
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

    it('should fail run when relay closes subscription', () => {
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
      expect(run.status).toBe('failed');
      expect(run.result.error).toBe('Provisioning subscription closed: auth required');
      expect(onError).toHaveBeenCalledWith('Provisioning subscription closed: auth required');
      expect(unsub).toHaveBeenCalledTimes(1);
    });

    it('should fail run on timeout when no relay updates arrive', () => {
      vi.useFakeTimers();

      const requestEventId = 'req-event-timeout';
      const onError = vi.fn();
      soulsModule.trackProvisioningRun(requestEventId, { onError });

      vi.advanceTimersByTime(121000);

      const run = soulsModule.provisioningRuns.get(requestEventId);
      expect(run.status).toBe('failed');
      expect(run.result.error).toBe('Provisioning timed out waiting for relay updates');
      expect(onError).toHaveBeenCalledWith('Provisioning timed out waiting for relay updates');

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

    it('updateSoulDetails publishes a regenerate action with JSON payload', async () => {
      mockNostr.publish.mockResolvedValue([{ relay: 'wss://relay', accepted: true, message: '' }]);

      await soulsModule.updateSoulDetails(
        { agentId: 'scout', pubkey: 'factory-pubkey', tier: 'standard', name: 'Scout', purpose: 'Observe' },
        { name: 'Scout v2', purpose: 'Observe and report', tier: 'heavy', brief: 'Updated brief', reason: 'ops update' }
      );

      const signedCall = authModule.signWithAuth.mock.calls.at(-1)?.[0];
      expect(signedCall.tags).toEqual(expect.arrayContaining([
        ['soul', '31951:factory-pubkey:scout'],
        ['action', 'regenerate'],
        ['reason', 'ops update']
      ]));
      expect(JSON.parse(signedCall.content)).toMatchObject({
        name: 'Scout v2',
        purpose: 'Observe and report',
        tier: 'heavy',
        brief: 'Updated brief'
      });
    });

    it('fetchSoulHistory includes soul updates and actions sorted newest-first', async () => {
      mockNostr.query.mockResolvedValue([
        {
          id: 'evt-action',
          kind: 1950,
          created_at: 200,
          pubkey: 'author2',
          tags: [['action', 'suspend'], ['reason', 'maintenance']]
        },
        {
          id: 'evt-soul',
          kind: 31951,
          created_at: 100,
          pubkey: 'factory',
          tags: [['status', 'active']]
        }
      ]);

      const history = await soulsModule.fetchSoulHistory({ agentId: 'scout', pubkey: 'factory' }, { limit: 10 });

      expect(mockNostr.query).toHaveBeenCalledWith([
        { kinds: [31951], '#d': ['scout'], limit: 10 },
        { kinds: [1950], '#soul': ['31951:factory:scout'], limit: 10 }
      ]);
      expect(history.map((item) => item.id)).toEqual(['evt-action', 'evt-soul']);
      expect(history[0].summary).toBe('suspend: maintenance');
    });
  });
});
