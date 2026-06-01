import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('$lib/nostr/encrypted-controlplane.js', () => ({
  encryptedRequestsAvailable: vi.fn(),
  requestEncryptedResult: vi.fn()
}));

vi.mock('$lib/api/client.js', () => ({
  default: {
    listOrgs: vi.fn(),
    getMyInvites: vi.fn(),
    getOrg: vi.fn(),
    listOrgMembers: vi.fn(),
    listOrgInvites: vi.fn(),
    createOrg: vi.fn(),
    deleteOrg: vi.fn(),
    acceptInvite: vi.fn(),
    createOrgInvite: vi.fn(),
    revokeOrgInvite: vi.fn(),
    updateOrgMemberRole: vi.fn(),
    removeOrgMember: vi.fn()
  }
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
  let apiClient;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    authStore = await import('$lib/stores/auth.js');
    encryptedRequests = await import('$lib/nostr/encrypted-controlplane.js');
    systemStore = await import('$lib/stores/system.svelte.js');
    paymentsStore = await import('$lib/stores/payments.svelte.js');
    orgsStore = await import('$lib/stores/orgs.svelte.js');
    apiClient = (await import('$lib/api/client.js')).default;
    paymentsStore.resetPaymentHistory();
    orgsStore.resetOrgsState();
    orgsStore.resetOrgDetailState();
    authStore.authState.status = 'authenticated';
    authStore.authState.pubkey = 'f'.repeat(64);
    authStore.initializeAuth.mockImplementation(async () => authStore.authState);
    encryptedRequests.encryptedRequestsAvailable.mockReturnValue(true);
    systemStore.currentSystemInfo.mockReturnValue({
      nostr: {
        browser_encrypted_request_relays: ['wss://encrypted.test.local'],
        service_pubkey: 'b'.repeat(64)
      }
    });
    systemStore.loadSystemInfo.mockResolvedValue(systemStore.currentSystemInfo());
    apiClient.listOrgs.mockResolvedValue([]);
    apiClient.getMyInvites.mockResolvedValue([]);
    apiClient.getOrg.mockResolvedValue(null);
    apiClient.listOrgMembers.mockResolvedValue([]);
    apiClient.listOrgInvites.mockResolvedValue([]);
    apiClient.createOrg.mockResolvedValue({ id: 'org-1' });
    apiClient.deleteOrg.mockResolvedValue(null);
    apiClient.acceptInvite.mockResolvedValue({ org_id: 'org-1', role: 'viewer' });
    apiClient.createOrgInvite.mockResolvedValue({ id: 'invite-2' });
    apiClient.revokeOrgInvite.mockResolvedValue(null);
    apiClient.updateOrgMemberRole.mockResolvedValue({ message: 'role updated' });
    apiClient.removeOrgMember.mockResolvedValue(null);
  });

  it('waits for auth and system readiness before requesting encrypted payment history', async () => {
    const discoveredInfo = {
      nostr: {
        browser_encrypted_request_relays: ['wss://encrypted.test.local'],
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

  it('requests payment history records through encrypted Nostr requests without mutating shared store state', async () => {
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

  it('loads payment history through encrypted Nostr requests only', async () => {
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

  it('loads org overview and accepts invites through the REST API client', async () => {
    apiClient.listOrgs.mockResolvedValueOnce([{ id: 'org-1', role: 'owner' }]);
    apiClient.getMyInvites.mockResolvedValueOnce([{ id: 'invite-1', org_name: 'demo' }]);
    apiClient.acceptInvite.mockResolvedValueOnce({ org_id: 'org-1', role: 'viewer' });

    const overview = await orgsStore.loadOrgsOverview();
    const accepted = await orgsStore.acceptInvite('invite-1');

    expect(apiClient.listOrgs).toHaveBeenCalledTimes(1);
    expect(apiClient.getMyInvites).toHaveBeenCalledTimes(1);
    expect(apiClient.acceptInvite).toHaveBeenCalledWith('invite-1');
    expect(overview.orgs).toEqual([{ id: 'org-1', role: 'owner' }]);
    expect(overview.myInvites).toEqual([{ id: 'invite-1', org_name: 'demo' }]);
    expect(accepted).toEqual({ org_id: 'org-1', role: 'viewer' });
  });

  it('loads org detail and sends member mutations through the REST API client', async () => {
    authStore.authState.pubkey = 'alice';
    apiClient.getOrg.mockResolvedValueOnce({ id: 'org-1' });
    apiClient.listOrgMembers.mockResolvedValueOnce([{ pubkey: 'alice', role: 'owner' }]);
    apiClient.listOrgInvites.mockResolvedValueOnce([]);
    apiClient.updateOrgMemberRole.mockResolvedValueOnce({ message: 'role updated' });
    apiClient.createOrgInvite.mockResolvedValueOnce({ id: 'invite-2' });

    await orgsStore.loadOrgDetail('org-1');
    await orgsStore.updateOrgMemberRole('org-1', 'bob', { role: 'admin' });
    await orgsStore.createOrgInvite('org-1', { pubkey: 'carol', role: 'viewer', expiresIn: 168 });

    expect(orgsStore.orgDetailState.myRole).toBe('owner');
    expect(apiClient.getOrg).toHaveBeenCalledWith('org-1');
    expect(apiClient.listOrgMembers).toHaveBeenCalledWith('org-1');
    expect(apiClient.listOrgInvites).toHaveBeenCalledWith('org-1');
    expect(apiClient.updateOrgMemberRole).toHaveBeenCalledWith('org-1', 'bob', { role: 'admin' });
    expect(apiClient.createOrgInvite).toHaveBeenCalledWith('org-1', { pubkey: 'carol', role: 'viewer', expiresIn: 168 });
  });
});
