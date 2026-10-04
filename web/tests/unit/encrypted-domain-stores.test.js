import { beforeEach, describe, expect, it, vi } from 'vitest';

const intentMock = vi.hoisted(() => vi.fn(async request => ({ id: request.coordinate, pending: true })));
vi.mock('../../src/lib/stores/sensitive-intents.svelte.js', () => ({ submitSensitiveIntent: intentMock }));
vi.mock('$lib/nostr/intent-client.svelte.js', () => ({ publishIntent: intentMock }));

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

const topicMock = vi.hoisted(() => ({ rows: new Map() }));
vi.mock('../../src/lib/stores/collections/confidential-records.js', () => ({
  readConfidentialTopic: (topic) => ({ rows: topicMock.rows.get(topic) || [] })
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
    topicMock.rows.clear();
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

  it('loads canonical org and invite records from the event-store view and accepts via intent', async () => {
    topicMock.rows.set('org', [{ id: 'org-1', role: 'owner' }]);
    topicMock.rows.set('org-invite', [{ id: 'invite-1', org_id: 'org-1', role: 'viewer', pubkey: authStore.authState.pubkey }]);
    const overview = await orgsStore.loadOrgsOverview();
    const accepted = await orgsStore.acceptInvite('invite-1');
    expect(encryptedRequests.requestEncryptedResult).not.toHaveBeenCalled();
    expect(intentMock).toHaveBeenCalledWith(expect.objectContaining({ domain: 'org', schema: 'bahia.intent.org-member.v1',
      content: expect.objectContaining({ invite_id: 'invite-1', role: 'viewer' }) }));
    expect(overview.orgs).toHaveLength(1);
    expect(overview.myInvites).toHaveLength(1);
    expect(accepted.pending).toBe(true);
  });

  it('mints an org id and submits a gift-wrapped create intent', async () => {
    const created = await orgsStore.createOrg({ name: 'demo', displayName: 'Demo Org' });
    expect(created.id).toMatch(/^[0-9a-f-]{36}$/);
    expect(intentMock).toHaveBeenCalledWith(expect.objectContaining({ domain: 'org', op: 'create', coordinate: created.id,
      orgId: created.id, content: { id: created.id, name: 'demo', display_name: 'Demo Org' } }));
    expect(encryptedRequests.requestEncryptedResult).not.toHaveBeenCalled();
  });

  it('reads the local org snapshot without a discovery or ContextVM round trip', async () => {
    systemStore.currentSystemInfo.mockReturnValue(null);
    topicMock.rows.set('org', [{ id: 'org-2', name: 'Offline' }]);
    await expect(orgsStore.loadOrgsOverview()).resolves.toMatchObject({ orgs: [{ id: 'org-2', name: 'Offline' }] });
    expect(systemStore.loadSystemInfo).not.toHaveBeenCalled();
    expect(encryptedRequests.requestEncryptedResult).not.toHaveBeenCalled();
  });

  it('loads canonical org detail and submits member and invite intents', async () => {
    topicMock.rows.set('org', [{ id: 'org-1' }]);
    topicMock.rows.set('org-member', [{ org_id: 'org-1', pubkey: 'alice', role: 'owner' }]);
    await orgsStore.loadOrgDetail('org-1');
    await orgsStore.updateOrgMemberRole('org-1', 'bob', { role: 'admin' });
    await orgsStore.createOrgInvite('org-1', { pubkey: 'carol', role: 'viewer', expiresIn: 168 });
    expect(encryptedRequests.requestEncryptedResult).not.toHaveBeenCalled();
    expect(intentMock).toHaveBeenCalledWith(expect.objectContaining({ schema: 'bahia.intent.org-member.v1',
      content: { pubkey: 'bob', role: 'admin' } }));
    expect(intentMock).toHaveBeenCalledWith(expect.objectContaining({ schema: 'bahia.intent.org-invite.v1',
      content: expect.objectContaining({ pubkey: 'carol', role: 'viewer', expires_in: 168 }) }));
    expect(orgsStore.orgMemberListState).toEqual({ orgID: 'org-1', members: [{ org_id: 'org-1', pubkey: 'alice', role: 'owner' }] });
  });
});
