import { beforeEach, describe, expect, it, vi } from 'vitest';

const submit = vi.hoisted(() => vi.fn(async request => ({ id: request.coordinate, pending: true })));
const rpc = vi.hoisted(() => vi.fn());
const ORG = '0199c749-9300-7444-8444-444444444444';
const CREDENTIAL_ID = `cred_${'a'.repeat(64)}`;

vi.mock('../../src/lib/stores/sensitive-intents.svelte.js', () => ({
  orgIdFor: record => record?.org_id || ORG,
  submitSensitiveIntent: submit
}));
vi.mock('$lib/stores/sensitive-intents.svelte.js', () => ({
  orgIdFor: record => record?.org_id || ORG,
  submitSensitiveIntent: submit
}));
vi.mock('$lib/nostr/encrypted-controlplane.js', () => ({
  requestEncryptedResult: rpc,
  encryptedRequestsAvailable: () => true
}));
vi.mock('$lib/stores/system.svelte.js', () => ({
  currentSystemInfo: () => ({ nostr: { service_pubkey: 'b'.repeat(64) } }),
  loadSystemInfo: async () => ({ nostr: { service_pubkey: 'b'.repeat(64) } })
}));

beforeEach(() => {
  vi.resetModules();
  submit.mockClear(); rpc.mockClear();
});

describe('sensitive store intent migration', () => {
  it('notification CRUD submits full desired state without ContextVM RPC', async () => {
    const store = await import('../../src/lib/stores/notifications.svelte.js');
    const created = await store.createNotificationChannel({ org_id: ORG, name: 'Ops', channel_type: 'webhook', config: { url: 'https://secret.example' } });
    expect(submit).toHaveBeenCalledWith(expect.objectContaining({ domain: 'notification', op: 'create', orgId: ORG,
      content: expect.objectContaining({ config: { url: 'https://secret.example' } }) }));
    store.notificationState.channels = [{ ...created, updated_at: 42 }];
    await store.updateNotificationChannel(created.id, { enabled: false });
    expect(submit).toHaveBeenLastCalledWith(expect.objectContaining({ domain: 'notification', op: 'update',
      currentRecord: expect.objectContaining({ updated_at: 42 }), content: expect.objectContaining({ enabled: false }) }));
    await store.deleteNotificationChannel(created.id);
    expect(submit).toHaveBeenLastCalledWith(expect.objectContaining({ domain: 'notification', op: 'delete', content: { id: created.id } }));
    expect(rpc).not.toHaveBeenCalled();
  });

  it('secret create/update send only a Signet reference; delete stays an intent and reveal stays RPC', async () => {
    const store = await import('../../src/lib/stores/service-secrets.svelte.js');
    const ref = { store: 'signet', credential_id: CREDENTIAL_ID, version: 1 };
    const created = await store.createServiceSecret('svc-1', { org_id: ORG, name: 'TOKEN', credential_ref: ref });
    expect(submit).toHaveBeenCalledWith(expect.objectContaining({ domain: 'secret', op: 'create', orgId: ORG,
      content: { id: created.id, service_id: 'svc-1', name: 'TOKEN', credential_ref: ref } }));
    store.serviceSecretsState.secretsByService['svc-1'] = [{ ...created, environment_id: 'env-1', updated_at: 42 }];
    const nextRef = { ...ref, version: 2 };
    await store.updateServiceSecret('svc-1', created.id, { credential_ref: nextRef });
    expect(submit).toHaveBeenLastCalledWith(expect.objectContaining({ domain: 'secret', op: 'update',
      currentRecord: expect.objectContaining({ updated_at: 42 }),
      content: { id: created.id, service_id: 'svc-1', name: 'TOKEN', environment_id: 'env-1', credential_ref: nextRef } }));
    expect(JSON.stringify(submit.mock.calls)).not.toMatch(/encrypted_value|"value"/);
    await expect(store.createServiceSecret('svc-1', { org_id: ORG, name: 'BAD', value: 'plaintext', credential_ref: ref })).rejects.toThrow(/cannot be submitted/);
    await store.deleteServiceSecret('svc-1', created.id);
    expect(submit).toHaveBeenLastCalledWith(expect.objectContaining({ domain: 'secret', op: 'delete' }));
    expect(rpc).not.toHaveBeenCalled();
  });
});
