import { describe, it, expect, beforeEach, vi } from 'vitest';

const encryptedRequestsMock = vi.hoisted(() => ({
  requestEncryptedResult: vi.fn(),
  encryptedRequestsAvailable: vi.fn(() => true),
  servicePubkeyFromSystemInfo: vi.fn(() => 'b'.repeat(64)),
  CONTEXTVM_MESSAGE_KIND: 25910,
  publishEncryptedRequest: vi.fn()
}));

const nip07Mock = vi.hoisted(() => ({
  encryptNip44: vi.fn(async (_pubkey, plaintext) => 'encrypted:' + plaintext)
}));

const bootstrapMock = vi.hoisted(() => vi.fn(async () => ({ ok: true })));

const systemMock = vi.hoisted(() => ({
  currentSystemInfo: vi.fn(() => ({ nostr: { service_pubkey: 'b'.repeat(64), browser_relays: ['wss://requests.example'] } })),
  loadSystemInfo: vi.fn(async () => ({ nostr: { service_pubkey: 'b'.repeat(64), browser_relays: ['wss://requests.example'] } }))
}));

vi.mock('$lib/nostr/encrypted-controlplane.js', () => encryptedRequestsMock);
vi.mock('$lib/stores/system.svelte.js', () => systemMock);
vi.mock('$lib/nostr/nip07-crypto.js', () => nip07Mock);
vi.mock('$lib/stores/controlplane.svelte.js', () => ({ bootstrapControlplane: bootstrapMock }));
vi.mock('../../src/lib/stores/controlplane.svelte.js', () => ({ bootstrapControlplane: bootstrapMock }));
vi.mock('$app/navigation', () => ({ goto: vi.fn() }));

describe('encrypted route stores', () => {
  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
    encryptedRequestsMock.encryptedRequestsAvailable.mockReturnValue(true);
    systemMock.currentSystemInfo.mockReturnValue({ nostr: { service_pubkey: 'b'.repeat(64), browser_relays: ['wss://requests.example'] } });
  });

  it('lists, creates, reveals, and deletes service secrets through encrypted operations', async () => {
    const serviceId = 'svc-123';
    const secretId = 'secret-1';
    encryptedRequestsMock.requestEncryptedResult
      .mockResolvedValueOnce({ result: { secrets: [{ id: secretId, name: 'TOKEN', version: 1 }] } })
      .mockResolvedValueOnce({ requestEventId: 'req-2', result: { secret: { id: 'secret-2', name: 'API_KEY', version: 1 }, status: 'created' } })
      .mockResolvedValueOnce({ result: { status: 'ok', payload: { value: 'plaintext' } } })
      .mockResolvedValueOnce({ requestEventId: 'req-4', result: { status: 'deleted', secret_id: secretId } });

    const store = await import('../../src/lib/stores/service-secrets.svelte.js');

    await expect(store.listServiceSecrets(serviceId)).resolves.toHaveLength(1);
    // Create now NIP-44 encrypts client-side and sends encrypted_value
    await expect(store.createServiceSecret(serviceId, { name: 'API_KEY', value: 'super-secret' })).resolves.toMatchObject({ id: 'secret-2' });
    await expect(store.revealServiceSecret(serviceId, secretId)).resolves.toBe('plaintext');
    await expect(store.deleteServiceSecret(serviceId, secretId)).resolves.toMatchObject({ status: 'deleted' });

    // List still uses ContextVM encrypted request
    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenNthCalledWith(1, expect.objectContaining({ operation: 'services.secrets.list', payload: { service_id: serviceId } }));
    // Create uses gift-wrapped requestEncryptedResult with NIP-44 client-side encrypted_value
    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenNthCalledWith(2, expect.objectContaining({
      operation: 'services.secrets.create',
      payload: { service_id: serviceId, name: 'API_KEY', encrypted_value: 'encrypted:super-secret' }
    }));
    // Reveal still uses ContextVM
    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenNthCalledWith(3, expect.objectContaining({ operation: 'services.secrets.reveal', payload: { service_id: serviceId, secret_id: secretId } }));
    // Delete uses gift-wrapped requestEncryptedResult
    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenNthCalledWith(4, expect.objectContaining({
      operation: 'services.secrets.delete',
      payload: { service_id: serviceId, secret_id: secretId }
    }));
    // Verify NIP-44 encryption was called with the service pubkey
    expect(nip07Mock.encryptNip44).toHaveBeenCalledWith('b'.repeat(64), 'super-secret');
  });

  it('unwraps legacy encrypted route payload envelopes for service secrets', async () => {
    const serviceId = 'svc-123';
    encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
      result: { status: 'ok', payload: { secrets: [{ id: 'secret-legacy', name: 'TOKEN', version: 1 }] } }
    });
    const store = await import('../../src/lib/stores/service-secrets.svelte.js');

    await expect(store.listServiceSecrets(serviceId)).resolves.toEqual([{ id: 'secret-legacy', name: 'TOKEN', version: 1 }]);
  });

  it('surfaces encrypted result errors for deployment run logs', async () => {
    encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
      result: { status: 'error', error: { message: 'run is still in progress' } }
    });
    const store = await import('../../src/lib/stores/deployment-run-logs.svelte.js');

    await expect(store.loadDeploymentRunLogs('run-1')).rejects.toThrow('run is still in progress');
    expect(store.deploymentRunLogsState.errorByRun['run-1']).toBe('run is still in progress');
    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenCalledWith(expect.objectContaining({
      operation: 'deployments/run-logs-get',
      payload: { run_id: 'run-1', tail: 100, stream: 'merged' }
    }));
  });

  it('verifies artifact signatures through ContextVM requests and records success state', async () => {
    encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
      result: { status: 'ok', payload: { found: 2, stored: 2, verified: 1, rejected: 1, signatures: [{ id: 'sig-1', verified: true }] } }
    });
    const store = await import('../../src/lib/stores/artifact-signatures.svelte.js');

    await expect(store.verifyArtifactSignatures('artifact-1')).resolves.toMatchObject({ found: 2, stored: 2, verified: 1 });
    expect(store.artifactSignatureState.lastResultByArtifact['artifact-1']).toMatchObject({ found: 2, stored: 2 });
    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenCalledWith(expect.objectContaining({
      operation: 'artifacts.signatures.verify',
      payload: { artifact_id: 'artifact-1' }
    }));
  });

  it('blocks ContextVM route stores when ContextVM requests are not configured for secrets', async () => {
    encryptedRequestsMock.encryptedRequestsAvailable.mockReturnValue(false);
    const store = await import('../../src/lib/stores/service-secrets.svelte.js');

    await expect(store.listServiceSecrets('svc-1')).rejects.toThrow(
      'ContextVM requests are not available for service secret management'
    );
    expect(encryptedRequestsMock.requestEncryptedResult).not.toHaveBeenCalled();
  });
});
