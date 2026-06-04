import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest';

const authMock = vi.hoisted(() => ({
  authState: {
    compatibility: { restNip98Ready: true },
    directNip98Ready: true
  },
  isAuthenticated: vi.fn(() => true),
  currentUser: vi.fn(() => ({ pubkey: 'a'.repeat(64) }))
}));

vi.mock('../../src/lib/stores/auth.js', () => authMock);

const controlplaneMock = vi.hoisted(() => ({
  services: [],
  environments: [],
  states: [],
  workers: [],
  events: [],
  loading: { services: false, environments: false, states: false, workers: false },
  controlplaneConnection: { status: 'idle', ready: false },
  bootstrapControlplane: vi.fn(),
  disconnectControlplane: vi.fn()
}));

vi.mock('../../src/lib/stores/controlplane.svelte.js', () => controlplaneMock);

describe('Global Stores (index.js)', () => {
  let storesModule;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();

    controlplaneMock.services.length = 0;
    controlplaneMock.environments.length = 0;
    controlplaneMock.states.length = 0;
    controlplaneMock.workers.length = 0;
    controlplaneMock.events.length = 0;
    Object.assign(controlplaneMock.loading, { services: false, environments: false, states: false, workers: false });
    Object.assign(controlplaneMock.controlplaneConnection, { status: 'idle', ready: false });
    Object.assign(authMock.authState, {
      compatibility: { restNip98Ready: true },
      directNip98Ready: true
    });
    controlplaneMock.bootstrapControlplane.mockResolvedValue({ ok: true });

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

  it('public loaders bootstrap relay read models without REST fallback', async () => {
    await storesModule.loadServices();
    await storesModule.loadEnvironments();
    await storesModule.loadStates();
    await storesModule.loadWorkers();

    expect(controlplaneMock.bootstrapControlplane).toHaveBeenCalledTimes(4);
  });

  it('does not let REST refreshes overwrite authoritative relay-backed state', async () => {
    controlplaneMock.controlplaneConnection.ready = true;
    controlplaneMock.services.push({ id: 'svc-relay', name: 'Relay Service' });

    await storesModule.loadServices();

    expect(storesModule.services).toEqual([{ id: 'svc-relay', name: 'Relay Service' }]);
  });

  it('loadAll bootstraps the relay-backed controlplane without REST fallback', async () => {
    await storesModule.loadAll();

    expect(controlplaneMock.bootstrapControlplane).toHaveBeenCalledTimes(1);
  });

  it('loadAll logs relay bootstrap failures without falling back to REST', async () => {
    const consoleSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    controlplaneMock.bootstrapControlplane.mockResolvedValueOnce({ ok: false, reason: 'no relay' });

    await storesModule.loadAll();

    expect(consoleSpy).toHaveBeenCalledWith('Nostr controlplane bootstrap failed:', 'no relay');
    consoleSpy.mockRestore();
  });

  it('subscribeToEvents starts Nostr bootstrap without SSE fallback', async () => {
    storesModule.subscribeToEvents();
    await Promise.resolve();

    expect(controlplaneMock.bootstrapControlplane).toHaveBeenCalledTimes(1);
  });

  it('unsubscribe tears down the live relay subscription', async () => {
    storesModule.subscribeToEvents();
    await Promise.resolve();

    storesModule.unsubscribeFromEvents();

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
