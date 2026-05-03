import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('$lib/nostr/private-controlplane.js', () => ({
  requestPrivateResult: vi.fn()
}));

describe('private payments/orgs stores', () => {
  let privateTransport;
  let paymentsStore;
  let orgsStore;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    privateTransport = await import('$lib/nostr/private-controlplane.js');
    paymentsStore = await import('$lib/stores/payments.svelte.js');
    orgsStore = await import('$lib/stores/orgs.svelte.js');
    paymentsStore.resetPaymentHistory();
    orgsStore.resetOrgsState();
    orgsStore.resetOrgDetailState();
  });

  it('loads payment history through encrypted private transport only', async () => {
    privateTransport.requestPrivateResult.mockResolvedValue({
      result: {
        status: 'ok',
        payload: [{ id: 'pay-1', worker_pubkey: 'worker-a', amount_sats: 42 }]
      }
    });

    const records = await paymentsStore.loadPaymentHistory({ worker: ' worker-a ', limit: 25 });

    expect(privateTransport.requestPrivateResult).toHaveBeenCalledWith({
      operation: 'payments.history',
      payload: { worker: 'worker-a', limit: 25 },
      tags: [['domain', 'payments']]
    });
    expect(records).toEqual([{ id: 'pay-1', worker_pubkey: 'worker-a', amount_sats: 42 }]);
    expect(paymentsStore.paymentHistoryState.loadedWorker).toBe('worker-a');
  });

  it('throws encrypted private result errors without falling back to REST', async () => {
    privateTransport.requestPrivateResult.mockResolvedValue({
      result: {
        status: 'error',
        error: { code: 'handler_failed', message: 'worker is required' }
      }
    });

    await expect(paymentsStore.loadPaymentHistory({ worker: 'worker-a' })).rejects.toThrow('worker is required');
    expect(paymentsStore.paymentHistoryState.records).toEqual([]);
    expect(paymentsStore.paymentHistoryState.error).toBe('worker is required');
  });

  it('loads org overview and accepts invites through private operations', async () => {
    privateTransport.requestPrivateResult
      .mockResolvedValueOnce({ result: { status: 'ok', payload: [{ id: 'org-1', role: 'owner' }] } })
      .mockResolvedValueOnce({ result: { status: 'ok', payload: [{ id: 'invite-1', org_name: 'demo' }] } })
      .mockResolvedValueOnce({ result: { status: 'ok', payload: { org_id: 'org-1', role: 'viewer' } } });

    const overview = await orgsStore.loadOrgsOverview();
    const accepted = await orgsStore.acceptInvite('invite-1');

    expect(privateTransport.requestPrivateResult).toHaveBeenNthCalledWith(1, {
      operation: 'orgs.list',
      payload: {},
      tags: [['domain', 'orgs']]
    });
    expect(privateTransport.requestPrivateResult).toHaveBeenNthCalledWith(2, {
      operation: 'orgs.my_invites',
      payload: {},
      tags: [['domain', 'orgs']]
    });
    expect(privateTransport.requestPrivateResult).toHaveBeenNthCalledWith(3, {
      operation: 'orgs.accept_invite',
      payload: { invite_id: 'invite-1' },
      tags: [['domain', 'orgs']]
    });
    expect(overview.orgs).toEqual([{ id: 'org-1', role: 'owner' }]);
    expect(overview.myInvites).toEqual([{ id: 'invite-1', org_name: 'demo' }]);
    expect(accepted).toEqual({ org_id: 'org-1', role: 'viewer' });
  });

  it('loads org detail and sends member mutations as private operations', async () => {
    privateTransport.requestPrivateResult
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
    expect(privateTransport.requestPrivateResult).toHaveBeenNthCalledWith(2, {
      operation: 'orgs.update_member_role',
      payload: { org_id: 'org-1', pubkey: 'bob', role: 'admin' },
      tags: [['domain', 'orgs']]
    });
    expect(privateTransport.requestPrivateResult).toHaveBeenNthCalledWith(3, {
      operation: 'orgs.create_invite',
      payload: { org_id: 'org-1', pubkey: 'carol', role: 'viewer', expires_in: 168 },
      tags: [['domain', 'orgs']]
    });
  });
});
