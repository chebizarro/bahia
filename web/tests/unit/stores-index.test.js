import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest';

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

const controlplaneMock = vi.hoisted(() => ({
  services: [],
  environments: [],
  states: [],
  workers: [],
  events: [],
  loading: { services: false, environments: false, states: false, workers: false },
  controlplaneConnection: { status: 'idle', ready: false, rollbackToSse: false },
  bootstrapControlplane: vi.fn(),
  disconnectControlplane: vi.fn(),
  ingestLegacyEvent: vi.fn()
}));

vi.mock('../../src/lib/stores/controlplane.svelte.js', () => controlplaneMock);

vi.mock('../../src/lib/stores/sse.svelte.js', () => ({
  connectEventStream: vi.fn(),
  disconnectEventStream: vi.fn()
}));

describe('Global Stores (index.js)', () => {
  let storesModule;
  let mockApi;
  let sseModule;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();

    controlplaneMock.services.length = 0;
    controlplaneMock.environments.length = 0;
    controlplaneMock.states.length = 0;
    controlplaneMock.workers.length = 0;
    controlplaneMock.events.length = 0;
    Object.assign(controlplaneMock.loading, { services: false, environments: false, states: false, workers: false });
    Object.assign(controlplaneMock.controlplaneConnection, { status: 'idle', ready: false, rollbackToSse: false });
    controlplaneMock.bootstrapControlplane.mockResolvedValue({ ok: true, rollbackToSse: false });

    const apiModule = await import('../../src/lib/api/client.js');
    mockApi = apiModule.api;
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

    sseModule = await import('../../src/lib/stores/sse.svelte.js');
    storesModule = await import('../../src/lib/stores/index.js');
  });

  afterEach(() => {
    storesModule.unsubscribeFromEvents?.();
  });

  it('re-exports Nostr-backed controlplane arrays', () => {
    controlplaneMock.services.push({ id: 'svc-1' });
    controlplaneMock.events.push({ id: 'evt-1' });

    expect(storesModule.services).toBe(controlplaneMock.services);
    expect(storesModule.events).toBe(controlplaneMock.events);
    expect(storesModule.serviceCount()).toBe(1);
  });

  it('keeps REST loaders as transitional manual refresh/fallback paths', async () => {
    await storesModule.loadServices();
    await storesModule.loadEnvironments();
    await storesModule.loadStates();
    await storesModule.loadWorkers();

    expect(mockApi.listServices).toHaveBeenCalledTimes(1);
    expect(mockApi.listEnvironments).toHaveBeenCalledTimes(1);
    expect(mockApi.listStates).toHaveBeenCalledTimes(1);
    expect(mockApi.listWorkers).toHaveBeenCalledTimes(1);
    expect(storesModule.services).toHaveLength(2);
    expect(storesModule.environments).toHaveLength(2);
    expect(storesModule.states).toHaveLength(2);
    expect(storesModule.workers).toHaveLength(2);
  });

  it('does not let REST refreshes overwrite authoritative relay-backed state', async () => {
    controlplaneMock.controlplaneConnection.ready = true;
    controlplaneMock.services.push({ id: 'svc-relay', name: 'Relay Service' });

    await storesModule.loadServices();

    expect(mockApi.listServices).not.toHaveBeenCalled();
    expect(storesModule.services).toEqual([{ id: 'svc-relay', name: 'Relay Service' }]);
  });

  it('loadAll bootstraps the relay-backed controlplane before falling back to REST', async () => {
    await storesModule.loadAll();

    expect(controlplaneMock.bootstrapControlplane).toHaveBeenCalledTimes(1);
    expect(mockApi.listServices).not.toHaveBeenCalled();
  });

  it('loadAll uses REST fallback when relay bootstrap fails', async () => {
    controlplaneMock.bootstrapControlplane.mockResolvedValueOnce({ ok: false, rollbackToSse: true, reason: 'no relay' });

    await storesModule.loadAll();

    expect(mockApi.listServices).toHaveBeenCalledTimes(1);
    expect(mockApi.listEnvironments).toHaveBeenCalledTimes(1);
    expect(mockApi.listStates).toHaveBeenCalledTimes(1);
    expect(mockApi.listWorkers).toHaveBeenCalledTimes(1);
  });

  it('subscribeToEvents starts Nostr bootstrap and does not call api.streamEvents', async () => {
    storesModule.subscribeToEvents();
    await Promise.resolve();

    expect(controlplaneMock.bootstrapControlplane).toHaveBeenCalledTimes(1);
    expect(mockApi.streamEvents).not.toHaveBeenCalled();
    expect(sseModule.connectEventStream).not.toHaveBeenCalled();
  });

  it('uses the SSE store only as a flagged rollback bridge', async () => {
    controlplaneMock.bootstrapControlplane.mockResolvedValueOnce({ ok: false, rollbackToSse: true, reason: 'relay unavailable' });

    storesModule.subscribeToEvents();
    await Promise.resolve();
    await Promise.resolve();

    expect(mockApi.streamEvents).not.toHaveBeenCalled();
    expect(sseModule.connectEventStream).toHaveBeenCalledWith({ onEvent: controlplaneMock.ingestLegacyEvent });
  });

  it('unsubscribe tears down rollback SSE and live relay subscription', async () => {
    controlplaneMock.bootstrapControlplane.mockResolvedValueOnce({ ok: false, rollbackToSse: true, reason: 'relay unavailable' });
    storesModule.subscribeToEvents();
    await Promise.resolve();
    await Promise.resolve();

    storesModule.unsubscribeFromEvents();

    expect(sseModule.disconnectEventStream).toHaveBeenCalledTimes(1);
    expect(controlplaneMock.disconnectControlplane).toHaveBeenCalled();
  });

  it('filters drifted states from relay-backed state', () => {
    controlplaneMock.states.push(
      { id: 'state-1', drift_status: 'drifted' },
      { id: 'state-2', drift_status: 'in_sync' }
    );

    expect(storesModule.driftedStates()).toEqual([{ id: 'state-1', drift_status: 'drifted' }]);
    expect(storesModule.driftCount()).toBe(1);
  });
});
