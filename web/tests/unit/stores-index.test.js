import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest';
import { get } from 'svelte/store';

// Mock browser environment
global.window = global;
global.EventSource = vi.fn();

// Mock the API client module
vi.mock('../../src/lib/api/client.js', () => {
  const mockApi = {
    listServices: vi.fn(),
    listEnvironments: vi.fn(),
    listStates: vi.fn(),
    listWorkers: vi.fn(),
    streamEvents: vi.fn()
  };
  return { api: mockApi };
});

describe('Global Stores (index.js)', () => {
  let storesModule;
  let mockApi;

  beforeEach(async () => {
    // Reset modules to get fresh store state
    vi.resetModules();
    vi.clearAllMocks();

    // Import mocked API
    const apiModule = await import('../../src/lib/api/client.js');
    mockApi = apiModule.api;

    // Set default mock implementations
    mockApi.listServices.mockResolvedValue([
      { id: 'svc-1', name: 'Service 1' },
      { id: 'svc-2', name: 'Service 2' }
    ]);
    mockApi.listEnvironments.mockResolvedValue([
      { id: 'env-prod', name: 'Production' },
      { id: 'env-dev', name: 'Development' }
    ]);
    mockApi.listStates.mockResolvedValue([
      { id: 'state-1', service_id: 'svc-1', environment_id: 'env-prod', drift_status: 'synced' },
      { id: 'state-2', service_id: 'svc-1', environment_id: 'env-dev', drift_status: 'drifted' }
    ]);
    mockApi.listWorkers.mockResolvedValue([
      { pubkey: 'worker-1', status: 'active' },
      { pubkey: 'worker-2', status: 'idle' }
    ]);
    mockApi.streamEvents.mockReturnValue(() => {});

    // Dynamically import stores module
    storesModule = await import('../../src/lib/stores/index.js');
  });

  afterEach(() => {
    // Clean up any subscriptions
    if (storesModule.unsubscribeFromEvents) {
      storesModule.unsubscribeFromEvents();
    }
  });

  describe('loadServices', () => {
    it('should load services and update store', async () => {
      await storesModule.loadServices();

      expect(mockApi.listServices).toHaveBeenCalledTimes(1);

      const services = get(storesModule.services);
      expect(services).toHaveLength(2);
      expect(services[0].id).toBe('svc-1');
      expect(services[1].id).toBe('svc-2');
    });

    it('should set loading state during load', async () => {
      let loadingDuringFetch = null;

      mockApi.listServices.mockImplementation(async () => {
        loadingDuringFetch = get(storesModule.loading);
        return [{ id: 'svc-1', name: 'Service 1' }];
      });

      await storesModule.loadServices();

      expect(loadingDuringFetch.services).toBe(true);

      const loadingAfter = get(storesModule.loading);
      expect(loadingAfter.services).toBe(false);
    });

    it('should handle load error gracefully', async () => {
      const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
      mockApi.listServices.mockRejectedValue(new Error('Network error'));

      await storesModule.loadServices();

      expect(consoleError).toHaveBeenCalledWith(
        'Failed to load services:',
        expect.any(Error)
      );

      const services = get(storesModule.services);
      expect(services).toEqual([]);

      const loading = get(storesModule.loading);
      expect(loading.services).toBe(false);

      consoleError.mockRestore();
    });

    it('should set empty array when API returns null', async () => {
      mockApi.listServices.mockResolvedValue(null);

      await storesModule.loadServices();

      const services = get(storesModule.services);
      expect(services).toEqual([]);
    });
  });

  describe('loadEnvironments', () => {
    it('should load environments and update store', async () => {
      await storesModule.loadEnvironments();

      expect(mockApi.listEnvironments).toHaveBeenCalledTimes(1);

      const environments = get(storesModule.environments);
      expect(environments).toHaveLength(2);
      expect(environments[0].id).toBe('env-prod');
    });

    it('should set loading state during load', async () => {
      let loadingDuringFetch = null;

      mockApi.listEnvironments.mockImplementation(async () => {
        loadingDuringFetch = get(storesModule.loading);
        return [{ id: 'env-1', name: 'Env 1' }];
      });

      await storesModule.loadEnvironments();

      expect(loadingDuringFetch.environments).toBe(true);

      const loadingAfter = get(storesModule.loading);
      expect(loadingAfter.environments).toBe(false);
    });

    it('should handle load error gracefully', async () => {
      const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
      mockApi.listEnvironments.mockRejectedValue(new Error('API error'));

      await storesModule.loadEnvironments();

      expect(consoleError).toHaveBeenCalled();

      const loading = get(storesModule.loading);
      expect(loading.environments).toBe(false);

      consoleError.mockRestore();
    });
  });

  describe('loadStates', () => {
    it('should load states and update store', async () => {
      await storesModule.loadStates();

      expect(mockApi.listStates).toHaveBeenCalledTimes(1);

      const states = get(storesModule.states);
      expect(states).toHaveLength(2);
      expect(states[0].drift_status).toBe('synced');
      expect(states[1].drift_status).toBe('drifted');
    });

    it('should set loading state during load', async () => {
      let loadingDuringFetch = null;

      mockApi.listStates.mockImplementation(async () => {
        loadingDuringFetch = get(storesModule.loading);
        return [{ id: 'state-1' }];
      });

      await storesModule.loadStates();

      expect(loadingDuringFetch.states).toBe(true);

      const loadingAfter = get(storesModule.loading);
      expect(loadingAfter.states).toBe(false);
    });

    it('should handle load error gracefully', async () => {
      const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
      mockApi.listStates.mockRejectedValue(new Error('States error'));

      await storesModule.loadStates();

      expect(consoleError).toHaveBeenCalled();

      const loading = get(storesModule.loading);
      expect(loading.states).toBe(false);

      consoleError.mockRestore();
    });
  });

  describe('loadWorkers', () => {
    it('should load workers and update store', async () => {
      await storesModule.loadWorkers();

      expect(mockApi.listWorkers).toHaveBeenCalledTimes(1);

      const workers = get(storesModule.workers);
      expect(workers).toHaveLength(2);
      expect(workers[0].pubkey).toBe('worker-1');
    });

    it('should set loading state during load', async () => {
      let loadingDuringFetch = null;

      mockApi.listWorkers.mockImplementation(async () => {
        loadingDuringFetch = get(storesModule.loading);
        return [{ pubkey: 'w1' }];
      });

      await storesModule.loadWorkers();

      expect(loadingDuringFetch.workers).toBe(true);

      const loadingAfter = get(storesModule.loading);
      expect(loadingAfter.workers).toBe(false);
    });

    it('should handle load error gracefully', async () => {
      const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
      mockApi.listWorkers.mockRejectedValue(new Error('Workers error'));

      await storesModule.loadWorkers();

      expect(consoleError).toHaveBeenCalled();

      const loading = get(storesModule.loading);
      expect(loading.workers).toBe(false);

      consoleError.mockRestore();
    });
  });

  describe('loadAll', () => {
    it('should load all data stores in parallel', async () => {
      await storesModule.loadAll();

      expect(mockApi.listServices).toHaveBeenCalledTimes(1);
      expect(mockApi.listEnvironments).toHaveBeenCalledTimes(1);
      expect(mockApi.listStates).toHaveBeenCalledTimes(1);
      expect(mockApi.listWorkers).toHaveBeenCalledTimes(1);

      const services = get(storesModule.services);
      const environments = get(storesModule.environments);
      const states = get(storesModule.states);
      const workers = get(storesModule.workers);

      expect(services).toHaveLength(2);
      expect(environments).toHaveLength(2);
      expect(states).toHaveLength(2);
      expect(workers).toHaveLength(2);
    });

    it('should complete even if one loader fails', async () => {
      const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
      
      mockApi.listServices.mockRejectedValue(new Error('Services failed'));
      mockApi.listEnvironments.mockResolvedValue([{ id: 'env-1' }]);

      await storesModule.loadAll();

      expect(mockApi.listServices).toHaveBeenCalled();
      expect(mockApi.listEnvironments).toHaveBeenCalled();

      const environments = get(storesModule.environments);
      expect(environments).toHaveLength(1);

      consoleError.mockRestore();
    });
  });

  describe('driftedStates derived store', () => {
    it('should filter states by drift_status', async () => {
      await storesModule.loadStates();

      const drifted = get(storesModule.driftedStates);
      
      expect(drifted).toHaveLength(1);
      expect(drifted[0].drift_status).toBe('drifted');
    });

    it('should update when states change', async () => {
      await storesModule.loadStates();

      let drifted = get(storesModule.driftedStates);
      expect(drifted).toHaveLength(1);

      // Update states
      mockApi.listStates.mockResolvedValue([
        { id: 'state-1', drift_status: 'drifted' },
        { id: 'state-2', drift_status: 'drifted' },
        { id: 'state-3', drift_status: 'synced' }
      ]);

      await storesModule.loadStates();

      drifted = get(storesModule.driftedStates);
      expect(drifted).toHaveLength(2);
    });

    it('should return empty array when no drifted states', async () => {
      mockApi.listStates.mockResolvedValue([
        { id: 'state-1', drift_status: 'synced' },
        { id: 'state-2', drift_status: 'synced' }
      ]);

      await storesModule.loadStates();

      const drifted = get(storesModule.driftedStates);
      expect(drifted).toHaveLength(0);
    });
  });

  describe('subscribeToEvents', () => {
    it('should call api.streamEvents and set up event handler', () => {
      let eventCallback = null;

      mockApi.streamEvents.mockImplementation((types, onEvent) => {
        eventCallback = onEvent;
        return () => {};
      });

      storesModule.subscribeToEvents();

      expect(mockApi.streamEvents).toHaveBeenCalledWith(
        [],
        expect.any(Function)
      );
      expect(eventCallback).not.toBeNull();
    });

    it('should add events to store when received', () => {
      let eventCallback = null;

      mockApi.streamEvents.mockImplementation((types, onEvent) => {
        eventCallback = onEvent;
        return () => {};
      });

      storesModule.subscribeToEvents();

      const event1 = { id: 'evt-1', type: 'service.created', timestamp: '2026-04-29T12:00:00Z' };
      const event2 = { id: 'evt-2', type: 'deployment.approved', timestamp: '2026-04-29T12:01:00Z' };

      eventCallback(event1);
      eventCallback(event2);

      const events = get(storesModule.events);
      expect(events).toHaveLength(2);
      expect(events[0]).toEqual(event2); // Most recent first
      expect(events[1]).toEqual(event1);
    });

    it('should cap events at 100', () => {
      let eventCallback = null;

      mockApi.streamEvents.mockImplementation((types, onEvent) => {
        eventCallback = onEvent;
        return () => {};
      });

      storesModule.subscribeToEvents();

      // Add 150 events
      for (let i = 0; i < 150; i++) {
        eventCallback({ id: `evt-${i}`, type: 'test.event', timestamp: new Date().toISOString() });
      }

      const events = get(storesModule.events);
      expect(events).toHaveLength(100);
      expect(events[0].id).toBe('evt-149'); // Most recent
    });

    it('should trigger loadStates on deployment events', () => {
      let eventCallback = null;

      mockApi.streamEvents.mockImplementation((types, onEvent) => {
        eventCallback = onEvent;
        return () => {};
      });

      storesModule.subscribeToEvents();

      mockApi.listStates.mockClear();

      eventCallback({ id: 'evt-1', type: 'deployment.completed' });

      expect(mockApi.listStates).toHaveBeenCalledTimes(1);
    });

    it('should trigger loadStates on drift events', () => {
      let eventCallback = null;

      mockApi.streamEvents.mockImplementation((types, onEvent) => {
        eventCallback = onEvent;
        return () => {};
      });

      storesModule.subscribeToEvents();

      mockApi.listStates.mockClear();

      eventCallback({ id: 'evt-2', type: 'drift.detected' });

      expect(mockApi.listStates).toHaveBeenCalledTimes(1);
    });

    it('should not trigger loadStates on other event types', () => {
      let eventCallback = null;

      mockApi.streamEvents.mockImplementation((types, onEvent) => {
        eventCallback = onEvent;
        return () => {};
      });

      storesModule.subscribeToEvents();

      mockApi.listStates.mockClear();

      eventCallback({ id: 'evt-3', type: 'service.created' });

      expect(mockApi.listStates).not.toHaveBeenCalled();
    });

    it('should close previous subscription when called again', () => {
      const cleanup1 = vi.fn();
      const cleanup2 = vi.fn();

      mockApi.streamEvents.mockReturnValueOnce(cleanup1).mockReturnValueOnce(cleanup2);

      storesModule.subscribeToEvents();
      storesModule.subscribeToEvents();

      expect(cleanup1).toHaveBeenCalledTimes(1);
      expect(mockApi.streamEvents).toHaveBeenCalledTimes(2);
    });
  });

  describe('unsubscribeFromEvents', () => {
    it('should call cleanup function when unsubscribing', () => {
      const cleanup = vi.fn();

      mockApi.streamEvents.mockReturnValue(cleanup);

      storesModule.subscribeToEvents();
      storesModule.unsubscribeFromEvents();

      expect(cleanup).toHaveBeenCalledTimes(1);
    });

    it('should be safe to call when not subscribed', () => {
      expect(() => {
        storesModule.unsubscribeFromEvents();
      }).not.toThrow();
    });

    it('should allow resubscribing after unsubscribe', () => {
      const cleanup1 = vi.fn();
      const cleanup2 = vi.fn();

      mockApi.streamEvents.mockReturnValueOnce(cleanup1).mockReturnValueOnce(cleanup2);

      storesModule.subscribeToEvents();
      storesModule.unsubscribeFromEvents();
      storesModule.subscribeToEvents();

      expect(mockApi.streamEvents).toHaveBeenCalledTimes(2);
      expect(cleanup1).toHaveBeenCalledTimes(1);
    });
  });
});
