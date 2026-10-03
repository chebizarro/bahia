import { beforeEach, describe, expect, it, vi } from 'vitest';

const intentMock = vi.hoisted(() => vi.fn(async request => ({ id: request.coordinate, pending: true })));
vi.mock('../../src/lib/stores/sensitive-intents.svelte.js', () => ({ submitSensitiveIntent: intentMock }));

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
    encryptedRequests.requestEncryptedResult.mockReset();
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

  it('does not make a ContextVM read for payment history', () => {
    expect(paymentsStore.requestPaymentHistoryRecords({ worker: 'worker-a' })).toEqual([]);
    expect(encryptedRequests.requestEncryptedResult).not.toHaveBeenCalled();
  });

  it('loads org overview and accepts invites through signed intents', async () => {
    encryptedRequests.requestEncryptedResult
      .mockResolvedValueOnce({ result: { status: 'ok', payload: [{ id: 'org-1', role: 'owner' }] } })
      .mockResolvedValueOnce({ result: { status: 'ok', payload: [{ id: 'invite-1', org_id: 'org-1', role: 'viewer', org_name: 'demo' }] } });

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
    expect(encryptedRequests.requestEncryptedResult).toHaveBeenCalledTimes(2);
    expect(intentMock).toHaveBeenCalledWith(expect.objectContaining({ domain: 'org', schema: 'bahia.intent.org-member.v1',
      content: expect.objectContaining({ invite_id: 'invite-1', role: 'viewer' }) }));
    expect(overview.orgs).toEqual([{ id: 'org-1', role: 'owner' }]);
    expect(overview.myInvites).toEqual([{ id: 'invite-1', org_id: 'org-1', role: 'viewer', org_name: 'demo' }]);
    expect(accepted.pending).toBe(true);
  });

  it('mints an org id and submits a gift-wrapped create intent', async () => {
    const created = await orgsStore.createOrg({ name: 'demo', displayName: 'Demo Org' });
    expect(created.id).toMatch(/^[0-9a-f-]{36}$/);
    expect(intentMock).toHaveBeenCalledWith(expect.objectContaining({ domain: 'org', op: 'create', coordinate: created.id,
      orgId: created.id, content: { id: created.id, name: 'demo', display_name: 'Demo Org' } }));
    expect(encryptedRequests.requestEncryptedResult).not.toHaveBeenCalled();
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

  it('loads org detail and submits member and invite intents', async () => {
    encryptedRequests.requestEncryptedResult
      .mockResolvedValueOnce({
        result: {
          status: 'ok',
          payload: { org: { id: 'org-1' }, members: [{ pubkey: 'alice', role: 'owner' }], invites: [], my_role: 'owner' }
        }
      });

    await orgsStore.loadOrgDetail('org-1');
    await orgsStore.updateOrgMemberRole('org-1', 'bob', { role: 'admin' });
    await orgsStore.createOrgInvite('org-1', { pubkey: 'carol', role: 'viewer', expiresIn: 168 });

    expect(orgsStore.orgDetailState.myRole).toBe('owner');
    expect(encryptedRequests.requestEncryptedResult).toHaveBeenCalledTimes(1);
    expect(intentMock).toHaveBeenCalledWith(expect.objectContaining({ schema: 'bahia.intent.org-member.v1',
      content: { pubkey: 'bob', role: 'admin' } }));
    expect(intentMock).toHaveBeenCalledWith(expect.objectContaining({ schema: 'bahia.intent.org-invite.v1',
      content: expect.objectContaining({ pubkey: 'carol', role: 'viewer', expires_in: 168 }) }));
    expect(orgsStore.orgMemberListState).toEqual({ orgID: 'org-1', members: [{ pubkey: 'alice', role: 'owner' }] });
  });
});
