import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('$lib/nostr/encrypted-controlplane.js', () => ({
  encryptedRequestsAvailable: vi.fn(),
  requestEncryptedResult: vi.fn()
}));

vi.mock('$lib/stores/auth.js', () => {
  const authState = { status: 'authenticated', pubkey: 'f'.repeat(64) };
  return {
    authState,
    initializeAuth: vi.fn(async () => authState)
  };
});

vi.mock('$lib/stores/system.svelte.js', () => ({
  currentSystemInfo: vi.fn(),
  loadSystemInfo: vi.fn()
}));

describe('encrypted payments/orgs stores', () => {
  let authStore;
  let encryptedRequests;
  let paymentsStore;
  let systemStore;
  let orgsStore;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    authStore = await import('$lib/stores/auth.js');
    encryptedRequests = await import('$lib/nostr/encrypted-controlplane.js');
    systemStore = await import('$lib/stores/system.svelte.js');
    paymentsStore = await import('$lib/stores/payments.svelte.js');
    orgsStore = await import('$lib/stores/orgs.svelte.js');
    paymentsStore.resetPaymentHistory();
    orgsStore.resetOrgsState();
    orgsStore.resetOrgDetailState();
    authStore.authState.status = 'authenticated';
    authStore.authState.pubkey = 'f'.repeat(64);
    authStore.initializeAuth.mockImplementation(async () => authStore.authState);
    encryptedRequests.encryptedRequestsAvailable.mockReturnValue(true);
    systemStore.currentSystemInfo.mockReturnValue({
      nostr: {
        browser_relays: ['wss://encrypted.test.local'],
        service_pubkey: 'b'.repeat(64)
      }
    });
    systemStore.loadSystemInfo.mockResolvedValue(systemStore.currentSystemInfo());
  });

  it('waits for auth and system readiness before requesting encrypted payment history', async () => {
    const discoveredInfo = {
      nostr: {
        browser_relays: ['wss://encrypted.test.local'],
        service_pubkey: 'b'.repeat(64)
      }
    };
    authStore.authState.status = 'checking';
    authStore.authState.pubkey = null;
    authStore.initializeAuth.mockImplementation(async () => {
      authStore.authState.status = 'authenticated';
      authStore.authState.pubkey = 'f'.repeat(64);
      return authStore.authState;
    });
    systemStore.currentSystemInfo.mockReturnValue(null);
    systemStore.loadSystemInfo.mockResolvedValue(discoveredInfo);
    encryptedRequests.requestEncryptedResult.mockResolvedValue({
      result: {
        status: 'ok',
        payload: [{ id: 'pay-ready', worker_pubkey: 'worker-a', amount_sats: 7 }]
      }
    });

    const records = await paymentsStore.requestPaymentHistoryRecords({ worker: 'worker-a' });

    expect(systemStore.loadSystemInfo).toHaveBeenCalledTimes(1);
    expect(encryptedRequests.encryptedRequestsAvailable).toHaveBeenCalledWith(discoveredInfo);
    expect(authStore.initializeAuth).toHaveBeenCalledTimes(1);
    expect(records).toEqual([{ id: 'pay-ready', worker_pubkey: 'worker-a', amount_sats: 7 }]);
  });

  it('requests payment history records through ContextVM requests without mutating shared store state', async () => {
    encryptedRequests.requestEncryptedResult.mockResolvedValue({
      result: {
        status: 'ok',
        payload: [{ id: 'pay-1', worker_pubkey: 'worker-a', amount_sats: 42 }]
      }
    });

    const records = await paymentsStore.requestPaymentHistoryRecords({ worker: ' worker-a ', limit: 25 });

    expect(encryptedRequests.requestEncryptedResult).toHaveBeenCalledWith({
      operation: 'payments.history',
      payload: { worker: 'worker-a', limit: 25 },
      tags: [['domain', 'payments']]
    });
    expect(records).toEqual([{ id: 'pay-1', worker_pubkey: 'worker-a', amount_sats: 42 }]);
    expect(paymentsStore.paymentHistoryState.records).toEqual([]);
    expect(paymentsStore.paymentHistoryState.loadedWorker).toBe('');
  });

  it('loads payment history through ContextVM requests only', async () => {
    encryptedRequests.requestEncryptedResult.mockResolvedValue({
      result: {
        status: 'ok',
        payload: [{ id: 'pay-1', worker_pubkey: 'worker-a', amount_sats: 42 }]
      }
    });

    const records = await paymentsStore.loadPaymentHistory({ worker: ' worker-a ', limit: 25 });

    expect(encryptedRequests.requestEncryptedResult).toHaveBeenCalledWith({
      operation: 'payments.history',
      payload: { worker: 'worker-a', limit: 25 },
      tags: [['domain', 'payments']]
    });
    expect(records).toEqual([{ id: 'pay-1', worker_pubkey: 'worker-a', amount_sats: 42 }]);
    expect(paymentsStore.paymentHistoryState.loadedWorker).toBe('worker-a');
  });

  it('throws encrypted result errors without falling back to REST', async () => {
    encryptedRequests.requestEncryptedResult.mockResolvedValue({
      result: {
        status: 'error',
        error: { code: 'handler_failed', message: 'worker is required' }
      }
    });

    await expect(paymentsStore.loadPaymentHistory({ worker: 'worker-a' })).rejects.toThrow('worker is required');
    expect(paymentsStore.paymentHistoryState.records).toEqual([]);
    expect(paymentsStore.paymentHistoryState.error).toBe('worker is required');
  });

  it('loads org overview and accepts invites through encrypted operations', async () => {
    encryptedRequests.requestEncryptedResult
      .mockResolvedValueOnce({ result: { status: 'ok', payload: [{ id: 'org-1', role: 'owner' }] } })
      .mockResolvedValueOnce({ result: { status: 'ok', payload: [{ id: 'invite-1', org_name: 'demo' }] } })
      .mockResolvedValueOnce({ result: { status: 'ok', payload: { org_id: 'org-1', role: 'viewer' } } });

    const overview = await orgsStore.loadOrgsOverview();
    const accepted = await orgsStore.acceptInvite('invite-1');

    expect(encryptedRequests.requestEncryptedResult).toHaveBeenNthCalledWith(1, {
      operation: 'orgs.list',
      payload: {},
      tags: [['domain', 'orgs']]
    });
    expect(encryptedRequests.requestEncryptedResult).toHaveBeenNthCalledWith(2, {
      operation: 'orgs.my_invites',
      payload: {},
      tags: [['domain', 'orgs']]
    });
    expect(encryptedRequests.requestEncryptedResult).toHaveBeenNthCalledWith(3, {
      operation: 'orgs.accept_invite',
      payload: { invite_id: 'invite-1' },
      tags: [['domain', 'orgs']]
    });
    expect(overview.orgs).toEqual([{ id: 'org-1', role: 'owner' }]);
    expect(overview.myInvites).toEqual([{ id: 'invite-1', org_name: 'demo' }]);
    expect(accepted).toEqual({ org_id: 'org-1', role: 'viewer' });
  });

  it('returns raw organization create results without requiring a payload envelope', async () => {
    encryptedRequests.requestEncryptedResult.mockResolvedValueOnce({
      result: { id: 'org-raw', name: 'demo', display_name: 'Demo Org' }
    });

    const created = await orgsStore.createOrg({ name: 'demo', displayName: 'Demo Org' });

    expect(encryptedRequests.requestEncryptedResult).toHaveBeenCalledWith({
      operation: 'orgs.create',
      payload: { name: 'demo', display_name: 'Demo Org' },
      tags: [['domain', 'orgs']]
    });
    expect(created).toEqual({ id: 'org-raw', name: 'demo', display_name: 'Demo Org' });
  });

  it('waits for system discovery before issuing encrypted org requests', async () => {
    const discoveredInfo = {
      features: { encrypted_nostr_requests: true },
      nostr: {
        browser_relays: ['wss://encrypted.test.local'],
        service_pubkey: 'b'.repeat(64)
      }
    };
    systemStore.currentSystemInfo.mockReturnValue(null);
    systemStore.loadSystemInfo.mockResolvedValue(discoveredInfo);
    encryptedRequests.requestEncryptedResult
      .mockResolvedValueOnce({ result: { status: 'ok', payload: [] } })
      .mockResolvedValueOnce({ result: { status: 'ok', payload: [] } });

    await orgsStore.loadOrgsOverview();

    expect(systemStore.loadSystemInfo).toHaveBeenCalledTimes(2);
    expect(encryptedRequests.encryptedRequestsAvailable).toHaveBeenCalledWith(discoveredInfo);
    expect(encryptedRequests.requestEncryptedResult).toHaveBeenNthCalledWith(1, {
      operation: 'orgs.list',
      payload: {},
      tags: [['domain', 'orgs']]
    });
  });

  it('loads org detail and sends member mutations as encrypted operations', async () => {
    encryptedRequests.requestEncryptedResult
      .mockResolvedValueOnce({
        result: {
          status: 'ok',
          payload: { org: { id: 'org-1' }, members: [{ pubkey: 'alice', role: 'owner' }], invites: [], my_role: 'owner' }
        }
      })
      .mockResolvedValueOnce({ result: { status: 'ok', payload: { message: 'role updated' } } })
      .mockResolvedValueOnce({ result: { status: 'ok', payload: { id: 'invite-2' } } });

    await orgsStore.loadOrgDetail('org-1');
    await orgsStore.updateOrgMemberRole('org-1', 'bob', { role: 'admin' });
    await orgsStore.createOrgInvite('org-1', { pubkey: 'carol', role: 'viewer', expiresIn: 168 });

    expect(orgsStore.orgDetailState.myRole).toBe('owner');
    expect(encryptedRequests.requestEncryptedResult).toHaveBeenNthCalledWith(2, {
      operation: 'orgs.update_member_role',
      payload: { org_id: 'org-1', pubkey: 'bob', role: 'admin' },
      tags: [['domain', 'orgs']]
    });
    expect(encryptedRequests.requestEncryptedResult).toHaveBeenNthCalledWith(3, {
      operation: 'orgs.create_invite',
      payload: { org_id: 'org-1', pubkey: 'carol', role: 'viewer', expires_in: 168 },
      tags: [['domain', 'orgs']]
    });
  });
});
